package activities

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

func (a *Activities) loadConversationContext(ctx context.Context, event domain.Event) ([]domain.ConversationMessage, []domain.ConversationSummary, error) {
	if a.Store == nil || !a.ConversationEnabled {
		return nil, nil, nil
	}
	boundary, hasBoundary, err := a.conversationThreadBoundary(ctx, event)
	if err != nil {
		return nil, nil, err
	}
	var summaries []domain.ConversationSummary
	if a.ConversationSummaryEnabled {
		summaries, err = a.loadConversationSummaries(ctx, event, boundary, hasBoundary)
		if err != nil {
			return nil, nil, err
		}
	}
	conversation, err := a.loadConversationHistory(ctx, event, summaries, boundary, hasBoundary)
	if err != nil {
		return nil, nil, err
	}
	return conversation, summaries, nil
}

type conversationBoundary struct {
	CreatedAt      time.Time
	MessageID      string
	RootMessage    domain.ConversationMessage
	HasRootMessage bool
}

func (a *Activities) conversationThreadBoundary(ctx context.Context, event domain.Event) (conversationBoundary, bool, error) {
	threadID := strings.TrimSpace(event.ThreadID)
	if threadID == "" {
		return conversationBoundary{}, false, nil
	}
	thread := domain.ConversationThread{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    threadID,
	}
	stored, ok, err := a.Store.GetConversationThread(ctx, storage.ConversationThreadQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    threadID,
	})
	if err != nil {
		return conversationBoundary{}, false, err
	}
	if ok {
		thread = stored
	}
	if root, ok, err := a.conversationThreadRootMessage(ctx, event, thread); err != nil {
		return conversationBoundary{}, false, err
	} else if ok {
		boundary := conversationBoundary{
			CreatedAt:      firstNonZeroTime(root.CreatedAt, thread.CreatedAt, event.CreatedAt),
			MessageID:      strings.TrimSpace(root.ID),
			RootMessage:    root,
			HasRootMessage: true,
		}
		return boundary, true, nil
	}
	if !thread.CreatedAt.IsZero() {
		return conversationBoundary{CreatedAt: thread.CreatedAt}, true, nil
	}
	return conversationBoundary{}, false, nil
}

func (a *Activities) conversationThreadRootMessage(ctx context.Context, event domain.Event, thread domain.ConversationThread) (domain.ConversationMessage, bool, error) {
	messageID := strings.TrimSpace(thread.RootMessageID)
	if messageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
		messageID = strings.TrimSpace(event.ThreadID)
	}
	if messageID == "" {
		return domain.ConversationMessage{}, false, nil
	}
	return a.Store.GetConversationRootMessage(ctx, storage.ConversationRootMessageQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		MessageID:   messageID,
	})
}

func (a *Activities) loadConversationHistory(ctx context.Context, event domain.Event, summaries []domain.ConversationSummary, boundary conversationBoundary, hasBoundary bool) ([]domain.ConversationMessage, error) {
	limit := storage.DefaultConversationHistoryLimit(a.ConversationLimit)
	if limit > 50 {
		limit = 50
	}
	base := storage.ConversationQuery{
		ProjectID:      strings.TrimSpace(event.ProjectID),
		ChannelType:    event.ChannelType,
		ChannelID:      strings.TrimSpace(event.ChannelID),
		ThreadID:       strings.TrimSpace(event.ThreadID),
		Roles:          conversationRoles(),
		Limit:          limit,
		ExcludeEventID: strings.TrimSpace(event.ID),
		ExcludeControl: true,
	}
	var messages []domain.ConversationMessage
	seen := map[string]bool{}
	appendMessage := func(message domain.ConversationMessage) {
		if strings.TrimSpace(message.ID) == "" || seen[message.ID] {
			return
		}
		seen[message.ID] = true
		messages = append(messages, message)
	}
	appendMessages := func(scope storage.ConversationScope, cutoff domain.ConversationSummary) error {
		if limit <= 0 {
			return nil
		}
		query := base
		query.Scope = scope
		query.Limit = limit
		query.AfterCreatedAt = cutoff.ToCreatedAt
		query.AfterID = cutoff.ToMessageID
		if hasBoundary && scope != storage.ConversationScopeThread {
			query.BeforeCreatedAt = boundary.CreatedAt
			query.BeforeID = boundary.MessageID
		}
		found, err := a.Store.ListConversationMessages(ctx, query)
		if err != nil {
			return err
		}
		for _, message := range found {
			appendMessage(message)
		}
		return nil
	}
	threadSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeThread)
	channelSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeChannel)
	projectSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeProject)
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			if err := appendMessages(storage.ConversationScopeThread, threadSummary); err != nil {
				return nil, err
			}
			if err := appendMessages(storage.ConversationScopeChannel, channelSummary); err != nil {
				return nil, err
			}
			if boundary.HasRootMessage {
				appendMessage(boundary.RootMessage)
			}
		} else if err := appendMessages(storage.ConversationScopeChannel, channelSummary); err != nil {
			return nil, err
		}
	} else {
		if err := appendMessages(storage.ConversationScopeProject, projectSummary); err != nil {
			return nil, err
		}
	}
	sortConversationMessages(messages)
	return messages, nil
}

func latestConversationSummary(summaries []domain.ConversationSummary, scope domain.ConversationSummaryScope) (domain.ConversationSummary, bool) {
	var latest domain.ConversationSummary
	found := false
	for _, summary := range summaries {
		if summary.Scope != scope {
			continue
		}
		if !found || summary.ToCreatedAt.After(latest.ToCreatedAt) ||
			(summary.ToCreatedAt.Equal(latest.ToCreatedAt) && summary.ToMessageID > latest.ToMessageID) {
			latest = summary
			found = true
		}
	}
	return latest, found
}

func sortConversationMessages(messages []domain.ConversationMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		left := messages[i].CreatedAt
		right := messages[j].CreatedAt
		if left.Equal(right) {
			return messages[i].ID < messages[j].ID
		}
		return left.Before(right)
	})
}

func (a *Activities) loadConversationSummaries(ctx context.Context, event domain.Event, boundary conversationBoundary, hasBoundary bool) ([]domain.ConversationSummary, error) {
	base := storage.ConversationSummaryQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
	}
	type summaryScopeQuery struct {
		scope domain.ConversationSummaryScope
		limit int
	}
	var scopes []summaryScopeQuery
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			scopes = append(
				scopes,
				summaryScopeQuery{scope: domain.ConversationSummaryScopeThread, limit: 3},
				summaryScopeQuery{scope: domain.ConversationSummaryScopeChannel, limit: 1},
			)
		} else {
			scopes = append(
				scopes,
				summaryScopeQuery{scope: domain.ConversationSummaryScopeChannel, limit: 3},
			)
		}
	} else {
		scopes = append(scopes, summaryScopeQuery{scope: domain.ConversationSummaryScopeProject, limit: 3})
	}
	var summaries []domain.ConversationSummary
	seen := map[string]bool{}
	for _, item := range scopes {
		query := base
		query.Scope = item.scope
		query.Limit = item.limit
		if hasBoundary && item.scope != domain.ConversationSummaryScopeThread {
			query.BeforeCreatedAt = boundary.CreatedAt
			query.BeforeID = boundary.MessageID
		}
		found, err := a.Store.ListConversationSummaries(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, summary := range found {
			if strings.TrimSpace(summary.ID) == "" || seen[summary.ID] {
				continue
			}
			seen[summary.ID] = true
			summaries = append(summaries, summary)
		}
	}
	return summaries, nil
}
