package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

func TestStoreMigratesAndVerifiesSchema(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	if err := store.VerifySchema(context.Background()); err != nil {
		t.Fatalf("verify schema: %v", err)
	}
	if err := store.EnsureProject(context.Background(), domain.Project{ID: "default", Name: "OpenCTO"}); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	var name string
	if err := store.db.QueryRowContext(context.Background(), `SELECT name FROM projects WHERE id = ?`, "default").Scan(&name); err != nil {
		t.Fatalf("query project: %v", err)
	}
	if name != "OpenCTO" {
		t.Fatalf("unexpected project name: %q", name)
	}
}

func TestMigrateAddsUserMemoryScopeToExistingSchema(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "opencto.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if _, err := store.db.ExecContext(ctx, `
CREATE TABLE schema_migrations (
	version INTEGER PRIMARY KEY,
	applied_at TEXT NOT NULL
);
INSERT INTO schema_migrations(version, applied_at) VALUES (1, '2026-05-01T00:00:00Z'), (2, '2026-05-01T00:00:00Z'), (3, '2026-05-01T00:00:00Z');
CREATE TABLE memories (
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
CREATE INDEX idx_memories_project_scope_updated ON memories(project_id, scope, updated_at);
CREATE INDEX idx_memories_scope_updated ON memories(scope, updated_at);
CREATE VIRTUAL TABLE memory_fts USING fts5(
	memory_id UNINDEXED,
	project_id UNINDEXED,
	scope UNINDEXED,
	content,
	tags
);
CREATE TABLE memory_embeddings (
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
INSERT INTO memories(id, project_id, scope, kind, content, created_at, updated_at)
VALUES ('existing-memory', 'default', 'project', 'fact', 'Project prefers durable local SQLite storage.', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z');
INSERT INTO memory_embeddings(memory_id, provider, model, dimensions, content_hash, created_at, updated_at)
VALUES ('existing-memory', 'openai', 'text-embedding-3-small', 1536, 'hash', '2026-05-01T00:00:00Z', '2026-05-01T00:00:00Z');
`); err != nil {
		t.Fatalf("seed old schema: %v", err)
	}

	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate old schema: %v", err)
	}
	if ok, err := tableHasColumn(ctx, store.db, "memories", "user_id"); err != nil {
		t.Fatalf("check user_id column: %v", err)
	} else if !ok {
		t.Fatalf("expected user_id column after migration")
	}
	if ok, err := tableHasColumn(ctx, store.db, "memories", "thread_id"); err != nil {
		t.Fatalf("check thread_id column: %v", err)
	} else if !ok {
		t.Fatalf("expected thread_id column after migration")
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM conversation_threads LIMIT 1`)
	if err != nil {
		t.Fatalf("expected conversation_threads table after migration: %v", err)
	}
	_ = rows.Close()
	if _, err := store.RememberMemory(ctx, domain.Memory{
		ID:        "user-memory",
		UserID:    "discord:user-1",
		Scope:     domain.MemoryScopeUser,
		Kind:      "preference",
		Content:   "User prefers concise technical explanations.",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("remember user memory after migration: %v", err)
	}
	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		UserID:         "discord:user-1",
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search memories after migration: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "existing-memory", "user-memory")

	if _, err := store.RememberMemory(ctx, domain.Memory{
		ID:          "thread-memory",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Scope:       domain.MemoryScopeThread,
		Kind:        "decision",
		Content:     "Use compact replies in this thread.",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}); err != nil {
		t.Fatalf("remember thread memory after migration: %v", err)
	}
	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-1",
		ThreadID:       "thread-1",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeThread},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search thread memories after migration: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "thread-memory")
}

func TestAppendEventIsIdempotentAndUpdatesChangedDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	event := domain.Event{
		ID:        "event-1",
		ProjectID: "default",
		Kind:      domain.EventKindMessage,
		Body:      "first",
		Metadata:  domain.Metadata{"source": "test"},
		Payload:   map[string]any{"ok": true},
		CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
	}
	result, err := store.AppendEvent(ctx, event)
	if err != nil {
		t.Fatalf("append event: %v", err)
	}
	if !result.Inserted || result.Updated || result.Changed {
		t.Fatalf("unexpected first append result: %#v", result)
	}
	result, err = store.AppendEvent(ctx, event)
	if err != nil {
		t.Fatalf("append duplicate event: %v", err)
	}
	if !result.Updated || result.Changed {
		t.Fatalf("unexpected duplicate result: %#v", result)
	}
	event.Body = "changed"
	result, err = store.AppendEvent(ctx, event)
	if err != nil {
		t.Fatalf("append changed duplicate event: %v", err)
	}
	if !result.Updated || !result.Changed {
		t.Fatalf("unexpected changed duplicate result: %#v", result)
	}
	var body string
	if err := store.db.QueryRowContext(ctx, `SELECT body FROM events WHERE id = ?`, event.ID).Scan(&body); err != nil {
		t.Fatalf("query event: %v", err)
	}
	if body != "changed" {
		t.Fatalf("expected event body to update, got %q", body)
	}
}

func TestWorkItemsAndExecutionAuditRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	items := []domain.WorkItem{
		{ID: "wi-1", ProjectID: "default", Title: "pending", Status: domain.WorkItemStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "wi-2", ProjectID: "default", Title: "done", Status: domain.WorkItemStatusCompleted, CreatedAt: now, UpdatedAt: now},
	}
	if err := store.UpsertWorkItems(ctx, items); err != nil {
		t.Fatalf("upsert work items: %v", err)
	}
	pending, err := store.ListPendingWorkItems(ctx, "default")
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != "wi-1" {
		t.Fatalf("unexpected pending work items: %#v", pending)
	}

	completedAt := now.Add(time.Second)
	attempt := domain.ExecutionAttempt{
		ID:          "attempt-1",
		ProjectID:   "default",
		WorkItemID:  "wi-1",
		Status:      domain.ExecutionStatusSucceeded,
		Attempt:     1,
		Tool:        domain.ToolTypeExec,
		Summary:     "pwd",
		StartedAt:   now,
		CompletedAt: &completedAt,
	}
	if err := store.UpsertExecutionAttempt(ctx, attempt); err != nil {
		t.Fatalf("upsert attempt: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"stdout": "/tmp"})
	invocation := domain.ToolInvocation{
		ID:                 "invocation-1",
		ProjectID:          "default",
		ExecutionAttemptID: "attempt-1",
		RequestedIntent:    "pwd",
		ChosenTool:         domain.ToolTypeExec,
		InputPayload:       json.RawMessage(`{"command":"pwd"}`),
		OutputPayload:      payload,
		ResultCode:         "0",
		CreatedAt:          now,
		CompletedAt:        &completedAt,
	}
	if err := store.UpsertToolInvocation(ctx, invocation); err != nil {
		t.Fatalf("upsert invocation: %v", err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tool_invocations WHERE id = ? AND json_extract(input_payload, '$.command') = 'pwd'`, invocation.ID).Scan(&count); err != nil {
		t.Fatalf("query invocation: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected one persisted invocation, got %d", count)
	}
}

func TestConversationMessagesUseScopedHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	messages := []domain.ConversationMessage{
		{
			ID:        "project-1",
			ProjectID: "default",
			EventID:   "project-event",
			Role:      domain.ConversationRoleUser,
			Body:      "unscoped project note",
			CreatedAt: base,
		},
		{
			ID:          "channel-1",
			ProjectID:   "default",
			EventID:     "channel-event",
			Role:        domain.ConversationRoleAssistant,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "same channel",
			CreatedAt:   base.Add(time.Second),
		},
		{
			ID:          "thread-1",
			ProjectID:   "default",
			EventID:     "thread-event",
			Role:        domain.ConversationRoleTool,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			ThreadID:    "thread-a",
			Body:        "same thread tool output",
			CreatedAt:   base.Add(2 * time.Second),
		},
		{
			ID:          "other-channel",
			ProjectID:   "default",
			EventID:     "other-event",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-b",
			Body:        "other channel",
			CreatedAt:   base.Add(3 * time.Second),
		},
		{
			ID:          "control",
			ProjectID:   "default",
			EventID:     "control-event",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "/stop",
			Metadata:    domain.Metadata{domain.MetadataKeyControl: "cancel"},
			CreatedAt:   base.Add(3500 * time.Millisecond),
		},
		{
			ID:          "current",
			ProjectID:   "default",
			EventID:     "current-event",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "current event",
			CreatedAt:   base.Add(4 * time.Second),
		},
	}
	for _, message := range messages {
		if err := store.UpsertConversationMessage(ctx, message); err != nil {
			t.Fatalf("upsert conversation message %s: %v", message.ID, err)
		}
	}

	thread, err := store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-a",
		ThreadID:       "thread-a",
		Scope:          storage.ConversationScopeThread,
		Limit:          10,
		ExcludeEventID: "current-event",
	})
	if err != nil {
		t.Fatalf("list thread conversation: %v", err)
	}
	if len(thread) != 1 || thread[0].ID != "thread-1" || thread[0].Role != domain.ConversationRoleTool {
		t.Fatalf("unexpected thread history: %#v", thread)
	}

	channel, err := store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-a",
		Scope:          storage.ConversationScopeChannel,
		Limit:          10,
		ExcludeEventID: "current-event",
		ExcludeControl: true,
	})
	if err != nil {
		t.Fatalf("list channel conversation: %v", err)
	}
	if len(channel) != 1 || channel[0].ID != "channel-1" {
		t.Fatalf("unexpected channel history: %#v", channel)
	}

	project, err := store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID: "default",
		Scope:     storage.ConversationScopeProject,
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list project conversation: %v", err)
	}
	if len(project) != 1 || project[0].ID != "project-1" {
		t.Fatalf("unexpected project history: %#v", project)
	}

	oldest, err := store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-a",
		Scope:          storage.ConversationScopeChannel,
		Limit:          10,
		AfterCreatedAt: base.Add(time.Second),
		AfterID:        "channel-1",
		OldestFirst:    true,
		ExcludeControl: true,
	})
	if err != nil {
		t.Fatalf("list oldest conversation: %v", err)
	}
	if len(oldest) != 1 || oldest[0].ID != "current" {
		t.Fatalf("unexpected oldest-first history: %#v", oldest)
	}
}

func TestConversationMessagesRespectBeforeCreatedAtAndID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rootTime := base.Add(time.Minute)
	messages := []domain.ConversationMessage{
		{
			ID:          "covered",
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "covered by summary",
			CreatedAt:   base,
		},
		{
			ID:          "a-gap",
			ProjectID:   "default",
			Role:        domain.ConversationRoleAssistant,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "after summary before root",
			CreatedAt:   rootTime.Add(-time.Second),
		},
		{
			ID:          "m-root",
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "root message",
			CreatedAt:   rootTime,
		},
		{
			ID:          "z-after-same-time",
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "same timestamp after root",
			CreatedAt:   rootTime,
		},
		{
			ID:          "after-later",
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        "after root",
			CreatedAt:   rootTime.Add(time.Second),
		},
	}
	for _, message := range messages {
		if err := store.UpsertConversationMessage(ctx, message); err != nil {
			t.Fatalf("upsert conversation message %s: %v", message.ID, err)
		}
	}

	found, err := store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:       "default",
		ChannelType:     domain.ChannelTypeDiscord,
		ChannelID:       "channel-a",
		Scope:           storage.ConversationScopeChannel,
		Limit:           10,
		AfterCreatedAt:  base,
		AfterID:         "covered",
		BeforeCreatedAt: rootTime,
		BeforeID:        "m-root",
		OldestFirst:     true,
	})
	if err != nil {
		t.Fatalf("list bounded conversation messages: %v", err)
	}
	got := make([]string, 0, len(found))
	for _, message := range found {
		got = append(got, message.ID)
	}
	if strings.Join(got, ",") != "a-gap,m-root" {
		t.Fatalf("unexpected bounded conversation messages: %#v", found)
	}
}

func TestConversationRootMessageUsesEventSourceID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        "original parent request",
		Provenance:  domain.Provenance{SourceID: "discord-message-1"},
		CreatedAt:   base,
	}
	if _, err := store.AppendEvent(ctx, event); err != nil {
		t.Fatalf("append event: %v", err)
	}
	message := domain.ConversationMessage{
		ID:          "conversation-1",
		ProjectID:   "default",
		EventID:     event.ID,
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        event.Body,
		CreatedAt:   base,
	}
	if err := store.UpsertConversationMessage(ctx, message); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	found, ok, err := store.GetConversationRootMessage(ctx, storage.ConversationRootMessageQuery{
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		MessageID:   "discord-message-1",
	})
	if err != nil {
		t.Fatalf("get root message: %v", err)
	}
	if !ok || found.ID != "conversation-1" {
		t.Fatalf("expected parent conversation message, got ok=%v message=%#v", ok, found)
	}
}

func TestConversationRootMessageUsesEventPayloadMessageID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        "original parent request",
		Payload:     map[string]any{"message_id": "payload-message-1"},
		CreatedAt:   base,
	}
	if _, err := store.AppendEvent(ctx, event); err != nil {
		t.Fatalf("append event: %v", err)
	}
	message := domain.ConversationMessage{
		ID:          "conversation-1",
		ProjectID:   "default",
		EventID:     event.ID,
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        event.Body,
		CreatedAt:   base,
	}
	if err := store.UpsertConversationMessage(ctx, message); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	found, ok, err := store.GetConversationRootMessage(ctx, storage.ConversationRootMessageQuery{
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		MessageID:   "payload-message-1",
	})
	if err != nil {
		t.Fatalf("get root message: %v", err)
	}
	if !ok || found.ID != "conversation-1" {
		t.Fatalf("expected parent conversation message from payload id, got ok=%v message=%#v", ok, found)
	}
}

func TestConversationRootMessageUsesConversationMetadataMessageID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	message := domain.ConversationMessage{
		ID:          "conversation-1",
		ProjectID:   "default",
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        "original parent request",
		Metadata:    domain.Metadata{"message_id": "metadata-message-1"},
		CreatedAt:   base,
	}
	if err := store.UpsertConversationMessage(ctx, message); err != nil {
		t.Fatalf("upsert conversation: %v", err)
	}

	found, ok, err := store.GetConversationRootMessage(ctx, storage.ConversationRootMessageQuery{
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		MessageID:   "metadata-message-1",
	})
	if err != nil {
		t.Fatalf("get root message: %v", err)
	}
	if !ok || found.ID != "conversation-1" {
		t.Fatalf("expected parent conversation message from metadata id, got ok=%v message=%#v", ok, found)
	}
}

func TestConversationSummariesUseScopedHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	summaries := []domain.ConversationSummary{
		{
			ID:            "project-summary",
			ProjectID:     "default",
			Scope:         domain.ConversationSummaryScopeProject,
			Summary:       "Project-level context.",
			FromMessageID: "p1",
			ToMessageID:   "p2",
			FromCreatedAt: base,
			ToCreatedAt:   base.Add(time.Minute),
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
		{
			ID:            "thread-summary",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			ThreadID:      "thread-a",
			Scope:         domain.ConversationSummaryScopeThread,
			Summary:       "Thread-level context.",
			FromMessageID: "t1",
			ToMessageID:   "t2",
			FromCreatedAt: base.Add(time.Hour),
			ToCreatedAt:   base.Add(time.Hour + time.Minute),
			MessageCount:  2,
			SourceChars:   300,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
	}
	for _, summary := range summaries {
		if err := store.UpsertConversationSummary(ctx, summary); err != nil {
			t.Fatalf("upsert summary %s: %v", summary.ID, err)
		}
	}

	thread, err := store.ListConversationSummaries(ctx, storage.ConversationSummaryQuery{
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		ThreadID:    "thread-a",
		Scope:       domain.ConversationSummaryScopeThread,
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("list thread summaries: %v", err)
	}
	if len(thread) != 1 || thread[0].ID != "thread-summary" {
		t.Fatalf("unexpected thread summaries: %#v", thread)
	}

	project, err := store.ListConversationSummaries(ctx, storage.ConversationSummaryQuery{
		ProjectID: "default",
		Scope:     domain.ConversationSummaryScopeProject,
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("list project summaries: %v", err)
	}
	if len(project) != 1 || project[0].ID != "project-summary" {
		t.Fatalf("unexpected project summaries: %#v", project)
	}
}

func TestConversationSummariesRespectBeforeCreatedAtAndID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	rootTime := base.Add(time.Minute)
	summaries := []domain.ConversationSummary{
		{
			ID:            "summary-covered",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			Scope:         domain.ConversationSummaryScopeChannel,
			Summary:       "Covered channel context.",
			FromMessageID: "covered-start",
			ToMessageID:   "covered",
			FromCreatedAt: base.Add(-time.Minute),
			ToCreatedAt:   base,
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
		{
			ID:            "summary-gap",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			Scope:         domain.ConversationSummaryScopeChannel,
			Summary:       "Gap channel context.",
			FromMessageID: "a-gap-start",
			ToMessageID:   "a-gap",
			FromCreatedAt: base,
			ToCreatedAt:   rootTime.Add(-time.Second),
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
		{
			ID:            "summary-root",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			Scope:         domain.ConversationSummaryScopeChannel,
			Summary:       "Root-bounded channel context.",
			FromMessageID: "m-root-start",
			ToMessageID:   "m-root",
			FromCreatedAt: base,
			ToCreatedAt:   rootTime,
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
		{
			ID:            "summary-after-same-time",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			Scope:         domain.ConversationSummaryScopeChannel,
			Summary:       "Same-time after-root channel context.",
			FromMessageID: "z-after-start",
			ToMessageID:   "z-after-same-time",
			FromCreatedAt: base,
			ToCreatedAt:   rootTime,
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
		{
			ID:            "summary-after-later",
			ProjectID:     "default",
			ChannelType:   domain.ChannelTypeDiscord,
			ChannelID:     "channel-a",
			Scope:         domain.ConversationSummaryScopeChannel,
			Summary:       "Later after-root channel context.",
			FromMessageID: "after-later-start",
			ToMessageID:   "after-later",
			FromCreatedAt: base,
			ToCreatedAt:   rootTime.Add(time.Second),
			MessageCount:  2,
			SourceChars:   200,
			CreatedAt:     base,
			UpdatedAt:     base,
		},
	}
	for _, summary := range summaries {
		if err := store.UpsertConversationSummary(ctx, summary); err != nil {
			t.Fatalf("upsert summary %s: %v", summary.ID, err)
		}
	}

	found, err := store.ListConversationSummaries(ctx, storage.ConversationSummaryQuery{
		ProjectID:       "default",
		ChannelType:     domain.ChannelTypeDiscord,
		ChannelID:       "channel-a",
		Scope:           domain.ConversationSummaryScopeChannel,
		Limit:           10,
		BeforeCreatedAt: rootTime,
		BeforeID:        "m-root",
	})
	if err != nil {
		t.Fatalf("list bounded conversation summaries: %v", err)
	}
	got := make([]string, 0, len(found))
	for _, summary := range found {
		got = append(got, summary.ID)
	}
	if strings.Join(got, ",") != "summary-covered,summary-gap,summary-root" {
		t.Fatalf("unexpected bounded conversation summaries: %#v", found)
	}
}

func TestUpsertConversationThread(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	thread := domain.ConversationThread{
		ID:            "thread-row-1",
		ProjectID:     "default",
		ChannelType:   domain.ChannelTypeDiscord,
		ChannelID:     "channel-a",
		ThreadID:      "thread-a",
		RootMessageID: "root-message-1",
		EventID:       "event-1",
		Title:         "Initial thread prompt",
		Metadata:      domain.Metadata{"source": "test"},
		CreatedAt:     base,
		UpdatedAt:     base,
		LastMessageAt: base,
	}
	if err := store.UpsertConversationThread(ctx, thread); err != nil {
		t.Fatalf("upsert thread: %v", err)
	}
	thread.EventID = "event-2"
	thread.Title = ""
	thread.LastMessageAt = base.Add(time.Hour)
	if err := store.UpsertConversationThread(ctx, thread); err != nil {
		t.Fatalf("upsert thread update: %v", err)
	}
	var eventID, title, lastMessageAt string
	if err := store.db.QueryRowContext(ctx, `
SELECT event_id, title, last_message_at
FROM conversation_threads
WHERE project_id = ? AND channel_type = ? AND thread_id = ?
`, "default", string(domain.ChannelTypeDiscord), "thread-a").Scan(&eventID, &title, &lastMessageAt); err != nil {
		t.Fatalf("query thread: %v", err)
	}
	if eventID != "event-2" || title != "Initial thread prompt" || parseTime(lastMessageAt) != base.Add(time.Hour) {
		t.Fatalf("unexpected stored thread event=%q title=%q last=%q", eventID, title, lastMessageAt)
	}
}

func TestMemoryRememberSearchAndForget(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	memory, err := store.RememberMemory(ctx, domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "preference",
		Content:   "Use SQLite for local OpenCTO storage.",
		Tags:      []string{"storage", "sqlite"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if memory.ID != "memory-1" {
		t.Fatalf("unexpected remembered memory: %#v", memory)
	}
	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID: "default",
		Query:     "sqlite storage",
		Scopes:    []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeGlobal},
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("search memory: %v", err)
	}
	if len(found) != 1 || found[0].ID != "memory-1" {
		t.Fatalf("unexpected memory search result: %#v", found)
	}
	deleted, err := store.ForgetMemory(ctx, "default", "memory-1")
	if err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	if !deleted {
		t.Fatalf("expected memory to be deleted")
	}
	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{ProjectID: "default", Query: "sqlite", Limit: 5})
	if err != nil {
		t.Fatalf("search after forget: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("expected no memories after forget, got %#v", found)
	}
}

func TestSearchMemoriesFiltersExactTagsWithEmptyQuery(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:        "sqlite-local",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Use SQLite for local development.",
			Tags:      []string{"storage", "sqlite"},
			CreatedAt: now,
			UpdatedAt: now.Add(3 * time.Second),
		},
		{
			ID:        "postgres-production",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Use Postgres for production.",
			Tags:      []string{"storage", "postgres"},
			CreatedAt: now,
			UpdatedAt: now.Add(2 * time.Second),
		},
		{
			ID:        "other-project-sqlite",
			ProjectID: "other",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Other project SQLite preference.",
			Tags:      []string{"storage", "sqlite"},
			CreatedAt: now,
			UpdatedAt: now.Add(time.Second),
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID: "default",
		Query:     "",
		Scopes:    []domain.MemoryScope{domain.MemoryScopeProject},
		Tags:      []string{"SQLite", "storage"},
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("search memory by exact tags: %v", err)
	}
	if len(found) != 1 || found[0].ID != "sqlite-local" {
		t.Fatalf("expected exact project tag match, got %#v", found)
	}
}

func TestUpdateMemoryReindexesAndPreservesMemoryID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	if _, err := store.RememberMemory(ctx, domain.Memory{
		ID:         "memory-1",
		ProjectID:  "default",
		Scope:      domain.MemoryScopeProject,
		Kind:       "fact",
		Content:    "Use BoltDB for local OpenCTO storage.",
		Tags:       []string{"boltdb", "storage"},
		Confidence: 0.4,
		CreatedAt:  now,
		UpdatedAt:  now,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}

	confidence := 0.9
	pinned := true
	result, err := store.UpdateMemory(ctx, domain.MemoryUpdateRequest{
		ProjectID:   "default",
		MemoryID:    "memory-1",
		Content:     "Use SQLite for durable local OpenCTO storage.",
		Kind:        "preference",
		ReplaceTags: true,
		Tags:        []string{"sqlite", "storage"},
		Confidence:  &confidence,
		Pinned:      &pinned,
	})
	if err != nil {
		t.Fatalf("update memory: %v", err)
	}
	if !result.Updated || result.Memory.ID != "memory-1" {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if result.Memory.Content != "Use SQLite for durable local OpenCTO storage." || result.Memory.Kind != "preference" {
		t.Fatalf("unexpected updated memory fields: %#v", result.Memory)
	}
	if strings.Join(result.Memory.Tags, ",") != "sqlite,storage" || result.Memory.Confidence != 0.9 || !result.Memory.Pinned {
		t.Fatalf("unexpected updated memory metadata: %#v", result.Memory)
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID: "default",
		Query:     "sqlite",
		Tags:      []string{"storage"},
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("search updated memory: %v", err)
	}
	if len(found) != 1 || found[0].ID != "memory-1" {
		t.Fatalf("expected updated memory to be indexed, got %#v", found)
	}
	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID: "default",
		Query:     "boltdb",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("search old memory content: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("expected old FTS content to be removed, got %#v", found)
	}
}

func TestUpdateMemoryReturnsNotUpdatedForUnknownMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	result, err := store.UpdateMemory(ctx, domain.MemoryUpdateRequest{
		ProjectID: "default",
		MemoryID:  "missing-memory",
		Content:   "new content",
	})
	if err != nil {
		t.Fatalf("update missing memory: %v", err)
	}
	if result.Updated {
		t.Fatalf("expected missing memory update to be reported as not updated: %#v", result)
	}
}

func TestMemoryEmbeddingVectorSearch(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:        "sqlite-local",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Use SQLite for local development.",
			Tags:      []string{"storage", "sqlite"},
			CreatedAt: now,
			UpdatedAt: now.Add(time.Second),
		},
		{
			ID:        "postgres-production",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Use Postgres for production.",
			Tags:      []string{"storage", "postgres"},
			CreatedAt: now,
			UpdatedAt: now.Add(2 * time.Second),
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}
	if err := store.UpsertMemoryEmbedding(ctx, domain.MemoryEmbedding{
		MemoryID:    "sqlite-local",
		Provider:    "openai",
		Model:       "text-embedding-3-small",
		Dimensions:  1536,
		ContentHash: "hash-sqlite",
		Vector:      testVector(0),
	}); err != nil {
		t.Fatalf("upsert sqlite embedding: %v", err)
	}
	if err := store.UpsertMemoryEmbedding(ctx, domain.MemoryEmbedding{
		MemoryID:    "postgres-production",
		Provider:    "openai",
		Model:       "text-embedding-3-small",
		Dimensions:  1536,
		ContentHash: "hash-postgres",
		Vector:      testVector(1),
	}); err != nil {
		t.Fatalf("upsert postgres embedding: %v", err)
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:           "default",
		Query:               "database preference",
		Scopes:              []domain.MemoryScope{domain.MemoryScopeProject},
		Tags:                []string{"storage"},
		QueryEmbedding:      testVector(1),
		EmbeddingProvider:   "openai",
		EmbeddingModel:      "text-embedding-3-small",
		EmbeddingDimensions: 1536,
		Limit:               2,
	})
	if err != nil {
		t.Fatalf("search vector memory: %v", err)
	}
	if len(found) == 0 || found[0].ID != "postgres-production" {
		t.Fatalf("expected closest vector memory first, got %#v", found)
	}
}

func TestForgetMemoryDeletesEmbedding(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	if _, err := store.RememberMemory(ctx, domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "fact",
		Content:   "Remembered vector memory.",
		Tags:      []string{"storage"},
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("remember memory: %v", err)
	}
	if err := store.UpsertMemoryEmbedding(ctx, domain.MemoryEmbedding{
		MemoryID:    "memory-1",
		Provider:    "openai",
		Model:       "text-embedding-3-small",
		Dimensions:  1536,
		ContentHash: "hash",
		Vector:      testVector(0),
	}); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}
	deleted, err := store.ForgetMemory(ctx, "default", "memory-1")
	if err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	if !deleted {
		t.Fatalf("expected memory to be deleted")
	}
	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:           "default",
		Query:               "vector",
		Scopes:              []domain.MemoryScope{domain.MemoryScopeProject},
		QueryEmbedding:      testVector(0),
		EmbeddingProvider:   "openai",
		EmbeddingModel:      "text-embedding-3-small",
		EmbeddingDimensions: 1536,
		Limit:               5,
	})
	if err != nil {
		t.Fatalf("search after forget: %v", err)
	}
	if len(found) != 0 {
		t.Fatalf("expected no vector results after forget, got %#v", found)
	}
}

func TestForgetMemoriesByIDsTagsOrScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:        "project-id-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Delete project memory by id.",
			Tags:      []string{"active"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "project-tag-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Delete project cleanup memory by tag.",
			Tags:      []string{"cleanup", "obsolete"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "project-scope-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Delete project memory by scope.",
			Tags:      []string{"active"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "other-project",
			ProjectID: "other",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Other project cleanup memory.",
			Tags:      []string{"cleanup", "obsolete"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "global-id-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeGlobal,
			Kind:      "fact",
			Content:   "Delete global memory by id.",
			Tags:      []string{"active"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "global-tag-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeGlobal,
			Kind:      "fact",
			Content:   "Delete global cleanup memory by tag.",
			Tags:      []string{"cleanup"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "global-scope-delete",
			ProjectID: "default",
			Scope:     domain.MemoryScopeGlobal,
			Kind:      "fact",
			Content:   "Delete global memory by scope.",
			Tags:      []string{"active"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	result, err := store.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: "default",
		MemoryIDs: []string{"project-id-delete", "global-id-delete"},
	})
	if err != nil {
		t.Fatalf("forget memories by ids: %v", err)
	}
	requireMemoryIDs(t, result.DeletedMemoryIDs, "global-id-delete", "project-id-delete")

	result, err = store.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: "default",
		Tags:      []string{"cleanup"},
	})
	if err != nil {
		t.Fatalf("forget memories by tags: %v", err)
	}
	requireMemoryIDs(t, result.DeletedMemoryIDs, "global-tag-delete", "project-tag-delete")

	result, err = store.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: "default",
		Scopes:    []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeGlobal},
	})
	if err != nil {
		t.Fatalf("forget memories by scope: %v", err)
	}
	requireMemoryIDs(t, result.DeletedMemoryIDs, "global-scope-delete", "project-scope-delete")

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		Query:          "",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeGlobal},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search after filtered forget: %v", err)
	}
	remaining := memoryIDs(found)
	if strings.Join(remaining, ",") != "" {
		t.Fatalf("unexpected remaining default memories: %#v", found)
	}
}

func TestForgetMemoriesSupportsCombinedFilters(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:        "delete-me",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Delete matching memory.",
			Tags:      []string{"cleanup", "obsolete"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "wrong-tag",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Keep memory with wrong tag.",
			Tags:      []string{"cleanup"},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "wrong-scope",
			ProjectID: "default",
			Scope:     domain.MemoryScopeGlobal,
			Kind:      "fact",
			Content:   "Keep memory with wrong scope.",
			Tags:      []string{"cleanup", "obsolete"},
			CreatedAt: now,
			UpdatedAt: now,
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}
	result, err := store.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: "default",
		MemoryIDs: []string{"delete-me", "wrong-tag", "wrong-scope"},
		Tags:      []string{"cleanup", "obsolete"},
		Scopes:    []domain.MemoryScope{domain.MemoryScopeProject},
	})
	if err != nil {
		t.Fatalf("forget memories by combined filters: %v", err)
	}
	requireMemoryIDs(t, result.DeletedMemoryIDs, "delete-me")
}

func TestForgetMemoriesRejectsMissingSelector(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	if _, err := store.ForgetMemories(ctx, domain.MemoryForgetRequest{
		ProjectID: "default",
	}); err == nil {
		t.Fatalf("expected forget without selector to fail")
	}
}

func TestMemoryUserScopeIsVisibleOnlyToUser(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:        "project-memory",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Project prefers SQLite for local durable storage.",
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:        "user-one-memory",
			ProjectID: "default",
			UserID:    "discord:user-1",
			Scope:     domain.MemoryScopeUser,
			Kind:      "preference",
			Content:   "User prefers concise technical updates.",
			CreatedAt: now,
			UpdatedAt: now.Add(time.Second),
		},
		{
			ID:        "user-two-memory",
			ProjectID: "default",
			UserID:    "discord:user-2",
			Scope:     domain.MemoryScopeUser,
			Kind:      "preference",
			Content:   "User prefers detailed technical updates.",
			CreatedAt: now,
			UpdatedAt: now.Add(2 * time.Second),
		},
		{
			ID:        "global-memory",
			ProjectID: "default",
			Scope:     domain.MemoryScopeGlobal,
			Kind:      "constraint",
			Content:   "Deployments require explicit approval.",
			CreatedAt: now,
			UpdatedAt: now.Add(3 * time.Second),
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		UserID:         "discord:user-1",
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search visible memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "global-memory", "project-memory", "user-one-memory")

	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		UserID:         "discord:user-2",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeUser},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search user memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "user-two-memory")
}

func TestMemoryThreadScopeIsVisibleOnlyToThread(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:          "thread-one-memory",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-1",
			Scope:       domain.MemoryScopeThread,
			Kind:        "decision",
			Content:     "Use orange accents in this thread.",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "thread-two-memory",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-2",
			Scope:       domain.MemoryScopeThread,
			Kind:        "decision",
			Content:     "Use blue accents in this thread.",
			CreatedAt:   now,
			UpdatedAt:   now.Add(time.Second),
		},
		{
			ID:        "project-memory",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "fact",
			Content:   "Project uses Discord.",
			CreatedAt: now,
			UpdatedAt: now.Add(2 * time.Second),
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-1",
		ThreadID:       "thread-1",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeThread},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search thread memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "thread-one-memory")

	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeThread},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search unscoped thread memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found))
}

func TestMemoryChannelScopeIsVisibleOnlyToChannel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{
			ID:          "channel-one-memory",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Scope:       domain.MemoryScopeChannel,
			Kind:        "decision",
			Content:     "Use concise deployment updates in this channel.",
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		{
			ID:          "channel-two-memory",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-2",
			Scope:       domain.MemoryScopeChannel,
			Kind:        "decision",
			Content:     "Use detailed deployment updates in this channel.",
			CreatedAt:   now,
			UpdatedAt:   now.Add(time.Second),
		},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	found, err := store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeDiscord,
		ChannelID:      "channel-1",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeChannel},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search channel memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "channel-one-memory")

	found, err = store.SearchMemories(ctx, domain.MemorySearchRequest{
		ProjectID:      "default",
		ChannelType:    domain.ChannelTypeLocal,
		ChannelID:      "channel-1",
		Scopes:         []domain.MemoryScope{domain.MemoryScopeChannel},
		FallbackRecent: true,
		Limit:          10,
	})
	if err != nil {
		t.Fatalf("search other platform channel memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found))
}

func TestListMemoriesFiltersVisibleRecentMemory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := time.Now().UTC()
	memories := []domain.Memory{
		{ID: "project-old", ProjectID: "default", Scope: domain.MemoryScopeProject, Kind: "preference", Content: "Project prefers readable migration files.", Tags: []string{"database"}, CreatedAt: now, UpdatedAt: now},
		{ID: "user-new", UserID: "discord:user-1", Scope: domain.MemoryScopeUser, Kind: "preference", Content: "User prefers concise technical explanations.", Tags: []string{"communication"}, CreatedAt: now, UpdatedAt: now.Add(time.Second)},
		{ID: "other-user", UserID: "discord:user-2", Scope: domain.MemoryScopeUser, Kind: "preference", Content: "User prefers detailed technical explanations.", Tags: []string{"communication"}, CreatedAt: now, UpdatedAt: now.Add(2 * time.Second)},
		{ID: "global-constraint", Scope: domain.MemoryScopeGlobal, Kind: "constraint", Content: "Deployments require explicit approval.", Tags: []string{"deployment"}, Pinned: true, CreatedAt: now, UpdatedAt: now.Add(3 * time.Second)},
	}
	for _, memory := range memories {
		if _, err := store.RememberMemory(ctx, memory); err != nil {
			t.Fatalf("remember %s: %v", memory.ID, err)
		}
	}

	found, err := store.ListMemories(ctx, domain.MemoryListRequest{
		ProjectID: "default",
		UserID:    "discord:user-1",
		Scopes:    []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal},
		Kind:      "preference",
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "project-old", "user-new")

	found, err = store.ListMemories(ctx, domain.MemoryListRequest{
		ProjectID: "default",
		UserID:    "discord:user-1",
		Scopes:    []domain.MemoryScope{domain.MemoryScopeUser},
		Tags:      []string{"communication"},
		Limit:     10,
	})
	if err != nil {
		t.Fatalf("list user memories: %v", err)
	}
	requireMemoryIDs(t, memoryIDs(found), "user-new")
}

func TestRememberMemoryAppliesPolicyGate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	cases := []struct {
		name    string
		kind    string
		content string
	}{
		{name: "low-signal", kind: "fact", content: "ok"},
		{name: "secret", kind: "fact", content: "The api_key is sk-123456789012345678901234."},
		{name: "diff", kind: "fact", content: "diff --git a/app.go b/app.go\n@@ -1 +1 @@\n-old\n+new"},
		{name: "stack-trace", kind: "fact", content: "panic: nil pointer dereference\ngoroutine 12 [running]\nmain.main()"},
		{name: "command-output", kind: "fact", content: "command: go test ./...\nstdout:\nok package\nstderr:\n"},
		{name: "temporary", kind: "preference", content: "User prefers raw SQL for this migration."},
		{name: "unsupported-kind", kind: "debugging-note", content: "User prefers concise technical explanations."},
		{name: "scope-like-project-kind", kind: "project", content: "Project prefers concise technical explanations."},
		{name: "scope-like-user-kind", kind: "user", content: "User prefers concise technical explanations."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.RememberMemory(ctx, domain.Memory{
				ID:        "memory-" + tc.name,
				ProjectID: "default",
				Scope:     domain.MemoryScopeProject,
				Kind:      tc.kind,
				Content:   tc.content,
			})
			if !errors.Is(err, storage.ErrMemoryPolicyRejected) {
				t.Fatalf("expected policy rejection, got %v", err)
			}
		})
	}
}

func TestMigrateNormalizesScopeLikeMemoryKinds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	now := formatTime(time.Now().UTC())
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO memories(id, project_id, scope, kind, content, tags, metadata, created_at, updated_at)
VALUES
	('project-kind-memory', 'default', 'project', 'project', 'Project prefers concise explanations.', '[]', '{}', ?, ?),
	('user-kind-memory', '', 'user', 'user', 'User prefers concise explanations.', '[]', '{}', ?, ?)
`, now, now, now, now); err != nil {
		t.Fatalf("insert scope-like memory kinds: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = 8`); err != nil {
		t.Fatalf("remove migration marker: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("rerun migration: %v", err)
	}

	rows, err := store.db.QueryContext(ctx, `SELECT kind FROM memories WHERE id IN ('project-kind-memory', 'user-kind-memory')`)
	if err != nil {
		t.Fatalf("query migrated memory kinds: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			t.Fatalf("scan migrated memory kind: %v", err)
		}
		if kind != "fact" {
			t.Fatalf("expected scope-like kind to migrate to fact, got %q", kind)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migrated memory kinds: %v", err)
	}
}

func TestRememberMemoryRejectsExactDuplicate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := openTestStore(t)
	if _, err := store.RememberMemory(ctx, domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "pref",
		Content:   "User prefers concise technical explanations.",
	}); err != nil {
		t.Fatalf("remember first memory: %v", err)
	}
	remembered, err := store.RememberMemory(ctx, domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "pref",
		Content:   "User prefers concise technical explanations.",
	})
	if err != nil {
		t.Fatalf("upsert same memory id should be allowed: %v", err)
	}
	if remembered.Kind != "preference" {
		t.Fatalf("expected kind normalization, got %q", remembered.Kind)
	}
	_, err = store.RememberMemory(ctx, domain.Memory{
		ID:        "memory-2",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "preference",
		Content:   " user   prefers concise technical explanations. ",
	})
	if !errors.Is(err, storage.ErrMemoryPolicyRejected) {
		t.Fatalf("expected duplicate policy rejection, got %v", err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "opencto.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close store: %v", err)
		}
	})
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	return store
}

func requireMemoryIDs(t *testing.T, actual []string, expected ...string) {
	t.Helper()
	got := append([]string(nil), actual...)
	sort.Strings(got)
	sort.Strings(expected)
	if strings.Join(got, ",") != strings.Join(expected, ",") {
		t.Fatalf("unexpected memory ids: got %#v want %#v", got, expected)
	}
}

func memoryIDs(memories []domain.Memory) []string {
	ids := make([]string, 0, len(memories))
	for _, memory := range memories {
		ids = append(ids, memory.ID)
	}
	sort.Strings(ids)
	return ids
}

func testVector(activeIndex int) []float32 {
	vector := make([]float32, 1536)
	if activeIndex >= 0 && activeIndex < len(vector) {
		vector[activeIndex] = 1
	}
	return vector
}
