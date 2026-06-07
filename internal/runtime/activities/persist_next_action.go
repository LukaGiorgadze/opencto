package activities

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) PersistNextAction(ctx context.Context, request PersistNextActionRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	a.logActivityStep(
		"PersistNextAction", "begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", request.Status),
		slog.Int("work_items", len(request.NextAction.WorkItems)),
	)
	if err := a.persistNextAction(ctx, request.NextAction); err != nil {
		a.logActivityStep(
			"PersistNextAction", "work_items_error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	if !request.SkipConversation && shouldPersistNextActionConversation(request.Status) {
		message := strings.TrimSpace(request.NextAction.ResponseMessage)
		if message != "" || len(request.NextAction.ResponseAttachments) > 0 {
			metadata := domain.Metadata{"status": strings.TrimSpace(request.Status)}
			if len(request.NextAction.ResponseAttachments) > 0 {
				metadata["attachment_count"] = strconv.Itoa(len(request.NextAction.ResponseAttachments))
			}
			if err := a.Store.UpsertConversationMessage(ctx, domain.ConversationMessage{
				ID:          stableActivityID("conversation-assistant", event.ProjectID, event.ID, request.Status),
				ProjectID:   event.ProjectID,
				EventID:     event.ID,
				Role:        domain.ConversationRoleAssistant,
				ChannelType: event.ChannelType,
				ChannelID:   strings.TrimSpace(event.ChannelID),
				ThreadID:    strings.TrimSpace(event.ThreadID),
				Body:        message,
				Metadata:    metadata,
				CreatedAt:   time.Now().UTC(),
			}); err != nil {
				a.logActivityStep(
					"PersistNextAction", "conversation_error",
					slog.String("project_id", event.ProjectID),
					slog.String("event_id", event.ID),
					slog.String("error", err.Error()),
				)
				return err
			}
		}
	}
	a.logActivityStep(
		"PersistNextAction", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", request.Status),
	)
	return nil
}

func (a *Activities) persistNextAction(ctx context.Context, nextAction agent.NextAction) error {
	if a.Store == nil {
		a.logActivityStep(
			"NextAction", "persist_next_action_skip_no_store",
			slog.Int("work_items", len(nextAction.WorkItems)),
		)
		return nil
	}
	items := make([]domain.WorkItem, 0, len(nextAction.WorkItems))
	for _, item := range nextAction.WorkItems {
		if item.ID == "" {
			a.logActivityStep("NextAction", "persist_next_action_skip_empty_work_item_id")
			continue
		}
		items = append(items, item)
	}
	return a.Store.UpsertWorkItems(ctx, items)
}

func shouldPersistNextActionConversation(status string) bool {
	switch status {
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return true
	default:
		return false
	}
}
