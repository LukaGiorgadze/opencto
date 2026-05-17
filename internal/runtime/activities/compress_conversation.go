package activities

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

func (a *Activities) CompressConversation(ctx context.Context, request CompressConversationRequest) (CompressConversationResult, error) {
	if a.Store == nil || !a.ConversationEnabled || !a.ConversationSummaryEnabled || a.ConversationCompressor == nil {
		return CompressConversationResult{}, nil
	}
	event := inferDiscordThreadContext(request.Event)
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		return CompressConversationResult{}, nil
	}
	summaryScope, conversationScope := conversationCompressionScopes(event)
	rootMessage, hasRootMessage, err := a.compressionRootMessage(ctx, event, summaryScope)
	if err != nil {
		return CompressConversationResult{}, err
	}
	query := storage.ConversationSummaryQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		Scope:       summaryScope,
		Limit:       1,
	}
	latest, err := a.Store.ListConversationSummaries(ctx, query)
	if err != nil {
		return CompressConversationResult{}, err
	}
	var afterCreatedAt time.Time
	var afterID string
	if len(latest) > 0 {
		last := latest[len(latest)-1]
		afterCreatedAt = last.ToCreatedAt
		afterID = last.ToMessageID
	}
	messages, err := a.Store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:      strings.TrimSpace(event.ProjectID),
		ChannelType:    event.ChannelType,
		ChannelID:      strings.TrimSpace(event.ChannelID),
		ThreadID:       strings.TrimSpace(event.ThreadID),
		Scope:          conversationScope,
		Roles:          conversationRoles(),
		Limit:          500,
		AfterCreatedAt: afterCreatedAt,
		AfterID:        afterID,
		OldestFirst:    true,
		ExcludeControl: true,
	})
	if err != nil {
		return CompressConversationResult{}, err
	}
	recent := storage.DefaultConversationSummaryRecentMessages(a.ConversationSummaryRecent)
	if len(messages) <= recent {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(messages)}, nil
	}
	candidates := messages[:len(messages)-recent]
	sourceChars := conversationSourceChars(candidates)
	trigger := storage.DefaultConversationSummaryTriggerChars(a.ConversationSummaryTrigger)
	if sourceChars < trigger {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(candidates), SourceChars: sourceChars}, nil
	}
	compressorMessages := conversationCompressionMessagesWithRoot(rootMessage, hasRootMessage, candidates)
	compressorCtx := contextWithActivityLLMSession(ctx, strings.TrimSpace(event.ProjectID), "conversation_compression")
	output, err := a.ConversationCompressor.CompressConversation(compressorCtx, agent.ConversationCompressionInput{
		ProjectID:       strings.TrimSpace(event.ProjectID),
		Scope:           summaryScope,
		Messages:        compressorMessages,
		MaxSummaryChars: storage.DefaultConversationSummaryMaxChars(a.ConversationSummaryMaxChars),
	})
	if err != nil {
		return CompressConversationResult{}, err
	}
	summaryText := strings.TrimSpace(output.Summary)
	if summaryText == "" {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(candidates), SourceChars: sourceChars}, nil
	}
	first := candidates[0]
	last := candidates[len(candidates)-1]
	now := time.Now().UTC()
	summary := domain.ConversationSummary{
		ID:            stableActivityID("conversation-summary", event.ProjectID, string(summaryScope), string(event.ChannelType), event.ChannelID, event.ThreadID, first.ID, last.ID),
		ProjectID:     event.ProjectID,
		ChannelType:   event.ChannelType,
		ChannelID:     strings.TrimSpace(event.ChannelID),
		ThreadID:      strings.TrimSpace(event.ThreadID),
		Scope:         summaryScope,
		Summary:       summaryText,
		FromMessageID: strings.TrimSpace(first.ID),
		ToMessageID:   strings.TrimSpace(last.ID),
		FromCreatedAt: first.CreatedAt,
		ToCreatedAt:   last.CreatedAt,
		MessageCount:  len(candidates),
		SourceChars:   sourceChars,
		Metadata:      domain.Metadata{"source": "conversation_compressor"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := a.Store.UpsertConversationSummary(ctx, summary); err != nil {
		return CompressConversationResult{}, err
	}
	a.logActivityStep(
		"CompressConversation", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("scope", string(summaryScope)),
		slog.String("summary_id", summary.ID),
		slog.Int("message_count", len(candidates)),
		slog.Int("source_chars", sourceChars),
	)
	return CompressConversationResult{
		Summarized:   true,
		SummaryID:    summary.ID,
		Scope:        string(summaryScope),
		MessageCount: len(candidates),
		SourceChars:  sourceChars,
	}, nil
}

func (a *Activities) compressionRootMessage(ctx context.Context, event domain.Event, scope domain.ConversationSummaryScope) (domain.ConversationMessage, bool, error) {
	if scope != domain.ConversationSummaryScopeThread || strings.TrimSpace(event.ThreadID) == "" {
		return domain.ConversationMessage{}, false, nil
	}
	thread, ok, err := a.Store.GetConversationThread(ctx, storage.ConversationThreadQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
	})
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if !ok {
		thread = domain.ConversationThread{RootMessageID: strings.TrimSpace(event.ThreadID)}
	}
	return a.conversationThreadRootMessage(ctx, event, thread)
}

func conversationCompressionMessagesWithRoot(root domain.ConversationMessage, ok bool, messages []domain.ConversationMessage) []domain.ConversationMessage {
	if !ok || strings.TrimSpace(root.ID) == "" {
		return messages
	}
	for _, message := range messages {
		if strings.TrimSpace(message.ID) == strings.TrimSpace(root.ID) {
			return messages
		}
	}
	out := make([]domain.ConversationMessage, 0, len(messages)+1)
	out = append(out, root)
	out = append(out, messages...)
	return out
}

func conversationCompressionScopes(event domain.Event) (domain.ConversationSummaryScope, storage.ConversationScope) {
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			return domain.ConversationSummaryScopeThread, storage.ConversationScopeThread
		}
		return domain.ConversationSummaryScopeChannel, storage.ConversationScopeChannel
	}
	return domain.ConversationSummaryScopeProject, storage.ConversationScopeProject
}

func conversationSourceChars(messages []domain.ConversationMessage) int {
	total := 0
	for _, message := range messages {
		total += len(strings.TrimSpace(message.Body))
		if tool := strings.TrimSpace(message.Metadata["tool"]); tool != "" {
			total += len(tool)
		}
		if status := strings.TrimSpace(message.Metadata["status"]); status != "" {
			total += len(status)
		}
		total += len(message.Role)
	}
	return total
}
