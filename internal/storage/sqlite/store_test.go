package sqlite

import (
	"context"
	"encoding/json"
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
