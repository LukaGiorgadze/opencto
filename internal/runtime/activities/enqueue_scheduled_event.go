package activities

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/opencto/opencto/internal/runtime/scheduled"
)

func (a *Activities) EnqueueScheduledEvent(ctx context.Context, request scheduled.EnqueueScheduledEventRequest) error {
	if a.EventEnqueuer == nil {
		return fmt.Errorf("event enqueuer is not configured")
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(event.ID) == "" {
		return fmt.Errorf("scheduled event id is required")
	}
	a.logActivityStep(
		"EnqueueScheduledEvent", "start",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("schedule_id", scheduled.ScheduleID(event)),
	)
	if err := a.EventEnqueuer.EnqueueEvent(ctx, event); err != nil {
		a.logActivityStep(
			"EnqueueScheduledEvent", "error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	a.logActivityStep(
		"EnqueueScheduledEvent", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
	)
	return nil
}
