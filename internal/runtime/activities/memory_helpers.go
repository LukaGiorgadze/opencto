package activities

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
	"github.com/opencto/opencto/internal/storage"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
)

func memoryScope(value string) (domain.MemoryScope, error) {
	switch domain.MemoryScope(strings.ToLower(strings.TrimSpace(value))) {
	case domain.MemoryScopeThread:
		return domain.MemoryScopeThread, nil
	case domain.MemoryScopeChannel:
		return domain.MemoryScopeChannel, nil
	case domain.MemoryScopeGlobal:
		return domain.MemoryScopeGlobal, nil
	case domain.MemoryScopeUser:
		return domain.MemoryScopeUser, nil
	case domain.MemoryScopeProject:
		return domain.MemoryScopeProject, nil
	default:
		return "", fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memoryScopeForEvent(value string, event domain.Event) (domain.MemoryScope, error) {
	if strings.TrimSpace(value) == "" {
		event = inferDiscordThreadContext(event)
		if strings.TrimSpace(event.ThreadID) != "" {
			return domain.MemoryScopeThread, nil
		}
		if strings.TrimSpace(event.ChannelID) != "" {
			return domain.MemoryScopeChannel, nil
		}
		return domain.MemoryScopeProject, nil
	}
	return memoryScope(value)
}

func memorySearchScopes(value string) ([]domain.MemoryScope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case memorytool.ScopeThread:
		return []domain.MemoryScope{domain.MemoryScopeThread}, nil
	case memorytool.ScopeChannel:
		return []domain.MemoryScope{domain.MemoryScopeChannel}, nil
	case memorytool.ScopeProject:
		return []domain.MemoryScope{domain.MemoryScopeProject}, nil
	case memorytool.ScopeUser:
		return []domain.MemoryScope{domain.MemoryScopeUser}, nil
	case memorytool.ScopeGlobal:
		return []domain.MemoryScope{domain.MemoryScopeGlobal}, nil
	default:
		return nil, fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memorySearchScopesForEvent(value string, event domain.Event) ([]domain.MemoryScope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return autoContextMemoryScopes(inferDiscordThreadContext(event)), nil
	}
	return memorySearchScopes(value)
}

func memoryForgetScopes(value string) ([]domain.MemoryScope, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return []domain.MemoryScope{domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}, memorytool.ScopeAll, nil
	case memorytool.ScopeThread:
		return []domain.MemoryScope{domain.MemoryScopeThread}, memorytool.ScopeThread, nil
	case memorytool.ScopeChannel:
		return []domain.MemoryScope{domain.MemoryScopeChannel}, memorytool.ScopeChannel, nil
	case memorytool.ScopeProject:
		return []domain.MemoryScope{domain.MemoryScopeProject}, memorytool.ScopeProject, nil
	case memorytool.ScopeUser:
		return []domain.MemoryScope{domain.MemoryScopeUser}, memorytool.ScopeUser, nil
	case memorytool.ScopeGlobal:
		return []domain.MemoryScope{domain.MemoryScopeGlobal}, memorytool.ScopeGlobal, nil
	default:
		return nil, "", fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memoryForgetScopesForEvent(value string, event domain.Event) ([]domain.MemoryScope, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return autoContextMemoryScopes(inferDiscordThreadContext(event)), memorytool.ScopeAll, nil
	}
	return memoryForgetScopes(value)
}

func (a *Activities) searchMemoriesWithEmbeddingQuery(ctx context.Context, request domain.MemorySearchRequest, embeddingQuery string) ([]domain.Memory, error) {
	if a.Store == nil {
		return nil, nil
	}
	embeddingQuery = strings.TrimSpace(embeddingQuery)
	if a.MemoryEmbedder != nil && embeddingQuery != "" {
		result, err := a.MemoryEmbedder.Embed(ctx, []string{embeddingQuery})
		if err != nil {
			a.logActivityStep("Memory", "embed_search_query_failed", slog.String("error", err.Error()))
		} else if len(result.Embeddings) > 0 && len(result.Embeddings[0]) > 0 {
			request.QueryEmbedding = result.Embeddings[0]
			request.EmbeddingProvider = a.MemoryEmbedder.Provider()
			request.EmbeddingModel = a.MemoryEmbedder.Model()
			request.EmbeddingDimensions = a.MemoryEmbedder.Dimensions()
		}
	}
	memories, err := a.Store.SearchMemories(ctx, request)
	if err == nil || len(request.QueryEmbedding) == 0 {
		return memories, err
	}
	a.logActivityStep(
		"Memory", "vector_search_failed",
		slog.String("error", err.Error()),
		slog.String("embedding_provider", request.EmbeddingProvider),
		slog.String("embedding_model", request.EmbeddingModel),
	)
	request.QueryEmbedding = nil
	request.EmbeddingProvider = ""
	request.EmbeddingModel = ""
	request.EmbeddingDimensions = 0
	return a.Store.SearchMemories(ctx, request)
}

func (a *Activities) upsertMemoryEmbedding(ctx context.Context, memory domain.Memory) {
	if a.Store == nil || a.MemoryEmbedder == nil {
		return
	}
	text := embedding.MemoryText(memory)
	if strings.TrimSpace(text) == "" {
		return
	}
	result, err := a.MemoryEmbedder.Embed(ctx, []string{text})
	if err != nil {
		a.logActivityStep(
			"Memory", "embed_memory_failed",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
			slog.String("error", err.Error()),
		)
		return
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		a.logActivityStep(
			"Memory", "embed_memory_empty",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
		)
		return
	}
	if err := a.Store.UpsertMemoryEmbedding(ctx, domain.MemoryEmbedding{
		MemoryID:    memory.ID,
		Provider:    a.MemoryEmbedder.Provider(),
		Model:       a.MemoryEmbedder.Model(),
		Dimensions:  a.MemoryEmbedder.Dimensions(),
		ContentHash: embedding.ContentHash(text),
		Vector:      result.Embeddings[0],
	}); err != nil {
		a.logActivityStep(
			"Memory", "upsert_memory_embedding_failed",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
			slog.String("error", err.Error()),
		)
	}
}

func memoryUpdateAffectsEmbedding(update domain.MemoryUpdateRequest) bool {
	return strings.TrimSpace(update.Content) != "" || strings.TrimSpace(update.Kind) != "" || update.ReplaceTags
}

func memoryPolicyRejectedResult(err error) memoryToolRunResult {
	reason := memoryPolicyRejectionReason(err)
	return memoryToolRunResult{
		Observation: "Memory rejected by policy: " + reason,
		Payload: mustJSON(map[string]any{
			"rejected": true,
			"reason":   reason,
		}),
		Metadata: map[string]string{
			"policy_rejected": "true",
			"reason":          reason,
		},
		Status:     domain.ExecutionStatusFailed,
		ResultCode: "policy_rejected",
		Error:      reason,
	}
}

func memoryPolicyRejectionReason(err error) string {
	if err == nil {
		return "memory rejected by policy"
	}
	reason := strings.TrimSpace(err.Error())
	reason = strings.TrimPrefix(reason, storage.ErrMemoryPolicyRejected.Error()+": ")
	if reason == "" {
		return "memory rejected by policy"
	}
	return reason
}

func memorySearchObservation(memories []domain.Memory) string {
	if len(memories) == 0 {
		return "No memories found."
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "Memory search results.\ncount: %d", len(memories))
	for i, memory := range memories {
		_, _ = fmt.Fprintf(&builder, "\n\n%d. ", i+1)
		writeMemoryObservationFields(&builder, memory)
	}
	return builder.String()
}

func memoryListObservation(memories []domain.Memory, scope, kind string, tags []string) string {
	if len(memories) == 0 {
		return "No memories found.\nscope: " + firstNonEmpty(scope, memorytool.ScopeAll)
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "Memory list.\ncount: %d\nscope: %s", len(memories), firstNonEmpty(scope, memorytool.ScopeAll))
	if strings.TrimSpace(kind) != "" {
		builder.WriteString("\nkind: ")
		builder.WriteString(strings.TrimSpace(kind))
	}
	if len(tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(tags, ", "))
	}
	for i, memory := range memories {
		_, _ = fmt.Fprintf(&builder, "\n\n%d. ", i+1)
		writeMemoryObservationFields(&builder, memory)
	}
	return builder.String()
}

func memoryDetailObservation(title string, memory domain.Memory) string {
	var builder strings.Builder
	builder.WriteString(title)
	builder.WriteString("\n")
	writeMemoryObservationFields(&builder, memory)
	return builder.String()
}

func writeMemoryObservationFields(builder *strings.Builder, memory domain.Memory) {
	_, _ = fmt.Fprintf(
		builder, "memory_id: %s\nscope: %s\nkind: %s\nconfidence: %.2f\npinned: %t",
		strings.TrimSpace(memory.ID),
		memory.Scope,
		firstNonEmpty(memory.Kind, "fact"),
		memory.Confidence,
		memory.Pinned,
	)
	if !memory.UpdatedAt.IsZero() {
		builder.WriteString("\nupdated_at: ")
		builder.WriteString(memory.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if strings.TrimSpace(memory.UserID) != "" {
		builder.WriteString("\nuser_id: ")
		builder.WriteString(strings.TrimSpace(memory.UserID))
	}
	if strings.TrimSpace(memory.ChannelID) != "" {
		builder.WriteString("\nchannel_type: ")
		builder.WriteString(string(memory.ChannelType))
		builder.WriteString("\nchannel_id: ")
		builder.WriteString(strings.TrimSpace(memory.ChannelID))
	}
	if strings.TrimSpace(memory.ThreadID) != "" {
		builder.WriteString("\nthread_id: ")
		builder.WriteString(strings.TrimSpace(memory.ThreadID))
	}
	if strings.TrimSpace(memory.Actor) != "" {
		builder.WriteString("\nactor: ")
		builder.WriteString(strings.TrimSpace(memory.Actor))
	}
	if len(memory.Tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(memory.Tags, ", "))
	}
	builder.WriteString("\ncontent:\n")
	builder.WriteString(strings.TrimSpace(memory.Content))
}

func memoryForgetObservation(memoryIDs, deletedIDs, notFoundIDs, tags []string, scope string) string {
	if len(memoryIDs) == 1 && len(tags) == 0 {
		if len(deletedIDs) > 0 {
			return "Forgot memory.\ndeleted_count: 1\nmemory_id: " + memoryIDs[0]
		}
		return "Memory not found.\nmemory_id: " + memoryIDs[0]
	}

	var builder strings.Builder
	if len(deletedIDs) > 0 {
		_, _ = fmt.Fprintf(&builder, "Forgot memories.\ndeleted_count: %d", len(deletedIDs))
		builder.WriteString("\ndeleted_memory_ids: ")
		builder.WriteString(strings.Join(deletedIDs, ", "))
	} else {
		builder.WriteString("No memories forgotten.")
	}
	if len(memoryIDs) > 0 {
		builder.WriteString("\nrequested_memory_ids: ")
		builder.WriteString(strings.Join(memoryIDs, ", "))
	}
	if len(notFoundIDs) > 0 {
		builder.WriteString("\nnot_found_memory_ids: ")
		builder.WriteString(strings.Join(notFoundIDs, ", "))
	}
	if len(tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(tags, ", "))
	}
	if strings.TrimSpace(scope) != "" {
		builder.WriteString("\nscope: ")
		builder.WriteString(scope)
	}
	return builder.String()
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

func cleanMemoryTags(tags []string) []string {
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

func missingMemoryIDs(requested, deleted []string) []string {
	if len(requested) == 0 {
		return nil
	}
	deletedSet := map[string]bool{}
	for _, memoryID := range deleted {
		deletedSet[memoryID] = true
	}
	var missing []string
	for _, memoryID := range requested {
		if !deletedSet[memoryID] {
			missing = append(missing, memoryID)
		}
	}
	return missing
}
