package sqlite

import (
	"context"
	"database/sql"
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

	_ "github.com/ncruces/go-sqlite3/driver"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

const (
	driverName           = "sqlite3"
	currentSchemaVersion = 1
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

	db, err := sql.Open(driverName, dataSourceName(path))
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
	if !applied[currentSchemaVersion] {
		if _, err := tx.ExecContext(ctx, migrationV1); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, currentSchemaVersion, formatTime(time.Now().UTC())); err != nil {
			return err
		}
	}
	return tx.Commit()
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
	scope TEXT NOT NULL CHECK (scope IN ('project', 'global')),
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
CREATE INDEX IF NOT EXISTS idx_memories_scope_updated ON memories(scope, updated_at);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
	memory_id UNINDEXED,
	project_id UNINDEXED,
	scope UNINDEXED,
	content,
	tags
);
`

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
SELECT project_id, kind, channel_id, channel_type, actor_id, actor_name, body, metadata, payload, provenance, created_at
FROM events
WHERE id = ?
`, event.ID).Scan(
		&existing.ProjectID,
		&existing.Kind,
		&existing.ChannelID,
		&existing.ChannelType,
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
INSERT INTO events(id, project_id, kind, channel_id, channel_type, actor_id, actor_name, body, metadata, payload, provenance, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, json(?), json(?), json(?), ?)
`, event.ID, record.ProjectID, record.Kind, record.ChannelID, record.ChannelType, record.ActorID, record.ActorName, record.Body, record.Metadata, record.Payload, record.Provenance, record.CreatedAt)
		return storage.EventAppendResult{Inserted: true}, err
	}
	if err != nil {
		return storage.EventAppendResult{}, err
	}

	changed := !existing.equal(record)
	if changed {
		_, err = s.db.ExecContext(ctx, `
UPDATE events
SET project_id = ?, kind = ?, channel_id = ?, channel_type = ?, actor_id = ?, actor_name = ?, body = ?, metadata = json(?), payload = json(?), provenance = json(?), created_at = ?
WHERE id = ?
`, record.ProjectID, record.Kind, record.ChannelID, record.ChannelType, record.ActorID, record.ActorName, record.Body, record.Metadata, record.Payload, record.Provenance, record.CreatedAt, event.ID)
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

func (s *Store) UpsertConversationMessage(ctx context.Context, message domain.ConversationMessage) error {
	if strings.TrimSpace(message.ID) == "" {
		return fmt.Errorf("conversation message id is required")
	}
	if strings.TrimSpace(message.ProjectID) == "" {
		return fmt.Errorf("conversation message project id is required")
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	metadata, err := encodeJSON(message.Metadata, "{}")
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO conversation_messages(id, project_id, event_id, role, body, tool_call_id, metadata, created_at)
VALUES (?, ?, ?, ?, ?, ?, json(?), ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
	event_id = excluded.event_id,
	role = excluded.role,
	body = excluded.body,
	tool_call_id = excluded.tool_call_id,
	metadata = excluded.metadata,
	created_at = excluded.created_at
`, message.ID, message.ProjectID, strings.TrimSpace(message.EventID), string(message.Role), message.Body, strings.TrimSpace(message.ToolCallID), metadata, formatTime(message.CreatedAt))
	return err
}

func (s *Store) RememberMemory(ctx context.Context, memory domain.Memory) (domain.Memory, error) {
	memory.ID = strings.TrimSpace(memory.ID)
	if memory.ID == "" {
		return domain.Memory{}, fmt.Errorf("memory id is required")
	}
	memory.Scope = normalizeMemoryScope(memory.Scope)
	if memory.Scope == domain.MemoryScopeProject && strings.TrimSpace(memory.ProjectID) == "" {
		return domain.Memory{}, fmt.Errorf("project memory project id is required")
	}
	memory.Content = strings.TrimSpace(memory.Content)
	if memory.Content == "" {
		return domain.Memory{}, fmt.Errorf("memory content is required")
	}
	memory.Kind = firstNonEmpty(memory.Kind, "fact")
	if memory.Confidence <= 0 {
		memory.Confidence = 1
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
	if _, err := tx.ExecContext(ctx, `
INSERT INTO memories(id, project_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, json(?), ?, ?, ?, ?, ?, json(?), ?, ?)
ON CONFLICT(id) DO UPDATE SET
	project_id = excluded.project_id,
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
`, memory.ID, strings.TrimSpace(memory.ProjectID), string(memory.Scope), memory.Kind, memory.Content, tags, strings.TrimSpace(memory.Source), strings.TrimSpace(memory.SourceID), strings.TrimSpace(memory.Actor), memory.Confidence, boolInt(memory.Pinned), metadata, formatTime(memory.CreatedAt), formatTime(memory.UpdatedAt)); err != nil {
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
	scopes := normalizeMemoryScopes(request.Scopes)
	query := ftsQuery(request.Query)
	var memories []domain.Memory
	var err error
	if query != "" {
		memories, err = s.searchMemoriesFTS(ctx, strings.TrimSpace(request.ProjectID), scopes, query, limit)
		if err != nil {
			return nil, err
		}
	}
	if len(memories) > 0 {
		return memories, nil
	}
	if query != "" && !request.FallbackRecent {
		return nil, nil
	}
	return s.recentMemories(ctx, strings.TrimSpace(request.ProjectID), scopes, limit)
}

func (s *Store) searchMemoriesFTS(ctx context.Context, projectID string, scopes []domain.MemoryScope, query string, limit int) ([]domain.Memory, error) {
	scopeSQL, args := memoryVisibilitySQL(projectID, scopes)
	args = append([]any{query}, args...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.project_id, m.scope, m.kind, m.content, m.tags, m.source, m.source_id, m.actor, m.confidence, m.pinned, m.metadata, m.created_at, m.updated_at
FROM memory_fts f
JOIN memories m ON m.id = f.memory_id
WHERE memory_fts MATCH ? AND `+scopeSQL+`
ORDER BY rank, m.pinned DESC, m.updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func (s *Store) recentMemories(ctx context.Context, projectID string, scopes []domain.MemoryScope, limit int) ([]domain.Memory, error) {
	scopeSQL, args := memoryVisibilitySQL(projectID, scopes)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, project_id, scope, kind, content, tags, source, source_id, actor, confidence, pinned, metadata, created_at, updated_at
FROM memories m
WHERE `+scopeSQL+`
ORDER BY pinned DESC, updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemories(rows)
}

func memoryVisibilitySQL(projectID string, scopes []domain.MemoryScope) (string, []any) {
	projectID = strings.TrimSpace(projectID)
	includeProject := hasMemoryScope(scopes, domain.MemoryScopeProject)
	includeGlobal := hasMemoryScope(scopes, domain.MemoryScopeGlobal)
	switch {
	case includeProject && includeGlobal && projectID != "":
		return `(m.scope = 'global' OR (m.scope = 'project' AND m.project_id = ?))`, []any{projectID}
	case includeProject && projectID != "":
		return `(m.scope = 'project' AND m.project_id = ?)`, []any{projectID}
	case includeGlobal:
		return `m.scope = 'global'`, nil
	default:
		return `0 = 1`, nil
	}
}

func scanMemories(rows *sql.Rows) ([]domain.Memory, error) {
	var memories []domain.Memory
	for rows.Next() {
		var memory domain.Memory
		var scope string
		var tags string
		var metadata string
		var pinned int
		var createdAt string
		var updatedAt string
		if err := rows.Scan(&memory.ID, &memory.ProjectID, &scope, &memory.Kind, &memory.Content, &tags, &memory.Source, &memory.SourceID, &memory.Actor, &memory.Confidence, &pinned, &metadata, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
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

func (s *Store) ForgetMemory(ctx context.Context, projectID, memoryID string) (bool, error) {
	memoryID = strings.TrimSpace(memoryID)
	if memoryID == "" {
		return false, fmt.Errorf("memory id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	result, err := tx.ExecContext(ctx, `
DELETE FROM memories
WHERE id = ? AND (scope = 'global' OR project_id = ?)
`, memoryID, strings.TrimSpace(projectID))
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE memory_id = ?`, memoryID); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, nil
	}
	return affected > 0, nil
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
	case domain.MemoryScopeGlobal:
		return domain.MemoryScopeGlobal
	default:
		return domain.MemoryScopeProject
	}
}

func normalizeMemoryScopes(scopes []domain.MemoryScope) []domain.MemoryScope {
	if len(scopes) == 0 {
		return []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeGlobal}
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
