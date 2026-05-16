package activities

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) PersistEvent(ctx context.Context, request PersistEventRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	event = inferDiscordThreadContext(event)
	a.logActivityStep(
		"PersistEvent", "begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
	)
	result, err := a.Store.AppendEvent(ctx, event)
	if err != nil {
		a.logActivityStep(
			"PersistEvent", "error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	if result.Updated && result.Changed {
		a.activityLogger().Warn(
			"event id already existed with different content; stored event was updated",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
		)
	}
	if strings.TrimSpace(event.Body) != "" {
		message := domain.ConversationMessage{
			ID:          stableActivityID("conversation-user", event.ProjectID, event.ID),
			ProjectID:   event.ProjectID,
			EventID:     event.ID,
			Role:        domain.ConversationRoleUser,
			ChannelType: event.ChannelType,
			ChannelID:   strings.TrimSpace(event.ChannelID),
			ThreadID:    strings.TrimSpace(event.ThreadID),
			Body:        event.Body,
			Metadata:    conversationUserMetadata(event),
			CreatedAt:   firstNonZeroTime(event.CreatedAt, time.Now().UTC()),
		}
		if err := a.Store.UpsertConversationMessage(ctx, message); err != nil {
			a.logActivityStep(
				"PersistEvent", "conversation_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return err
		}
	}
	if err := a.persistConversationThread(ctx, conversationThreadFromEvent(event)); err != nil {
		a.logActivityStep(
			"PersistEvent", "thread_error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	a.logActivityStep(
		"PersistEvent", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.Bool("inserted", result.Inserted),
		slog.Bool("updated", result.Updated),
		slog.Bool("changed", result.Changed),
	)
	return nil
}
