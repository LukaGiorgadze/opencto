package activities

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

func (a *Activities) ResponseSession(ctx context.Context, request ResponseSessionRequest) error {
	event := request.Event
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	a.logActivityStep(
		"ResponseSession", "start",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("channel_type", string(event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(event.ChannelID)),
	)
	reporter, ok := a.Reporter.(TypingReporter)
	if !ok || reporter == nil {
		a.logActivityStep(
			"ResponseSession", "skip_no_indicator_reporter",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		return nil
	}

	interval := defaultResponseSessionRefresh
	if request.RefreshIntervalSeconds > 0 {
		interval = time.Duration(request.RefreshIntervalSeconds) * time.Second
	}
	maxAge := defaultResponseSessionMaxAge
	if request.MaxDurationSeconds > 0 {
		maxAge = time.Duration(request.MaxDurationSeconds) * time.Second
	}

	heartbeatDetails := map[string]string{
		"project_id":   projectID,
		"event_id":     event.ID,
		"channel_type": string(event.ChannelType),
	}
	stopHeartbeat := startResponseSessionHeartbeat(ctx, defaultResponseSessionRefresh, heartbeatDetails)
	defer stopHeartbeat()

	refresh := func() {
		typingCtx, cancel := context.WithTimeout(ctx, defaultResponseSessionTimeout)
		defer cancel()
		if err := reporter.NotifyTyping(typingCtx, event); err != nil && ctx.Err() == nil {
			a.logActivityStep(
				"ResponseSession", "indicator_error",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.NewTimer(maxAge)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logActivityStep(
				"ResponseSession", "canceled",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
			)
			return nil
		case <-deadline.C:
			a.logActivityStep(
				"ResponseSession", "expired",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.Duration("max_age", maxAge),
			)
			return nil
		case <-ticker.C:
			refresh()
		}
	}
}
