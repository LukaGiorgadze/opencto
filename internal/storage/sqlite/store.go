package sqlite

import (
	"bytes"
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/vec1"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

const (
	currentSchemaVersion = 9
	memoryVectorDims     = 1536
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("sqlite database path is required")
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create sqlite database directory: %w", err)
		}
	}

	db, err := driver.Open(dataSourceName(path), vec1.Register)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &Store{db: db}
	if err := db.PingContext(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func dataSourceName(path string) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(ON)")
	q.Set("_timefmt", "rfc3339")
	if path == ":memory:" {
		return path + "?" + q.Encode()
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return uri.String()
}

func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not open")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);
`); err != nil {
		return err
	}

	applied, err := appliedMigrations(ctx, tx)
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, migration := range migrations {
		if !applied[migration.version] {
			if err := applyMigration(ctx, s.db, migration); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) VerifySchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sqlite store is not open")
	}
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	if err != nil {
		return fmt.Errorf("database schema is not initialized; run worker once to apply migrations: %w", err)
	}
	if version < currentSchemaVersion {
		return fmt.Errorf("database schema version %d is behind required version %d; run worker once to apply migrations", version, currentSchemaVersion)
	}
	return nil
}

func appliedMigrations(ctx context.Context, tx *sql.Tx) (map[int]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

type migration struct {
	version int
	sql     string
	apply   func(context.Context, *sql.DB, migration) error
}

var migrations = []migration{
	{version: 1, sql: migrationV1},
	{version: 2, sql: migrationV2},
	{version: 3, sql: migrationV3},
	{version: 4, sql: migrationV4, apply: applyMemoryUserScopeMigration},
	{version: 5, sql: migrationV5, apply: applyThreadScopeMigration},
	{version: 6, sql: migrationV6},
	{version: 7, sql: migrationV7, apply: applyMemoryChannelScopeMigration},
	{version: 8, sql: migrationV8},
	{version: 9, sql: migrationV9},
}

func applyMigration(ctx context.Context, db *sql.DB, migration migration) error {
	if migration.apply != nil {
		return migration.apply(ctx, db, migration)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	return tx.Commit()
}

func applyMemoryUserScopeMigration(ctx context.Context, db *sql.DB, migration migration) error {
	hasUserID, err := tableHasColumn(ctx, db, "memories", "user_id")
	if err != nil {
		return err
	}
	if !hasUserID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		defer func() {
			_, _ = db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		}()
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if hasUserID {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_memories_user_scope_updated ON memories(user_id, scope, updated_at)`); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !hasUserID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		if rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`); err == nil {
			defer rows.Close()
			if rows.Next() {
				return fmt.Errorf("foreign key check failed after memory user scope migration")
			}
			if err := rows.Err(); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func applyThreadScopeMigration(ctx context.Context, db *sql.DB, migration migration) error {
	hasThreadID, err := tableHasColumn(ctx, db, "memories", "thread_id")
	if err != nil {
		return err
	}
	if !hasThreadID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		defer func() {
			_, _ = db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		}()
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if hasThreadID {
		if _, err := tx.ExecContext(ctx, conversationThreadsSchemaSQL); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_memories_thread_scope_updated ON memories(project_id, thread_id, scope, updated_at)`); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !hasThreadID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		if rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`); err == nil {
			defer rows.Close()
			if rows.Next() {
				return fmt.Errorf("foreign key check failed after memory thread scope migration")
			}
			if err := rows.Err(); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func applyMemoryChannelScopeMigration(ctx context.Context, db *sql.DB, migration migration) error {
	hasChannelID, err := tableHasColumn(ctx, db, "memories", "channel_id")
	if err != nil {
		return err
	}
	if !hasChannelID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
			return err
		}
		defer func() {
			_, _ = db.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		}()
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if hasChannelID {
		if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_memories_channel_scope_updated ON memories(project_id, channel_type, channel_id, scope, updated_at)`); err != nil {
			return err
		}
	} else if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, migration.version, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if !hasChannelID {
		if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
			return err
		}
		if rows, err := db.QueryContext(ctx, `PRAGMA foreign_key_check`); err == nil {
			defer rows.Close()
			if rows.Next() {
				return fmt.Errorf("foreign key check failed after memory channel scope migration")
			}
			if err := rows.Err(); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

const conversationThreadsSchemaSQL = `
CREATE TABLE IF NOT EXISTS conversation_threads (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	channel_type TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	root_message_id TEXT NOT NULL DEFAULT '',
	workflow_id TEXT NOT NULL DEFAULT '',
	event_id TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'active',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_message_at TEXT NOT NULL,
	UNIQUE(project_id, channel_type, channel_id, thread_id)
);
CREATE INDEX IF NOT EXISTS idx_conversation_threads_project_updated ON conversation_threads(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_threads_project_channel ON conversation_threads(project_id, channel_type, channel_id);
`

const conversationSummariesSchemaSQL = `
CREATE TABLE IF NOT EXISTS conversation_summaries (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	channel_type TEXT NOT NULL DEFAULT '',
	channel_id TEXT NOT NULL DEFAULT '',
	thread_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('project', 'channel', 'thread')),
	summary TEXT NOT NULL,
	from_message_id TEXT NOT NULL,
	to_message_id TEXT NOT NULL,
	from_created_at TEXT NOT NULL,
	to_created_at TEXT NOT NULL,
	message_count INTEGER NOT NULL DEFAULT 0,
	source_chars INTEGER NOT NULL DEFAULT 0,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_summaries_project_scope_to
ON conversation_summaries(project_id, scope, to_created_at, to_message_id);
CREATE INDEX IF NOT EXISTS idx_conversation_summaries_channel_to
ON conversation_summaries(project_id, channel_type, channel_id, scope, to_created_at, to_message_id);
CREATE INDEX IF NOT EXISTS idx_conversation_summaries_thread_to
ON conversation_summaries(project_id, channel_type, channel_id, thread_id, scope, to_created_at, to_message_id);
`

func tableHasColumn(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, column) {
			return true, nil
		}
	}
	return false, rows.Err()
}

const migrationV1 = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	channel_id TEXT NOT NULL DEFAULT '',
	channel_type TEXT NOT NULL DEFAULT '',
	actor_id TEXT NOT NULL DEFAULT '',
	actor_name TEXT NOT NULL DEFAULT '',
	body TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
	provenance TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(provenance)),
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_project_created ON events(project_id, created_at);

CREATE TABLE IF NOT EXISTS work_items (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	description TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_work_items_project_status ON work_items(project_id, status, updated_at);

CREATE TABLE IF NOT EXISTS execution_attempts (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	work_item_id TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	attempt INTEGER NOT NULL,
	tool TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	output_summary TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	started_at TEXT NOT NULL,
	completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_execution_attempts_project_work_item ON execution_attempts(project_id, work_item_id, started_at);

CREATE TABLE IF NOT EXISTS tool_invocations (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	execution_attempt_id TEXT NOT NULL,
	requested_intent TEXT NOT NULL DEFAULT '',
	chosen_tool TEXT NOT NULL,
	fallback_candidates TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(fallback_candidates)),
	working_directory TEXT NOT NULL DEFAULT '',
	timeout_seconds INTEGER NOT NULL DEFAULT 0,
	input_summary TEXT NOT NULL DEFAULT '',
	input_payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(input_payload)),
	output_summary TEXT NOT NULL DEFAULT '',
	output_payload TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(output_payload)),
	result_code TEXT NOT NULL DEFAULT '',
	error_details TEXT NOT NULL DEFAULT '',
	compensation_notes TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	completed_at TEXT
);
CREATE INDEX IF NOT EXISTS idx_tool_invocations_project_created ON tool_invocations(project_id, created_at);
CREATE INDEX IF NOT EXISTS idx_tool_invocations_execution_attempt ON tool_invocations(execution_attempt_id);

CREATE TABLE IF NOT EXISTS conversation_messages (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	event_id TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL,
	body TEXT NOT NULL DEFAULT '',
	tool_call_id TEXT NOT NULL DEFAULT '',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_conversation_messages_project_created ON conversation_messages(project_id, created_at);

CREATE TABLE IF NOT EXISTS memories (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('project', 'user', 'global')),
	kind TEXT NOT NULL DEFAULT 'fact',
	content TEXT NOT NULL,
	tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
	source TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 1.0,
	pinned INTEGER NOT NULL DEFAULT 0,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_memories_project_scope_updated ON memories(project_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_user_scope_updated ON memories(user_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_scope_updated ON memories(scope, updated_at);

	CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
		memory_id UNINDEXED,
		project_id UNINDEXED,
		scope UNINDEXED,
		content,
		tags
	);
	`

const migrationV2 = `
	ALTER TABLE events ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';

	ALTER TABLE conversation_messages ADD COLUMN channel_type TEXT NOT NULL DEFAULT '';
	ALTER TABLE conversation_messages ADD COLUMN channel_id TEXT NOT NULL DEFAULT '';
	ALTER TABLE conversation_messages ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';

	UPDATE conversation_messages
	SET
		channel_type = COALESCE((SELECT events.channel_type FROM events WHERE events.id = conversation_messages.event_id), ''),
		channel_id = COALESCE((SELECT events.channel_id FROM events WHERE events.id = conversation_messages.event_id), ''),
		thread_id = COALESCE((SELECT events.thread_id FROM events WHERE events.id = conversation_messages.event_id), '')
	WHERE event_id <> '';

	CREATE INDEX IF NOT EXISTS idx_conversation_messages_project_scope_created
	ON conversation_messages(project_id, channel_type, channel_id, thread_id, created_at);
	`

const migrationV3 = `
CREATE TABLE IF NOT EXISTS memory_embeddings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	memory_id TEXT NOT NULL UNIQUE,
	provider TEXT NOT NULL,
	model TEXT NOT NULL,
	dimensions INTEGER NOT NULL,
	content_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	FOREIGN KEY(memory_id) REFERENCES memories(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_memory_embeddings_profile ON memory_embeddings(provider, model, dimensions);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_embedding_vec USING vec1(embedding);
INSERT INTO memory_embedding_vec(cmd, embedding) VALUES('rebuild', '{index:"flat", distance:"cos"}');
`

const migrationV4 = `
CREATE TABLE memories_new (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('project', 'user', 'global')),
	kind TEXT NOT NULL DEFAULT 'fact',
	content TEXT NOT NULL,
	tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
	source TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 1.0,
	pinned INTEGER NOT NULL DEFAULT 0,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
INSERT INTO memories_new(id, project_id, user_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at)
SELECT id, project_id, '', scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories;
DROP TABLE memories;
ALTER TABLE memories_new RENAME TO memories;
CREATE INDEX IF NOT EXISTS idx_memories_project_scope_updated ON memories(project_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_user_scope_updated ON memories(user_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_scope_updated ON memories(scope, updated_at);
`

const migrationV5 = `
CREATE TABLE IF NOT EXISTS conversation_threads (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL,
	channel_type TEXT NOT NULL,
	channel_id TEXT NOT NULL,
	thread_id TEXT NOT NULL,
	root_message_id TEXT NOT NULL DEFAULT '',
	workflow_id TEXT NOT NULL DEFAULT '',
	event_id TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'active',
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	last_message_at TEXT NOT NULL,
	UNIQUE(project_id, channel_type, channel_id, thread_id)
);
CREATE INDEX IF NOT EXISTS idx_conversation_threads_project_updated ON conversation_threads(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_conversation_threads_project_channel ON conversation_threads(project_id, channel_type, channel_id);

CREATE TABLE memories_new (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	thread_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('thread', 'project', 'user', 'global')),
	kind TEXT NOT NULL DEFAULT 'fact',
	content TEXT NOT NULL,
	tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
	source TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 1.0,
	pinned INTEGER NOT NULL DEFAULT 0,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
INSERT INTO memories_new(id, project_id, user_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at)
SELECT id, project_id, user_id, '', scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories;
DROP TABLE memories;
ALTER TABLE memories_new RENAME TO memories;
CREATE INDEX IF NOT EXISTS idx_memories_project_scope_updated ON memories(project_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_user_scope_updated ON memories(user_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_thread_scope_updated ON memories(project_id, thread_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_scope_updated ON memories(scope, updated_at);
`

const migrationV6 = conversationSummariesSchemaSQL

const migrationV7 = `
CREATE TABLE memories_new (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL DEFAULT '',
	user_id TEXT NOT NULL DEFAULT '',
	channel_type TEXT NOT NULL DEFAULT '',
	channel_id TEXT NOT NULL DEFAULT '',
	thread_id TEXT NOT NULL DEFAULT '',
	scope TEXT NOT NULL CHECK (scope IN ('thread', 'channel', 'project', 'user', 'global')),
	kind TEXT NOT NULL DEFAULT 'fact',
	content TEXT NOT NULL,
	tags TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(tags)),
	source TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	actor TEXT NOT NULL DEFAULT '',
	confidence REAL NOT NULL DEFAULT 1.0,
	pinned INTEGER NOT NULL DEFAULT 0,
	metadata TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(metadata)),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);
INSERT INTO memories_new(id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at)
SELECT id, project_id, user_id, '', '', thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories;
DROP TABLE memories;
ALTER TABLE memories_new RENAME TO memories;
CREATE INDEX IF NOT EXISTS idx_memories_project_scope_updated ON memories(project_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_user_scope_updated ON memories(user_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_channel_scope_updated ON memories(project_id, channel_type, channel_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_thread_scope_updated ON memories(project_id, thread_id, scope, updated_at);
CREATE INDEX IF NOT EXISTS idx_memories_scope_updated ON memories(scope, updated_at);
`

const migrationV8 = `
UPDATE memories
SET kind = 'fact'
WHERE lower(trim(kind)) IN ('project', 'user');
`

const migrationV9 = `
DROP TABLE IF EXISTS conversation_threads;
` + conversationThreadsSchemaSQL

func (s *Store) EnsureProject(ctx context.Context, project domain.Project) error {
	project.ID = strings.TrimSpace(project.ID)
	if project.ID == "" {
		return fmt.Errorf("project id is required")
	}
	project.Name = strings.TrimSpace(project.Name)
	if project.Name == "" {
		project.Name = project.ID
	}
	now := time.Now().UTC()
	if project.CreatedAt.IsZero() {
		project.CreatedAt = now
	}
	if project.UpdatedAt.IsZero() {
		project.UpdatedAt = project.CreatedAt
	}
	metadata, err := encodeJSON(project.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO projects(id, name, description, metadata, created_at, updated_at)
VALUES (?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO NOTHING
`, project.ID, project.Name, strings.TrimSpace(project.Description), metadata, formatTime(project.CreatedAt), formatTime(project.UpdatedAt))
	return err
}

func (s *Store) AppendEvent(ctx context.Context, event domain.Event) (storage.EventAppendResult, error) {
	event.ID = strings.TrimSpace(event.ID)
	if event.ID == "" {
		return storage.EventAppendResult{}, fmt.Errorf("event id is required")
	}
	event.ProjectID = strings.TrimSpace(event.ProjectID)
	if event.ProjectID == "" {
		return storage.EventAppendResult{}, fmt.Errorf("event project id is required")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	record, err := eventRecordFromDomain(event)
	if err != nil {
		return storage.EventAppendResult{}, err
	}

	var existing eventRecord
	err = s.db.QueryRowContext(ctx, `
	SELECT project_id, kind, channel_id, channel_type, thread_id, actor_id, actor_name, body, metadata, payload, provenance, created_at
	FROM events
	WHERE id = ?
	`, event.ID).Scan(
		&existing.ProjectID,
		&existing.Kind,
		&existing.ChannelID,
		&existing.ChannelType,
		&existing.ThreadID,
		&existing.ActorID,
		&existing.ActorName,
		&existing.Body,
		&existing.Metadata,
		&existing.Payload,
		&existing.Provenance,
		&existing.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.ExecContext(ctx, `
	INSERT INTO events(id, project_id, kind, channel_id, channel_type, thread_id, actor_id, actor_name, body, metadata, payload, provenance, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, json(?), json(?), json(?), ?)
	`, event.ID, record.ProjectID, record.Kind, record.ChannelID, record.ChannelType, record.ThreadID, record.ActorID, record.ActorName, record.Body, record.Metadata, record.Payload, record.Provenance, record.CreatedAt)
		return storage.EventAppendResult{Inserted: true}, err
	}
	if err != nil {
		return storage.EventAppendResult{}, err
	}

	changed := !existing.equal(record)
	if changed {
		_, err = s.db.ExecContext(ctx, `
	UPDATE events
	SET project_id = ?, kind = ?, channel_id = ?, channel_type = ?, thread_id = ?, actor_id = ?, actor_name = ?, body = ?, metadata = json(?), payload = json(?), provenance = json(?), created_at = ?
	WHERE id = ?
	`, record.ProjectID, record.Kind, record.ChannelID, record.ChannelType, record.ThreadID, record.ActorID, record.ActorName, record.Body, record.Metadata, record.Payload, record.Provenance, record.CreatedAt, event.ID)
		if err != nil {
			return storage.EventAppendResult{}, err
		}
	}
	return storage.EventAppendResult{Updated: true, Changed: changed}, nil
}

type eventRecord struct {
	ProjectID   string
	Kind        string
	ChannelID   string
	ChannelType string
	ThreadID    string
	ActorID     string
	ActorName   string
	Body        string
	Metadata    string
	Payload     string
	Provenance  string
	CreatedAt   string
}

func eventRecordFromDomain(event domain.Event) (eventRecord, error) {
	metadata, err := encodeJSON(event.Metadata, "{}")
	if err != nil {
		return eventRecord{}, err
	}
	payload, err := encodeJSON(event.Payload, "{}")
	if err != nil {
		return eventRecord{}, err
	}
	provenance, err := encodeJSON(event.Provenance, "{}")
	if err != nil {
		return eventRecord{}, err
	}
	return eventRecord{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		Kind:        string(event.Kind),
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ChannelType: string(event.ChannelType),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		ActorID:     strings.TrimSpace(event.ActorID),
		ActorName:   strings.TrimSpace(event.ActorName),
		Body:        event.Body,
		Metadata:    metadata,
		Payload:     payload,
		Provenance:  provenance,
		CreatedAt:   formatTime(event.CreatedAt),
	}, nil
}

func (r eventRecord) equal(other eventRecord) bool {
	return r.ProjectID == other.ProjectID &&
		r.Kind == other.Kind &&
		r.ChannelID == other.ChannelID &&
		r.ChannelType == other.ChannelType &&
		r.ThreadID == other.ThreadID &&
		r.ActorID == other.ActorID &&
		r.ActorName == other.ActorName &&
		r.Body == other.Body &&
		normalizeJSONText(r.Metadata) == normalizeJSONText(other.Metadata) &&
		normalizeJSONText(r.Payload) == normalizeJSONText(other.Payload) &&
		normalizeJSONText(r.Provenance) == normalizeJSONText(other.Provenance) &&
		r.CreatedAt == other.CreatedAt
}

func (s *Store) ListPendingWorkItems(ctx context.Context, projectID string) ([]domain.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, title, description, status, metadata, created_at, updated_at
FROM work_items
WHERE project_id = ? AND status IN (?, ?, ?)
ORDER BY updated_at ASC
`, strings.TrimSpace(projectID), domain.WorkItemStatusPending, domain.WorkItemStatusReady, domain.WorkItemStatusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.WorkItem
	for rows.Next() {
		var item domain.WorkItem
		var status string
		var metadata string
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Title, &item.Description, &status, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		item.Status = domain.WorkItemStatus(status)
		if err := decodeJSON(metadata, &item.Metadata); err != nil {
			return nil, err
		}
		item.CreatedAt = parseTime(createdAt)
		item.UpdatedAt = parseTime(updatedAt)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertWorkItems(ctx context.Context, items []domain.WorkItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, item := range items {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		if strings.TrimSpace(item.ProjectID) == "" {
			return fmt.Errorf("work item %q project id is required", item.ID)
		}
		now := time.Now().UTC()
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = now
		}
		metadata, err := encodeJSON(item.Metadata, "{}")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO work_items(id, project_id, title, description, status, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	title = excluded.title,
	description = excluded.description,
	status = excluded.status,
	metadata = excluded.metadata,
	updated_at = excluded.updated_at
`, item.ID, item.ProjectID, item.Title, item.Description, string(item.Status), metadata, formatTime(item.CreatedAt), formatTime(item.UpdatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertExecutionAttempt(ctx context.Context, attempt domain.ExecutionAttempt) error {
	if strings.TrimSpace(attempt.ID) == "" {
		return fmt.Errorf("execution attempt id is required")
	}
	metadata, err := encodeJSON(attempt.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO execution_attempts(id, project_id, work_item_id, status, attempt, tool, summary, output_summary, metadata, started_at, completed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	work_item_id = excluded.work_item_id,
	status = excluded.status,
	attempt = excluded.attempt,
	tool = excluded.tool,
	summary = excluded.summary,
	output_summary = excluded.output_summary,
	metadata = excluded.metadata,
	started_at = excluded.started_at,
	completed_at = excluded.completed_at
`, attempt.ID, attempt.ProjectID, attempt.WorkItemID, string(attempt.Status), attempt.Attempt, string(attempt.Tool), attempt.Summary, attempt.OutputSummary, metadata, formatTime(attempt.StartedAt), nullableTime(attempt.CompletedAt))
	return err
}

func (s *Store) UpsertToolInvocation(ctx context.Context, invocation domain.ToolInvocation) error {
	if strings.TrimSpace(invocation.ID) == "" {
		return fmt.Errorf("tool invocation id is required")
	}
	fallbacks, err := encodeJSON(invocation.FallbackCandidates, "[]")
	if err != nil {
		return err
	}
	metadata, err := encodeJSON(invocation.Metadata, "{}")
	if err != nil {
		return err
	}
	inputPayload := jsonText(invocation.InputPayload, "{}")
	outputPayload := jsonText(invocation.OutputPayload, "{}")
	_, err = s.db.ExecContext(ctx, `
INSERT INTO tool_invocations(id, project_id, execution_attempt_id, requested_intent, chosen_tool, fallback_candidates, working_directory, timeout_seconds, input_summary, input_payload, output_summary, output_payload, result_code, error_details, compensation_notes, metadata, created_at, completed_at)
VALUES (?, ?, ?, ?, ?, json(?), ?, ?, ?, json(?), ?, json(?), ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	execution_attempt_id = excluded.execution_attempt_id,
	requested_intent = excluded.requested_intent,
	chosen_tool = excluded.chosen_tool,
	fallback_candidates = excluded.fallback_candidates,
	working_directory = excluded.working_directory,
	timeout_seconds = excluded.timeout_seconds,
	input_summary = excluded.input_summary,
	input_payload = excluded.input_payload,
	output_summary = excluded.output_summary,
	output_payload = excluded.output_payload,
	result_code = excluded.result_code,
	error_details = excluded.error_details,
	compensation_notes = excluded.compensation_notes,
	metadata = excluded.metadata,
	created_at = excluded.created_at,
	completed_at = excluded.completed_at
`, invocation.ID, invocation.ProjectID, invocation.ExecutionAttemptID, invocation.RequestedIntent, string(invocation.ChosenTool), fallbacks, invocation.WorkingDirectory, invocation.TimeoutSeconds, invocation.InputSummary, inputPayload, invocation.OutputSummary, outputPayload, invocation.ResultCode, invocation.ErrorDetails, invocation.CompensationNotes, metadata, formatTime(invocation.CreatedAt), nullableTime(invocation.CompletedAt))
	return err
}

func (s *Store) UpsertConversationThread(ctx context.Context, thread domain.ConversationThread) error {
	thread.ID = strings.TrimSpace(thread.ID)
	thread.ProjectID = strings.TrimSpace(thread.ProjectID)
	thread.ChannelID = strings.TrimSpace(thread.ChannelID)
	thread.ThreadID = strings.TrimSpace(thread.ThreadID)
	if thread.ID == "" {
		thread.ID = stableStoreID("conversation-thread", thread.ProjectID, string(thread.ChannelType), thread.ChannelID, thread.ThreadID)
	}
	if thread.ProjectID == "" {
		return fmt.Errorf("conversation thread project id is required")
	}
	if thread.ChannelType == "" {
		return fmt.Errorf("conversation thread channel type is required")
	}
	if thread.ChannelID == "" {
		return fmt.Errorf("conversation thread channel id is required")
	}
	if thread.ThreadID == "" {
		return fmt.Errorf("conversation thread id is required")
	}
	now := time.Now().UTC()
	if thread.CreatedAt.IsZero() {
		thread.CreatedAt = now
	}
	if thread.UpdatedAt.IsZero() {
		thread.UpdatedAt = now
	}
	if thread.LastMessageAt.IsZero() {
		thread.LastMessageAt = thread.UpdatedAt
	}
	if strings.TrimSpace(thread.Status) == "" {
		thread.Status = "active"
	}
	metadata, err := encodeJSON(thread.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversation_threads(id, project_id, channel_type, channel_id, thread_id, root_message_id, workflow_id, event_id, title, status, metadata, created_at, updated_at, last_message_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, json(?), ?, ?, ?)
ON CONFLICT(project_id, channel_type, channel_id, thread_id) DO UPDATE SET
	root_message_id = CASE WHEN excluded.root_message_id <> '' THEN excluded.root_message_id ELSE conversation_threads.root_message_id END,
	workflow_id = CASE WHEN excluded.workflow_id <> '' THEN excluded.workflow_id ELSE conversation_threads.workflow_id END,
	event_id = CASE WHEN excluded.event_id <> '' THEN excluded.event_id ELSE conversation_threads.event_id END,
	title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE conversation_threads.title END,
	status = CASE WHEN excluded.status <> '' THEN excluded.status ELSE conversation_threads.status END,
	metadata = excluded.metadata,
	updated_at = excluded.updated_at,
	last_message_at = CASE WHEN excluded.last_message_at > conversation_threads.last_message_at THEN excluded.last_message_at ELSE conversation_threads.last_message_at END
`, thread.ID, thread.ProjectID, string(thread.ChannelType), thread.ChannelID, thread.ThreadID, strings.TrimSpace(thread.RootMessageID), strings.TrimSpace(thread.WorkflowID), strings.TrimSpace(thread.EventID), strings.TrimSpace(thread.Title), strings.TrimSpace(thread.Status), metadata, formatTime(thread.CreatedAt), formatTime(thread.UpdatedAt), formatTime(thread.LastMessageAt))
	return err
}

func (s *Store) GetConversationThread(ctx context.Context, query storage.ConversationThreadQuery) (domain.ConversationThread, bool, error) {
	projectID := strings.TrimSpace(query.ProjectID)
	channelID := strings.TrimSpace(query.ChannelID)
	threadID := strings.TrimSpace(query.ThreadID)
	if projectID == "" || query.ChannelType == "" || channelID == "" || threadID == "" {
		return domain.ConversationThread{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
SELECT id, project_id, channel_type, channel_id, thread_id, root_message_id, workflow_id, event_id, title, status, metadata, created_at, updated_at, last_message_at
FROM conversation_threads
WHERE project_id = ? AND channel_type = ? AND channel_id = ? AND thread_id = ?
`, projectID, string(query.ChannelType), channelID, threadID)
	thread, err := scanConversationThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ConversationThread{}, false, nil
	}
	if err != nil {
		return domain.ConversationThread{}, false, err
	}
	return thread, true, nil
}

func (s *Store) GetConversationRootMessage(ctx context.Context, query storage.ConversationRootMessageQuery) (domain.ConversationMessage, bool, error) {
	projectID := strings.TrimSpace(query.ProjectID)
	messageID := strings.TrimSpace(query.MessageID)
	channelID := strings.TrimSpace(query.ChannelID)
	if projectID == "" || query.ChannelType == "" || channelID == "" || messageID == "" {
		return domain.ConversationMessage{}, false, nil
	}
	where := []string{"cm.project_id = ?", "cm.channel_type = ?", "cm.channel_id = ?", "(cm.thread_id = '' OR cm.thread_id = ?)"}
	args := []any{projectID, string(query.ChannelType), channelID, messageID}
	args = append(args, messageID, messageID, messageID)
	where = append(where, `(COALESCE(json_extract(e.provenance, '$.source_id'), '') = ? OR COALESCE(json_extract(e.payload, '$.message_id'), '') = ? OR COALESCE(json_extract(cm.metadata, '$.message_id'), '') = ?)`)
	rows, err := s.db.QueryContext(ctx, `
SELECT cm.id, cm.project_id, cm.event_id, cm.role, cm.channel_type, cm.channel_id, cm.thread_id, cm.body, cm.tool_call_id, cm.metadata, cm.created_at
FROM conversation_messages cm
LEFT JOIN events e ON e.id = cm.event_id
WHERE `+strings.Join(where, " AND ")+`
ORDER BY cm.created_at ASC, cm.id ASC
LIMIT 1
`, args...)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	defer rows.Close()
	messages, err := scanConversationMessages(rows)
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if len(messages) == 0 {
		return domain.ConversationMessage{}, false, nil
	}
	return messages[0], true, nil
}

type conversationThreadScanner interface {
	Scan(dest ...any) error
}

func scanConversationThread(row conversationThreadScanner) (domain.ConversationThread, error) {
	var thread domain.ConversationThread
	var channelType string
	var metadata string
	var createdAt string
	var updatedAt string
	var lastMessageAt string
	if err := row.Scan(&thread.ID, &thread.ProjectID, &channelType, &thread.ChannelID, &thread.ThreadID, &thread.RootMessageID, &thread.WorkflowID, &thread.EventID, &thread.Title, &thread.Status, &metadata, &createdAt, &updatedAt, &lastMessageAt); err != nil {
		return domain.ConversationThread{}, err
	}
	thread.ChannelType = domain.ChannelType(channelType)
	if err := decodeJSON(metadata, &thread.Metadata); err != nil {
		return domain.ConversationThread{}, err
	}
	thread.CreatedAt = parseTime(createdAt)
	thread.UpdatedAt = parseTime(updatedAt)
	thread.LastMessageAt = parseTime(lastMessageAt)
	return thread, nil
}

func (s *Store) UpsertConversationMessage(ctx context.Context, message domain.ConversationMessage) error {
	message.ID = strings.TrimSpace(message.ID)
	message.ProjectID = strings.TrimSpace(message.ProjectID)
	message.ChannelID = strings.TrimSpace(message.ChannelID)
	message.ThreadID = strings.TrimSpace(message.ThreadID)
	if strings.TrimSpace(message.ID) == "" {
		return fmt.Errorf("conversation message id is required")
	}
	if strings.TrimSpace(message.ProjectID) == "" {
		return fmt.Errorf("conversation message project id is required")
	}
	if message.ChannelID != "" && message.ChannelType == "" {
		return fmt.Errorf("conversation channel message requires channel type")
	}
	if message.ThreadID != "" && (message.ChannelType == "" || message.ChannelID == "") {
		return fmt.Errorf("conversation thread message requires channel type and id")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	metadata, err := encodeJSON(message.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
	INSERT INTO conversation_messages(id, project_id, event_id, role, channel_type, channel_id, thread_id, body, tool_call_id, metadata, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, json(?), ?)
	ON CONFLICT(id) DO UPDATE SET
		project_id = excluded.project_id,
		event_id = excluded.event_id,
		role = excluded.role,
		channel_type = excluded.channel_type,
		channel_id = excluded.channel_id,
		thread_id = excluded.thread_id,
		body = excluded.body,
		tool_call_id = excluded.tool_call_id,
		metadata = excluded.metadata,
		created_at = excluded.created_at
	`, message.ID, message.ProjectID, strings.TrimSpace(message.EventID), string(message.Role), string(message.ChannelType), message.ChannelID, message.ThreadID, message.Body, strings.TrimSpace(message.ToolCallID), metadata, formatTime(message.CreatedAt))
	return err
}

func (s *Store) ListConversationMessages(ctx context.Context, query storage.ConversationQuery) ([]domain.ConversationMessage, error) {
	projectID := strings.TrimSpace(query.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("conversation project id is required")
	}
	limit := storage.DefaultConversationHistoryLimit(query.Limit)
	maxLimit := 100
	if query.OldestFirst {
		maxLimit = 500
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	where := []string{"project_id = ?"}
	args := []any{projectID}
	switch query.Scope {
	case storage.ConversationScopeThread:
		channelID := strings.TrimSpace(query.ChannelID)
		threadID := strings.TrimSpace(query.ThreadID)
		if channelID == "" || threadID == "" {
			return nil, nil
		}
		where = append(where, "channel_type = ?", "channel_id = ?", "thread_id = ?")
		args = append(args, string(query.ChannelType), channelID, threadID)
	case storage.ConversationScopeChannel:
		channelID := strings.TrimSpace(query.ChannelID)
		if channelID == "" {
			return nil, nil
		}
		where = append(where, "channel_type = ?", "channel_id = ?", "thread_id = ''")
		args = append(args, string(query.ChannelType), channelID)
	case storage.ConversationScopeProject:
		where = append(where, "channel_id = ''", "thread_id = ''")
	default:
		return nil, fmt.Errorf("unsupported conversation scope %q", query.Scope)
	}
	if eventID := strings.TrimSpace(query.ExcludeEventID); eventID != "" {
		where = append(where, "event_id <> ?")
		args = append(args, eventID)
	}
	if query.ExcludeControl {
		where = append(where, "COALESCE(json_extract(metadata, '$."+domain.MetadataKeyControl+"'), '') = ''")
	}
	if !query.AfterCreatedAt.IsZero() {
		where = append(where, "(created_at > ? OR (created_at = ? AND id > ?))")
		after := formatTime(query.AfterCreatedAt)
		args = append(args, after, after, strings.TrimSpace(query.AfterID))
	}
	if !query.BeforeCreatedAt.IsZero() {
		before := formatTime(query.BeforeCreatedAt)
		if beforeID := strings.TrimSpace(query.BeforeID); beforeID != "" {
			where = append(where, "(created_at < ? OR (created_at = ? AND id <= ?))")
			args = append(args, before, before, beforeID)
		} else {
			where = append(where, "created_at <= ?")
			args = append(args, before)
		}
	}
	roles := normalizeConversationRoles(query.Roles)
	if len(roles) > 0 {
		placeholders := make([]string, 0, len(roles))
		for _, role := range roles {
			placeholders = append(placeholders, "?")
			args = append(args, string(role))
		}
		where = append(where, "role IN ("+strings.Join(placeholders, ", ")+")")
	}
	args = append(args, limit)
	querySQL := `
SELECT id, project_id, event_id, role, channel_type, channel_id, thread_id, body, tool_call_id, metadata, created_at
FROM (
	SELECT id, project_id, event_id, role, channel_type, channel_id, thread_id, body, tool_call_id, metadata, created_at
	FROM conversation_messages
	WHERE ` + strings.Join(where, " AND ") + `
	ORDER BY created_at DESC, id DESC
	LIMIT ?
)
ORDER BY created_at ASC, id ASC
`
	if query.OldestFirst {
		querySQL = `
SELECT id, project_id, event_id, role, channel_type, channel_id, thread_id, body, tool_call_id, metadata, created_at
FROM conversation_messages
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY created_at ASC, id ASC
LIMIT ?
`
	}
	rows, err := s.db.QueryContext(ctx, querySQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversationMessages(rows)
}

func normalizeConversationRoles(roles []domain.ConversationRole) []domain.ConversationRole {
	if len(roles) == 0 {
		return []domain.ConversationRole{
			domain.ConversationRoleUser,
			domain.ConversationRoleAssistant,
			domain.ConversationRoleTool,
		}
	}
	seen := map[domain.ConversationRole]bool{}
	normalized := make([]domain.ConversationRole, 0, len(roles))
	for _, role := range roles {
		switch role {
		case domain.ConversationRoleUser, domain.ConversationRoleAssistant, domain.ConversationRoleTool:
			if !seen[role] {
				seen[role] = true
				normalized = append(normalized, role)
			}
		}
	}
	return normalized
}

func scanConversationMessages(rows *sql.Rows) ([]domain.ConversationMessage, error) {
	var messages []domain.ConversationMessage
	for rows.Next() {
		var message domain.ConversationMessage
		var role string
		var channelType string
		var metadata string
		var createdAt string
		if err := rows.Scan(&message.ID, &message.ProjectID, &message.EventID, &role, &channelType, &message.ChannelID, &message.ThreadID, &message.Body, &message.ToolCallID, &metadata, &createdAt); err != nil {
			return nil, err
		}
		message.Role = domain.ConversationRole(role)
		message.ChannelType = domain.ChannelType(channelType)
		if err := decodeJSON(metadata, &message.Metadata); err != nil {
			return nil, err
		}
		message.CreatedAt = parseTime(createdAt)
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

func (s *Store) UpsertConversationSummary(ctx context.Context, summary domain.ConversationSummary) error {
	summary.ProjectID = strings.TrimSpace(summary.ProjectID)
	summary.ChannelID = strings.TrimSpace(summary.ChannelID)
	summary.ThreadID = strings.TrimSpace(summary.ThreadID)
	summary.Summary = strings.TrimSpace(summary.Summary)
	summary.FromMessageID = strings.TrimSpace(summary.FromMessageID)
	summary.ToMessageID = strings.TrimSpace(summary.ToMessageID)
	if summary.ProjectID == "" {
		return fmt.Errorf("conversation summary project id is required")
	}
	if summary.Summary == "" {
		return fmt.Errorf("conversation summary body is required")
	}
	switch summary.Scope {
	case domain.ConversationSummaryScopeProject:
		summary.ChannelType = ""
		summary.ChannelID = ""
		summary.ThreadID = ""
	case domain.ConversationSummaryScopeChannel:
		if summary.ChannelType == "" || summary.ChannelID == "" {
			return fmt.Errorf("conversation channel summary requires channel type and id")
		}
		summary.ThreadID = ""
	case domain.ConversationSummaryScopeThread:
		if summary.ChannelType == "" || summary.ChannelID == "" || summary.ThreadID == "" {
			return fmt.Errorf("conversation thread summary requires channel type, channel id, and thread id")
		}
	default:
		return fmt.Errorf("unsupported conversation summary scope %q", summary.Scope)
	}
	if summary.FromMessageID == "" || summary.ToMessageID == "" {
		return fmt.Errorf("conversation summary message range is required")
	}
	if summary.FromCreatedAt.IsZero() || summary.ToCreatedAt.IsZero() {
		return fmt.Errorf("conversation summary message timestamps are required")
	}
	if summary.ID == "" {
		summary.ID = stableStoreID("conversation-summary", summary.ProjectID, string(summary.Scope), string(summary.ChannelType), summary.ChannelID, summary.ThreadID, summary.FromMessageID, summary.ToMessageID)
	}
	now := time.Now().UTC()
	if summary.CreatedAt.IsZero() {
		summary.CreatedAt = now
	}
	if summary.UpdatedAt.IsZero() {
		summary.UpdatedAt = now
	}
	metadata, err := encodeJSON(summary.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversation_summaries(id, project_id, channel_type, channel_id, thread_id, scope, summary, from_message_id, to_message_id, from_created_at, to_created_at, message_count, source_chars, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	summary = excluded.summary,
	message_count = excluded.message_count,
	source_chars = excluded.source_chars,
	metadata = excluded.metadata,
	updated_at = excluded.updated_at
`, strings.TrimSpace(summary.ID), summary.ProjectID, string(summary.ChannelType), summary.ChannelID, summary.ThreadID, string(summary.Scope), summary.Summary, summary.FromMessageID, summary.ToMessageID, formatTime(summary.FromCreatedAt), formatTime(summary.ToCreatedAt), summary.MessageCount, summary.SourceChars, metadata, formatTime(summary.CreatedAt), formatTime(summary.UpdatedAt))
	return err
}

func (s *Store) ListConversationSummaries(ctx context.Context, query storage.ConversationSummaryQuery) ([]domain.ConversationSummary, error) {
	projectID := strings.TrimSpace(query.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("conversation summary project id is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 3
	}
	if limit > 20 {
		limit = 20
	}
	where := []string{"project_id = ?", "scope = ?"}
	args := []any{projectID, string(query.Scope)}
	switch query.Scope {
	case domain.ConversationSummaryScopeProject:
		where = append(where, "channel_id = ''", "thread_id = ''")
	case domain.ConversationSummaryScopeChannel:
		channelID := strings.TrimSpace(query.ChannelID)
		if channelID == "" {
			return nil, nil
		}
		where = append(where, "channel_type = ?", "channel_id = ?", "thread_id = ''")
		args = append(args, string(query.ChannelType), channelID)
	case domain.ConversationSummaryScopeThread:
		channelID := strings.TrimSpace(query.ChannelID)
		threadID := strings.TrimSpace(query.ThreadID)
		if channelID == "" || threadID == "" {
			return nil, nil
		}
		where = append(where, "channel_type = ?", "channel_id = ?", "thread_id = ?")
		args = append(args, string(query.ChannelType), channelID, threadID)
	default:
		return nil, fmt.Errorf("unsupported conversation summary scope %q", query.Scope)
	}
	if !query.BeforeCreatedAt.IsZero() {
		before := formatTime(query.BeforeCreatedAt)
		if beforeID := strings.TrimSpace(query.BeforeID); beforeID != "" {
			where = append(where, "(to_created_at < ? OR (to_created_at = ? AND to_message_id <= ?))")
			args = append(args, before, before, beforeID)
		} else {
			where = append(where, "to_created_at <= ?")
			args = append(args, before)
		}
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, channel_type, channel_id, thread_id, scope, summary, from_message_id, to_message_id, from_created_at, to_created_at, message_count, source_chars, metadata, created_at, updated_at
FROM (
	SELECT id, project_id, channel_type, channel_id, thread_id, scope, summary, from_message_id, to_message_id, from_created_at, to_created_at, message_count, source_chars, metadata, created_at, updated_at
	FROM conversation_summaries
	WHERE `+strings.Join(where, " AND ")+`
	ORDER BY to_created_at DESC, to_message_id DESC
	LIMIT ?
)
ORDER BY to_created_at ASC, to_message_id ASC
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanConversationSummaries(rows)
}

func scanConversationSummaries(rows *sql.Rows) ([]domain.ConversationSummary, error) {
	var summaries []domain.ConversationSummary
	for rows.Next() {
		var summary domain.ConversationSummary
		var channelType string
		var scope string
		var metadata string
		var fromCreatedAt string
		var toCreatedAt string
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&summary.ID, &summary.ProjectID, &channelType, &summary.ChannelID, &summary.ThreadID, &scope, &summary.Summary, &summary.FromMessageID, &summary.ToMessageID, &fromCreatedAt, &toCreatedAt, &summary.MessageCount, &summary.SourceChars, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		summary.ChannelType = domain.ChannelType(channelType)
		summary.Scope = domain.ConversationSummaryScope(scope)
		if err := decodeJSON(metadata, &summary.Metadata); err != nil {
			return nil, err
		}
		summary.FromCreatedAt = parseTime(fromCreatedAt)
		summary.ToCreatedAt = parseTime(toCreatedAt)
		summary.CreatedAt = parseTime(createdAt)
		summary.UpdatedAt = parseTime(updatedAt)
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (s *Store) RememberMemory(ctx context.Context, memory domain.Memory) (domain.Memory, error) {
	memory.ID = strings.TrimSpace(memory.ID)
	if memory.ID == "" {
		return domain.Memory{}, fmt.Errorf("memory id is required")
	}
	memory.Scope = normalizeMemoryScope(memory.Scope)
	memory.ProjectID = strings.TrimSpace(memory.ProjectID)
	memory.ChannelID = strings.TrimSpace(memory.ChannelID)
	memory.ThreadID = strings.TrimSpace(memory.ThreadID)
	memory.UserID = strings.TrimSpace(memory.UserID)
	if memory.ChannelID == "" {
		memory.ChannelType = ""
	}
	switch memory.Scope {
	case domain.MemoryScopeGlobal:
		memory.ProjectID = ""
		memory.UserID = ""
		memory.ChannelType = ""
		memory.ChannelID = ""
		memory.ThreadID = ""
	case domain.MemoryScopeUser:
		if memory.UserID == "" {
			return domain.Memory{}, memoryPolicyError("user memory user id is required")
		}
		memory.ProjectID = ""
		memory.ChannelType = ""
		memory.ChannelID = ""
		memory.ThreadID = ""
	case domain.MemoryScopeProject:
		if memory.ProjectID == "" {
			return domain.Memory{}, fmt.Errorf("project memory project id is required")
		}
		memory.UserID = ""
		memory.ChannelType = ""
		memory.ChannelID = ""
		memory.ThreadID = ""
	case domain.MemoryScopeChannel:
		if memory.ProjectID == "" || memory.ChannelType == "" || memory.ChannelID == "" {
			return domain.Memory{}, memoryPolicyError("channel memory project id, channel type, and channel id are required")
		}
		memory.UserID = ""
		memory.ThreadID = ""
	case domain.MemoryScopeThread:
		if memory.ProjectID == "" || memory.ChannelType == "" || memory.ChannelID == "" || memory.ThreadID == "" {
			return domain.Memory{}, memoryPolicyError("thread memory project id, channel type, channel id, and thread id are required")
		}
		memory.UserID = ""
	}
	memory.Content = strings.TrimSpace(memory.Content)
	kind, err := normalizeMemoryKind(memory.Kind)
	if err != nil {
		return domain.Memory{}, err
	}
	memory.Kind = kind
	if err := validateMemoryPolicy(memory.Content, memory.Kind); err != nil {
		return domain.Memory{}, err
	}
	if memory.Confidence <= 0 {
		memory.Confidence = 1
	}
	if memory.Confidence > 1 {
		return domain.Memory{}, fmt.Errorf("memory confidence must be between 0 and 1")
	}
	now := time.Now().UTC()
	if memory.CreatedAt.IsZero() {
		memory.CreatedAt = now
	}
	if memory.UpdatedAt.IsZero() {
		memory.UpdatedAt = now
	}
	memory.Tags = cleanTags(memory.Tags)
	tags, err := encodeJSON(memory.Tags, "[]")
	if err != nil {
		return domain.Memory{}, err
	}
	metadata, err := encodeJSON(memory.Metadata, "{}")
	if err != nil {
		return domain.Memory{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Memory{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	duplicateID, err := duplicateMemoryID(ctx, tx, memory)
	if err != nil {
		return domain.Memory{}, err
	}
	if duplicateID != "" {
		return domain.Memory{}, memoryPolicyError("exact duplicate of existing memory %s", duplicateID)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memories(id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, json(?), ?, ?, ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	user_id = excluded.user_id,
	channel_type = excluded.channel_type,
	channel_id = excluded.channel_id,
	thread_id = excluded.thread_id,
	scope = excluded.scope,
	kind = excluded.kind,
	content = excluded.content,
	tags = excluded.tags,
	source = excluded.source,
	source_id = excluded.source_id,
	actor = excluded.actor,
	confidence = excluded.confidence,
	pinned = excluded.pinned,
	metadata = excluded.metadata,
	updated_at = excluded.updated_at
`, memory.ID, memory.ProjectID, memory.UserID, string(memory.ChannelType), memory.ChannelID, memory.ThreadID, string(memory.Scope), memory.Kind, memory.Content, tags, strings.TrimSpace(memory.Source), strings.TrimSpace(memory.SourceID), strings.TrimSpace(memory.Actor), memory.Confidence, boolInt(memory.Pinned), metadata, formatTime(memory.CreatedAt), formatTime(memory.UpdatedAt)); err != nil {
		return domain.Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id = ?`, memory.ID); err != nil {
		return domain.Memory{}, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memory_fts(memory_id, project_id, scope, content, tags)
VALUES (?, ?, ?, ?, ?)
`, memory.ID, strings.TrimSpace(memory.ProjectID), string(memory.Scope), memory.Content, strings.Join(memory.Tags, " ")); err != nil {
		return domain.Memory{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.Memory{}, err
	}
	return memory, nil
}

func (s *Store) SearchMemories(ctx context.Context, request domain.MemorySearchRequest) ([]domain.Memory, error) {
	limit := storage.DefaultAutoContextLimit(request.Limit)
	if limit > 20 {
		limit = 20
	}
	projectID := strings.TrimSpace(request.ProjectID)
	userID := strings.TrimSpace(request.UserID)
	channelType := request.ChannelType
	channelID := strings.TrimSpace(request.ChannelID)
	threadID := strings.TrimSpace(request.ThreadID)
	scopes := normalizeMemoryScopes(request.Scopes)
	tags := cleanTags(request.Tags)
	query := ftsQuery(request.Query)
	var ftsMemories []domain.Memory
	var vectorMemories []domain.Memory
	if query != "" {
		var err error
		ftsMemories, err = s.searchMemoriesFTS(ctx, projectID, userID, channelType, channelID, threadID, scopes, query, tags, limit)
		if err != nil {
			return nil, err
		}
	}
	if len(request.QueryEmbedding) > 0 {
		var err error
		vectorMemories, err = s.searchMemoriesVector(ctx, projectID, userID, channelType, channelID, threadID, scopes, tags, request, limit)
		if err != nil {
			return nil, err
		}
	}
	if len(ftsMemories) > 0 || len(vectorMemories) > 0 {
		return fuseMemoryResults(limit, ftsMemories, vectorMemories), nil
	}
	if query != "" && !request.FallbackRecent {
		return nil, nil
	}
	memories, err := s.recentMemories(ctx, projectID, userID, channelType, channelID, threadID, scopes, tags, limit)
	if err != nil {
		return nil, err
	}
	return memories, nil
}

func (s *Store) ListMemories(ctx context.Context, request domain.MemoryListRequest) ([]domain.Memory, error) {
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	projectID := strings.TrimSpace(request.ProjectID)
	userID := strings.TrimSpace(request.UserID)
	channelType := request.ChannelType
	channelID := strings.TrimSpace(request.ChannelID)
	threadID := strings.TrimSpace(request.ThreadID)
	scopes := normalizeMemoryScopes(request.Scopes)
	tags := cleanTags(request.Tags)
	scopeSQL, args := memoryVisibilitySQL(projectID, userID, channelType, channelID, threadID, scopes)
	tagSQL, tagArgs := memoryTagsSQL(tags)
	if kind := strings.TrimSpace(request.Kind); kind != "" {
		normalized, err := normalizeMemoryKind(kind)
		if err != nil {
			return nil, err
		}
		scopeSQL += " AND m.kind = ?"
		args = append(args, normalized)
	}
	args = append(args, tagArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories m
WHERE `+scopeSQL+tagSQL+`
ORDER BY pinned DESC, updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) searchMemoriesFTS(ctx context.Context, projectID, userID string, channelType domain.ChannelType, channelID string, threadID string, scopes []domain.MemoryScope, query string, tags []string, limit int) ([]domain.Memory, error) {
	scopeSQL, args := memoryVisibilitySQL(projectID, userID, channelType, channelID, threadID, scopes)
	tagSQL, tagArgs := memoryTagsSQL(tags)
	args = append([]any{query}, args...)
	args = append(args, tagArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.project_id, m.user_id, m.channel_type, m.channel_id, m.thread_id, m.scope, m.kind, m.content, m.tags, m.source, m.source_id, m.actor, m.confidence, m.pinned, m.metadata, m.created_at, m.updated_at
FROM memory_fts f
JOIN memories m ON m.id = f.memory_id
WHERE memory_fts MATCH ? AND `+scopeSQL+tagSQL+`
ORDER BY rank, m.pinned DESC, m.updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) searchMemoriesVector(ctx context.Context, projectID, userID string, channelType domain.ChannelType, channelID string, threadID string, scopes []domain.MemoryScope, tags []string, request domain.MemorySearchRequest, limit int) ([]domain.Memory, error) {
	if len(request.QueryEmbedding) != memoryVectorDims {
		return nil, fmt.Errorf("memory query embedding dimensions mismatch: got %d, want %d", len(request.QueryEmbedding), memoryVectorDims)
	}
	if strings.TrimSpace(request.EmbeddingProvider) == "" || strings.TrimSpace(request.EmbeddingModel) == "" || request.EmbeddingDimensions <= 0 {
		return nil, fmt.Errorf("memory embedding profile is required")
	}
	vector, err := serializeFloat32(request.QueryEmbedding)
	if err != nil {
		return nil, err
	}
	scopeSQL, args := memoryVisibilitySQL(projectID, userID, channelType, channelID, threadID, scopes)
	tagSQL, tagArgs := memoryTagsSQL(tags)
	k := limit * 3
	if k < limit {
		k = limit
	}
	if k < 10 {
		k = 10
	}
	args = append([]any{vector, k, strings.TrimSpace(request.EmbeddingProvider), strings.TrimSpace(request.EmbeddingModel), request.EmbeddingDimensions}, args...)
	args = append(args, tagArgs...)
	rows, err := s.db.QueryContext(ctx, `
WITH vector_matches AS (
	SELECT rowid, embedding
	FROM memory_embedding_vec(?, ?)
)
SELECT m.id, m.project_id, m.user_id, m.channel_type, m.channel_id, m.thread_id, m.scope, m.kind, m.content, m.tags, m.source, m.source_id, m.actor, m.confidence, m.pinned, m.metadata, m.created_at, m.updated_at
FROM vector_matches v
JOIN memory_embeddings e ON e.id = v.rowid
JOIN memories m ON m.id = e.memory_id
WHERE e.provider = ? AND e.model = ? AND e.dimensions = ? AND `+scopeSQL+tagSQL+`
ORDER BY vec1_cos_distance(?, v.embedding) ASC, m.pinned DESC, m.updated_at DESC
LIMIT ?
`, append(args, vector, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) recentMemories(ctx context.Context, projectID, userID string, channelType domain.ChannelType, channelID string, threadID string, scopes []domain.MemoryScope, tags []string, limit int) ([]domain.Memory, error) {
	scopeSQL, args := memoryVisibilitySQL(projectID, userID, channelType, channelID, threadID, scopes)
	tagSQL, tagArgs := memoryTagsSQL(tags)
	args = append(args, tagArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories m
WHERE `+scopeSQL+tagSQL+`
ORDER BY pinned DESC, updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func memoryVisibilitySQL(projectID, userID string, channelType domain.ChannelType, channelID, threadID string, scopes []domain.MemoryScope) (string, []any) {
	projectID = strings.TrimSpace(projectID)
	userID = strings.TrimSpace(userID)
	channelID = strings.TrimSpace(channelID)
	threadID = strings.TrimSpace(threadID)
	scopes = normalizeMemoryScopes(scopes)
	includeThread := hasMemoryScope(scopes, domain.MemoryScopeThread)
	includeChannel := hasMemoryScope(scopes, domain.MemoryScopeChannel)
	includeProject := hasMemoryScope(scopes, domain.MemoryScopeProject)
	includeUser := hasMemoryScope(scopes, domain.MemoryScopeUser)
	includeGlobal := hasMemoryScope(scopes, domain.MemoryScopeGlobal)
	var clauses []string
	var args []any
	if includeThread && projectID != "" && channelID != "" && threadID != "" {
		clauses = append(clauses, `(m.scope = 'thread' AND m.project_id = ? AND m.thread_id = ? AND m.channel_type = ? AND m.channel_id = ?)`)
		args = append(args, projectID, threadID, string(channelType), channelID)
	}
	if includeChannel && projectID != "" && channelID != "" {
		clauses = append(clauses, `(m.scope = 'channel' AND m.project_id = ? AND m.channel_type = ? AND m.channel_id = ?)`)
		args = append(args, projectID, string(channelType), channelID)
	}
	if includeGlobal {
		clauses = append(clauses, `m.scope = 'global'`)
	}
	if includeProject && projectID != "" {
		clauses = append(clauses, `(m.scope = 'project' AND m.project_id = ?)`)
		args = append(args, projectID)
	}
	if includeUser && userID != "" {
		clauses = append(clauses, `(m.scope = 'user' AND m.user_id = ?)`)
		args = append(args, userID)
	}
	if len(clauses) == 0 {
		return `0 = 1`, nil
	}
	return `(` + strings.Join(clauses, " OR ") + `)`, args
}

func scanMemories(rows *sql.Rows) ([]domain.Memory, error) {
	var memories []domain.Memory
	for rows.Next() {
		var memory domain.Memory
		var scope string
		var channelType string
		var tags string
		var metadata string
		var pinned int
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&memory.ID, &memory.ProjectID, &memory.UserID, &channelType, &memory.ChannelID, &memory.ThreadID, &scope, &memory.Kind, &memory.Content, &tags, &memory.Source, &memory.SourceID, &memory.Actor, &memory.Confidence, &pinned, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		memory.ChannelType = domain.ChannelType(channelType)
		memory.Scope = domain.MemoryScope(scope)
		if err := decodeJSON(tags, &memory.Tags); err != nil {
			return nil, err
		}
		if err := decodeJSON(metadata, &memory.Metadata); err != nil {
			return nil, err
		}
		memory.Pinned = pinned != 0
		memory.CreatedAt = parseTime(createdAt)
		memory.UpdatedAt = parseTime(updatedAt)
		memories = append(memories, memory)
	}
	return memories, rows.Err()
}

func fuseMemoryResults(limit int, rankedLists ...[]domain.Memory) []domain.Memory {
	if limit <= 0 {
		limit = 5
	}
	type candidate struct {
		memory domain.Memory
		score  float64
	}
	candidates := map[string]*candidate{}
	for _, memories := range rankedLists {
		for rank, memory := range memories {
			id := strings.TrimSpace(memory.ID)
			if id == "" {
				continue
			}
			current := candidates[id]
			if current == nil {
				current = &candidate{memory: memory}
				candidates[id] = current
			}
			current.score += 1 / float64(60+rank+1)
		}
	}
	ordered := make([]candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.memory.Pinned {
			candidate.score += 0.001
		}
		ordered = append(ordered, *candidate)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].score != ordered[j].score {
			return ordered[i].score > ordered[j].score
		}
		if ordered[i].memory.Pinned != ordered[j].memory.Pinned {
			return ordered[i].memory.Pinned
		}
		return ordered[i].memory.UpdatedAt.After(ordered[j].memory.UpdatedAt)
	})
	if len(ordered) > limit {
		ordered = ordered[:limit]
	}
	memories := make([]domain.Memory, 0, len(ordered))
	for _, candidate := range ordered {
		memories = append(memories, candidate.memory)
	}
	return memories
}

func (s *Store) UpdateMemory(ctx context.Context, request domain.MemoryUpdateRequest) (domain.MemoryUpdateResult, error) {
	memoryID := strings.TrimSpace(request.MemoryID)
	if memoryID == "" {
		return domain.MemoryUpdateResult{}, fmt.Errorf("memory id is required")
	}
	if request.Confidence != nil && (*request.Confidence < 0 || *request.Confidence > 1) {
		return domain.MemoryUpdateResult{}, fmt.Errorf("memory confidence must be between 0 and 1")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	current, found, err := getVisibleMemory(ctx, tx,
		strings.TrimSpace(request.ProjectID),
		strings.TrimSpace(request.UserID),
		request.ChannelType,
		strings.TrimSpace(request.ChannelID),
		strings.TrimSpace(request.ThreadID),
		memoryID,
	)
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if !found {
		return domain.MemoryUpdateResult{}, nil
	}

	updated := current
	if content := strings.TrimSpace(request.Content); content != "" {
		updated.Content = content
	}
	if kind := strings.TrimSpace(request.Kind); kind != "" {
		normalizedKind, err := normalizeMemoryKind(kind)
		if err != nil {
			return domain.MemoryUpdateResult{}, err
		}
		updated.Kind = normalizedKind
	}
	if request.ReplaceTags {
		updated.Tags = cleanTags(request.Tags)
	}
	if request.Confidence != nil {
		updated.Confidence = *request.Confidence
	}
	if request.Pinned != nil {
		updated.Pinned = *request.Pinned
	}
	normalizedKind, err := normalizeMemoryKind(updated.Kind)
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	updated.Kind = normalizedKind
	updated.Content = strings.TrimSpace(updated.Content)
	if err := validateMemoryPolicy(updated.Content, updated.Kind); err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	duplicateID, err := duplicateMemoryID(ctx, tx, updated)
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if duplicateID != "" {
		return domain.MemoryUpdateResult{}, memoryPolicyError("exact duplicate of existing memory %s", duplicateID)
	}
	updated.UpdatedAt = time.Now().UTC()

	tags, err := encodeJSON(updated.Tags, "[]")
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	metadata, err := encodeJSON(updated.Metadata, "{}")
	if err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
	UPDATE memories
	SET kind = ?, content = ?, tags = json(?), confidence = ?, pinned = ?, metadata = json(?), updated_at = ?
	WHERE id = ?
	`, updated.Kind, updated.Content, tags, updated.Confidence, boolInt(updated.Pinned), metadata, formatTime(updated.UpdatedAt), updated.ID); err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id = ?`, updated.ID); err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO memory_fts(memory_id, project_id, scope, content, tags)
	VALUES (?, ?, ?, ?, ?)
	`, updated.ID, strings.TrimSpace(updated.ProjectID), string(updated.Scope), updated.Content, strings.Join(updated.Tags, " ")); err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MemoryUpdateResult{}, err
	}
	return domain.MemoryUpdateResult{Memory: updated, Updated: true}, nil
}

func getVisibleMemory(ctx context.Context, tx *sql.Tx, projectID, userID string, channelType domain.ChannelType, channelID, threadID, memoryID string) (domain.Memory, bool, error) {
	scopeSQL, args := memoryVisibilitySQL(projectID, userID, channelType, channelID, threadID, nil)
	args = append([]any{strings.TrimSpace(memoryID)}, args...)
	rows, err := tx.QueryContext(ctx, `
	SELECT id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
	FROM memories m
	WHERE id = ? AND `+scopeSQL+`
	LIMIT 1
	`, args...)
	if err != nil {
		return domain.Memory{}, false, err
	}
	defer rows.Close()
	memories, err := scanMemories(rows)
	if err != nil {
		return domain.Memory{}, false, err
	}
	if len(memories) == 0 {
		return domain.Memory{}, false, nil
	}
	return memories[0], true, nil
}

func duplicateMemoryID(ctx context.Context, tx *sql.Tx, memory domain.Memory) (string, error) {
	content := normalizedMemoryContent(memory.Content)
	if content == "" {
		return "", nil
	}
	where := []string{"id <> ?"}
	args := []any{strings.TrimSpace(memory.ID)}
	switch normalizeMemoryScope(memory.Scope) {
	case domain.MemoryScopeThread:
		where = append(where, "scope = 'thread'", "project_id = ?", "thread_id = ?")
		args = append(args, strings.TrimSpace(memory.ProjectID), strings.TrimSpace(memory.ThreadID))
		if strings.TrimSpace(memory.ChannelID) != "" {
			where = append(where, "channel_type = ?", "channel_id = ?")
			args = append(args, string(memory.ChannelType), strings.TrimSpace(memory.ChannelID))
		}
	case domain.MemoryScopeChannel:
		where = append(where, "scope = 'channel'", "project_id = ?", "channel_type = ?", "channel_id = ?")
		args = append(args, strings.TrimSpace(memory.ProjectID), string(memory.ChannelType), strings.TrimSpace(memory.ChannelID))
	case domain.MemoryScopeProject:
		where = append(where, "scope = 'project'", "project_id = ?")
		args = append(args, strings.TrimSpace(memory.ProjectID))
	case domain.MemoryScopeUser:
		where = append(where, "scope = 'user'", "user_id = ?")
		args = append(args, strings.TrimSpace(memory.UserID))
	case domain.MemoryScopeGlobal:
		where = append(where, "scope = 'global'")
	}
	rows, err := tx.QueryContext(ctx, `
	SELECT id, content
	FROM memories
	WHERE `+strings.Join(where, " AND ")+`
	`, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var existingContent string
		if err := rows.Scan(&id, &existingContent); err != nil {
			return "", err
		}
		if normalizedMemoryContent(existingContent) == content {
			return id, nil
		}
	}
	return "", rows.Err()
}

func (s *Store) UpsertMemoryEmbedding(ctx context.Context, embedding domain.MemoryEmbedding) error {
	memoryID := strings.TrimSpace(embedding.MemoryID)
	if memoryID == "" {
		return fmt.Errorf("memory id is required")
	}
	provider := strings.TrimSpace(embedding.Provider)
	model := strings.TrimSpace(embedding.Model)
	contentHash := strings.TrimSpace(embedding.ContentHash)
	if provider == "" || model == "" || contentHash == "" {
		return fmt.Errorf("memory embedding provider, model, and content hash are required")
	}
	if embedding.Dimensions != memoryVectorDims || len(embedding.Vector) != memoryVectorDims {
		return fmt.Errorf("memory embedding dimensions mismatch: got config=%d vector=%d want=%d", embedding.Dimensions, len(embedding.Vector), memoryVectorDims)
	}
	vector, err := serializeFloat32(embedding.Vector)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	now := formatTime(time.Now().UTC())
	var rowID int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM memory_embeddings WHERE memory_id = ?`, memoryID).Scan(&rowID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		result, err := tx.ExecContext(ctx, `
INSERT INTO memory_embeddings(memory_id, provider, model, dimensions, content_hash, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, memoryID, provider, model, embedding.Dimensions, contentHash, now, now)
		if err != nil {
			return err
		}
		rowID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, `
UPDATE memory_embeddings
SET provider = ?, model = ?, dimensions = ?, content_hash = ?, updated_at = ?
WHERE id = ?
`, provider, model, embedding.Dimensions, contentHash, now, rowID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_embedding_vec WHERE rowid = ?`, rowID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_embedding_vec(rowid, embedding) VALUES (?, ?)`, rowID, vector); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteMemoryEmbeddings(ctx context.Context, memoryIDs []string) error {
	memoryIDs = cleanMemoryIDs(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := deleteMemoryEmbeddingsTx(ctx, tx, memoryIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ForgetMemory(ctx context.Context, projectID, memoryID string) (bool, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return false, fmt.Errorf("memory id is required")
	}
	result, err := s.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: projectID,
		MemoryIDs: []string{memoryID},
	})
	if err != nil {
		return false, err
	}
	return len(result.DeletedMemoryIDs) > 0, nil
}

func (s *Store) ForgetMemories(ctx context.Context, request domain.MemoryForgetRequest) (domain.MemoryForgetResult, error) {
	memoryIDs := cleanMemoryIDs(request.MemoryIDs)
	tags := cleanTags(request.Tags)
	if len(memoryIDs) == 0 && len(tags) == 0 && len(request.Scopes) == 0 {
		return domain.MemoryForgetResult{}, fmt.Errorf("memory ids, tags, or memory scope is required")
	}
	scopes := normalizeMemoryScopes(request.Scopes)

	scopeSQL, args := memoryVisibilitySQL(
		strings.TrimSpace(request.ProjectID),
		strings.TrimSpace(request.UserID),
		request.ChannelType,
		strings.TrimSpace(request.ChannelID),
		strings.TrimSpace(request.ThreadID),
		scopes,
	)
	where := []string{scopeSQL}
	if len(memoryIDs) > 0 {
		where = append(where, "m.id IN ("+sqlPlaceholders(len(memoryIDs))+")")
		for _, memoryID := range memoryIDs {
			args = append(args, memoryID)
		}
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, user_id, channel_type, channel_id, thread_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories m
WHERE `+strings.Join(where, " AND ")+`
ORDER BY updated_at DESC
`, args...)
	if err != nil {
		return domain.MemoryForgetResult{}, err
	}
	defer rows.Close()
	candidates, err := scanMemories(rows)
	if err != nil {
		return domain.MemoryForgetResult{}, err
	}

	var deleteIDs []string
	for _, memory := range candidates {
		if memoryHasAllTags(memory, tags) {
			deleteIDs = append(deleteIDs, memory.ID)
		}
	}
	deleteIDs = cleanMemoryIDs(deleteIDs)
	if len(deleteIDs) == 0 {
		return domain.MemoryForgetResult{}, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.MemoryForgetResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := deleteMemoryEmbeddingsTx(ctx, tx, deleteIDs); err != nil {
		return domain.MemoryForgetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM memories
WHERE id IN (`+sqlPlaceholders(len(deleteIDs))+`)
`, stringAnySlice(deleteIDs)...); err != nil {
		return domain.MemoryForgetResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_fts
WHERE memory_id IN (`+sqlPlaceholders(len(deleteIDs))+`)
`, stringAnySlice(deleteIDs)...); err != nil {
		return domain.MemoryForgetResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.MemoryForgetResult{}, err
	}
	return domain.MemoryForgetResult{DeletedMemoryIDs: deleteIDs}, nil
}

func deleteMemoryEmbeddingsTx(ctx context.Context, tx *sql.Tx, memoryIDs []string) error {
	memoryIDs = cleanMemoryIDs(memoryIDs)
	if len(memoryIDs) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
SELECT id FROM memory_embeddings
WHERE memory_id IN (`+sqlPlaceholders(len(memoryIDs))+`)
`, stringAnySlice(memoryIDs)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	var rowIDs []string
	for rows.Next() {
		var rowID string
		if err := rows.Scan(&rowID); err != nil {
			return err
		}
		rowIDs = append(rowIDs, rowID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(rowIDs) == 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_embedding_vec
WHERE rowid IN (`+sqlPlaceholders(len(rowIDs))+`)
`, stringAnySlice(rowIDs)...); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM memory_embeddings
WHERE id IN (`+sqlPlaceholders(len(rowIDs))+`)
`, stringAnySlice(rowIDs)...); err != nil {
		return err
	}
	return nil
}

func encodeJSON(value any, empty string) (string, error) {
	if value == nil {
		return empty, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || string(data) == "null" {
		return empty, nil
	}
	return string(data), nil
}

func jsonText(raw json.RawMessage, empty string) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return empty
	}
	if !json.Valid(raw) {
		data, err := json.Marshal(string(raw))
		if err != nil {
			return empty
		}
		return string(data)
	}
	return string(raw)
}

func decodeJSON(value string, target any) error {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "{}"
	}
	return json.Unmarshal([]byte(value), target)
}

func normalizeJSONText(value string) string {
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return strings.TrimSpace(value)
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return strings.TrimSpace(value)
	}
	return string(encoded)
}

func stableStoreID(parts ...string) string {
	hash := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:])
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		value = time.Now().UTC()
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableTime(value *time.Time) any {
	if value == nil || value.IsZero() {
		return nil
	}
	return formatTime(*value)
}

func parseTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func normalizeMemoryScope(scope domain.MemoryScope) domain.MemoryScope {
	switch scope {
	case domain.MemoryScopeThread:
		return domain.MemoryScopeThread
	case domain.MemoryScopeChannel:
		return domain.MemoryScopeChannel
	case domain.MemoryScopeGlobal:
		return domain.MemoryScopeGlobal
	case domain.MemoryScopeUser:
		return domain.MemoryScopeUser
	default:
		return domain.MemoryScopeProject
	}
}

func normalizeMemoryScopes(scopes []domain.MemoryScope) []domain.MemoryScope {
	if len(scopes) == 0 {
		return []domain.MemoryScope{domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
	}
	seen := map[domain.MemoryScope]bool{}
	var normalized []domain.MemoryScope
	for _, scope := range scopes {
		scope = normalizeMemoryScope(scope)
		if !seen[scope] {
			seen[scope] = true
			normalized = append(normalized, scope)
		}
	}
	return normalized
}

func hasMemoryScope(scopes []domain.MemoryScope, target domain.MemoryScope) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func ftsQuery(query string) string {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-')
	})
	seen := map[string]bool{}
	var cleaned []string
	for _, term := range terms {
		term = strings.Trim(term, "-_")
		if len(term) < 2 || seen[term] {
			continue
		}
		seen[term] = true
		cleaned = append(cleaned, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
		if len(cleaned) == 8 {
			break
		}
	}
	return strings.Join(cleaned, " OR ")
}

func cleanTags(tags []string) []string {
	cleaned := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		cleaned = append(cleaned, tag)
	}
	sort.Strings(cleaned)
	return cleaned
}

func memoryTagsSQL(tags []string) (string, []any) {
	if len(tags) == 0 {
		return "", nil
	}
	clauses := make([]string, 0, len(tags))
	args := make([]any, 0, len(tags))
	for _, tag := range tags {
		clauses = append(clauses, `EXISTS (SELECT 1 FROM json_each(m.tags) AS tag WHERE tag.value = ?)`)
		args = append(args, tag)
	}
	return " AND " + strings.Join(clauses, " AND "), args
}

func serializeFloat32(vector []float32) ([]byte, error) {
	var buffer bytes.Buffer
	if err := binary.Write(&buffer, binary.LittleEndian, vector); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func cleanMemoryIDs(memoryIDs []string) []string {
	cleaned := make([]string, 0, len(memoryIDs))
	seen := map[string]bool{}
	for _, memoryID := range memoryIDs {
		memoryID = strings.TrimSpace(memoryID)
		if memoryID == "" || seen[memoryID] {
			continue
		}
		seen[memoryID] = true
		cleaned = append(cleaned, memoryID)
	}
	return cleaned
}

func memoryHasAllTags(memory domain.Memory, tags []string) bool {
	if len(tags) == 0 {
		return true
	}
	memoryTags := map[string]bool{}
	for _, tag := range memory.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag != "" {
			memoryTags[tag] = true
		}
	}
	for _, tag := range tags {
		if !memoryTags[tag] {
			return false
		}
	}
	return true
}

func sqlPlaceholders(count int) string {
	placeholders := make([]string, 0, count)
	for range count {
		placeholders = append(placeholders, "?")
	}
	return strings.Join(placeholders, ", ")
}

func stringAnySlice(values []string) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
