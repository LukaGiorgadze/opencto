package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"go.temporal.io/sdk/temporal"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
)

type memoryToolRunResult struct {
	Observation string
	Payload     json.RawMessage
	Metadata    map[string]string
	Status      domain.ExecutionStatus
	ResultCode  string
	Error       string
}

func (a *Activities) runMemoryTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (memoryToolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeMemoryProposeAdd:
		var req memorytool.ProposeAddRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		if strings.TrimSpace(req.Content) == "" {
			err := fmt.Errorf("memory content is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		scope, err := memoryScopeForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memory := domain.Memory{
			ID:          stableActivityID("memory", execution.ProjectID, execution.ToolCallID, strings.TrimSpace(req.Content)),
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Scope:       scope,
			Kind:        firstNonEmpty(req.Kind, "fact"),
			Content:     strings.TrimSpace(req.Content),
			Tags:        req.Tags,
			Source:      "tool",
			SourceID:    execution.ToolCallID,
			Actor:       strings.TrimSpace(sourceEvent.ActorName),
			Confidence:  req.Confidence,
			Pinned:      req.Pinned,
			Metadata:    memoryMetadata(sourceEvent, req.Reason),
		}
		remembered, err := a.Store.RememberMemory(ctx, memory)
		if err != nil {
			if errors.Is(err, storage.ErrMemoryPolicyRejected) {
				return memoryPolicyRejectedResult(err), nil
			}
			return memoryToolRunResult{}, err
		}
		if err := a.completeOnboardingFromMemoryTool(ctx, execution.ProjectID, sourceEvent, choice, remembered.Tags); err != nil {
			return memoryToolRunResult{}, err
		}
		a.upsertMemoryEmbedding(ctx, remembered)
		payload := mustJSON(memorytool.ProposeAddResult{Memory: remembered})
		return memoryToolRunResult{
			Observation: memoryDetailObservation("Accepted memory add proposal.", remembered),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_id": remembered.ID,
				"scope":     string(remembered.Scope),
			},
		}, nil
	case domain.ToolTypeMemorySearch:
		var req memorytool.SearchRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		if strings.TrimSpace(sourceEvent.ProjectID) == "" {
			sourceEvent.ProjectID = execution.ProjectID
		}
		tags := cleanMemoryTags(req.Tags)
		scopes, err := memorySearchScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		query := strings.TrimSpace(req.Query)
		conversation, _, err := a.loadConversationContext(ctx, sourceEvent)
		if err != nil {
			a.logActivityStep(
				"Memory", "tool_search_conversation_context_failed",
				slog.String("project_id", execution.ProjectID),
				slog.String("tool_call_id", execution.ToolCallID),
				slog.String("error", err.Error()),
			)
			conversation = nil
		}
		memories, err := a.searchMemoriesWithEmbeddingQuery(ctx, domain.MemorySearchRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Query:       query,
			Scopes:      scopes,
			Tags:        tags,
			Limit:       req.Limit,
		}, prepareMemoryEmbeddingQuery(query, sourceEvent, nil, conversation))
		if err != nil {
			return memoryToolRunResult{}, err
		}
		payload := mustJSON(memorytool.SearchResult{Memories: memories})
		return memoryToolRunResult{
			Observation: memorySearchObservation(memories),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_count": strconv.Itoa(len(memories)),
				"tags":         strings.Join(tags, ", "),
			},
		}, nil
	case domain.ToolTypeMemoryList:
		var req memorytool.ListRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		tags := cleanMemoryTags(req.Tags)
		scopes, err := memorySearchScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memories, err := a.Store.ListMemories(ctx, domain.MemoryListRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Scopes:      scopes,
			Kind:        strings.TrimSpace(req.Kind),
			Tags:        tags,
			Limit:       req.Limit,
		})
		if err != nil {
			return memoryToolRunResult{}, err
		}
		scope := strings.ToLower(strings.TrimSpace(req.Scope))
		if scope == "" {
			scope = memorytool.ScopeAll
		}
		payload := mustJSON(memorytool.ListResult{Memories: memories})
		return memoryToolRunResult{
			Observation: memoryListObservation(memories, scope, strings.TrimSpace(req.Kind), tags),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_count": strconv.Itoa(len(memories)),
				"scope":        scope,
				"kind":         strings.TrimSpace(req.Kind),
				"tags":         strings.Join(tags, ", "),
			},
		}, nil
	case domain.ToolTypeMemoryProposeUpdate:
		var req memorytool.ProposeUpdateRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memoryID := strings.TrimSpace(req.MemoryID)
		if memoryID == "" {
			err := fmt.Errorf("memory_id is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		update := domain.MemoryUpdateRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			MemoryID:    memoryID,
		}
		hasUpdate := false
		if content := strings.TrimSpace(req.Content); content != "" {
			update.Content = content
			hasUpdate = true
		}
		if kind := strings.TrimSpace(req.Kind); kind != "" {
			update.Kind = kind
			hasUpdate = true
		}
		if scope := strings.TrimSpace(req.Scope); scope != "" {
			targetScope, err := memoryScope(scope)
			if err != nil {
				return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
			}
			update.Scope = targetScope
			hasUpdate = true
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.TagsMode)); mode {
		case "", "keep":
		case "replace":
			update.ReplaceTags = true
			update.Tags = cleanMemoryTags(req.Tags)
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported tags_mode %q", req.TagsMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.ConfidenceMode)); mode {
		case "", "keep":
		case "set":
			if req.Confidence < 0 || req.Confidence > 1 {
				err := fmt.Errorf("memory confidence must be between 0 and 1")
				return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
			}
			confidence := req.Confidence
			update.Confidence = &confidence
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported confidence_mode %q", req.ConfidenceMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.PinnedMode)); mode {
		case "", "keep":
		case "set":
			pinned := req.Pinned
			update.Pinned = &pinned
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported pinned_mode %q", req.PinnedMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		if !hasUpdate {
			err := fmt.Errorf("at least one memory update field is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		result, err := a.Store.UpdateMemory(ctx, update)
		if err != nil {
			if errors.Is(err, storage.ErrMemoryPolicyRejected) {
				return memoryPolicyRejectedResult(err), nil
			}
			return memoryToolRunResult{}, err
		}
		payload := mustJSON(memorytool.ProposeUpdateResult{Memory: result.Memory, Updated: result.Updated})
		observation := "Memory not found.\nmemory_id: " + memoryID
		metadata := map[string]string{
			"memory_id": memoryID,
			"updated":   strconv.FormatBool(result.Updated),
		}
		if result.Updated {
			if err := a.completeOnboardingFromMemoryTool(ctx, execution.ProjectID, sourceEvent, choice, result.Memory.Tags); err != nil {
				return memoryToolRunResult{}, err
			}
			if memoryUpdateAffectsEmbedding(update) {
				a.upsertMemoryEmbedding(ctx, result.Memory)
			}
			observation = memoryDetailObservation("Accepted memory update proposal.", result.Memory)
			metadata["scope"] = string(result.Memory.Scope)
			metadata["tags"] = strings.Join(result.Memory.Tags, ", ")
		}
		return memoryToolRunResult{
			Observation: observation,
			Payload:     payload,
			Metadata:    metadata,
		}, nil
	case domain.ToolTypeMemoryProposeForget:
		var req memorytool.ProposeForgetRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memoryIDs := cleanMemoryIDs(req.MemoryIDs)
		tags := cleanMemoryTags(req.Tags)
		scope := strings.ToLower(strings.TrimSpace(req.Scope))
		if len(memoryIDs) == 0 && len(tags) == 0 && scope == "" {
			err := fmt.Errorf("memory_ids, tags, or scope is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		scopes, scope, err := memoryForgetScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		result, err := a.Store.ForgetMemories(ctx, domain.MemoryForgetRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			MemoryIDs:   memoryIDs,
			Scopes:      scopes,
			Tags:        tags,
		})
		if err != nil {
			return memoryToolRunResult{}, err
		}
		deletedIDs := cleanMemoryIDs(result.DeletedMemoryIDs)
		notFoundIDs := missingMemoryIDs(memoryIDs, deletedIDs)
		deleted := len(deletedIDs) > 0
		payload := mustJSON(memorytool.ProposeForgetResult{
			MemoryIDs:         memoryIDs,
			Deleted:           deleted,
			DeletedCount:      len(deletedIDs),
			DeletedMemoryIDs:  deletedIDs,
			NotFoundMemoryIDs: notFoundIDs,
			Tags:              tags,
			Scope:             scope,
		})
		observation := memoryForgetObservation(memoryIDs, deletedIDs, notFoundIDs, tags, scope)
		return memoryToolRunResult{
			Observation: observation,
			Payload:     payload,
			Metadata: map[string]string{
				"memory_ids":    strings.Join(memoryIDs, ", "),
				"deleted":       strconv.FormatBool(deleted),
				"deleted_count": strconv.Itoa(len(deletedIDs)),
				"deleted_ids":   strings.Join(deletedIDs, ", "),
				"scope":         scope,
				"tags":          strings.Join(tags, ", "),
			},
		}, nil
	default:
		err := fmt.Errorf("unsupported memory tool type %q", choice.Type)
		return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
	}
}
