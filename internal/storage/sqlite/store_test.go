package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
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
