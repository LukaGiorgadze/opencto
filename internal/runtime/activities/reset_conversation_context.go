package activities

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) ResetConversationContext(ctx context.Context, request ResetConversationContextRequest) (ResetConversationContextResult, error) {
	if a.Store == nil {
		return ResetConversationContextResult{}, nil
	}
	event := inferDiscordThreadContext(request.Event)
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		return ResetConversationContextResult{}, fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(event.ChannelID) == "" {
		return ResetConversationContextResult{}, fmt.Errorf("channel_id is required")
	}
	scope := domain.ContextResetScopeChannel
	if strings.TrimSpace(event.ThreadID) != "" {
		scope = domain.ContextResetScopeThread
	}
	result, err := a.Store.ResetContext(ctx, domain.ContextResetRequest{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		UserID:      eventUserID(event),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		Scope:       scope,
	})
	if err != nil {
		return ResetConversationContextResult{}, err
	}
	a.logActivityStep(
		"ResetConversationContext", "done",
		slog.String("project_id", strings.TrimSpace(event.ProjectID)),
		slog.String("channel_type", string(event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(event.ChannelID)),
		slog.String("thread_id", strings.TrimSpace(event.ThreadID)),
		slog.String("scope", string(result.Scope)),
		slog.Int("deleted_conversation_messages", result.DeletedConversationMessages),
		slog.Int("deleted_conversation_summaries", result.DeletedConversationSummaries),
		slog.Int("deleted_conversation_threads", result.DeletedConversationThreads),
		slog.Int("deleted_memories", len(result.DeletedMemoryIDs)),
	)
	return ResetConversationContextResult{
		Scope:                        string(result.Scope),
		DeletedConversationMessages:  result.DeletedConversationMessages,
		DeletedConversationSummaries: result.DeletedConversationSummaries,
		DeletedConversationThreads:   result.DeletedConversationThreads,
		DeletedMemoryIDs:             append([]string(nil), result.DeletedMemoryIDs...),
	}, nil
}
