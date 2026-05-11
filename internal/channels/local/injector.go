package local

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/channels"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
)

const (
	localDefaultChannelID        = "default"
	localOutboundMessageMaxChars = 2000
)

type Injector struct {
	projectID  string
	dispatcher *runtime.Dispatcher
	logger     *slog.Logger
}

func NewInjector(projectID string, dispatcher *runtime.Dispatcher, logger *slog.Logger) *Injector {
	if logger == nil {
		logger = slog.Default()
	}
	return &Injector{
		projectID:  projectID,
		dispatcher: dispatcher,
		logger:     logger,
	}
}

func (i *Injector) Inject(ctx context.Context, actor, body string) (domain.Event, error) {
	event, err := newLocalEvent(i.projectID, actor, body, time.Now().UTC())
	if err != nil {
		return domain.Event{}, err
	}

	if err := i.dispatcher.EnqueueEvent(ctx, event); err != nil {
		return domain.Event{}, err
	}
	i.logger.Info("local event injected", slog.String("project_id", i.projectID), slog.String("event_id", event.ID), slog.String("channel_id", event.ChannelID))
	return event, nil
}

func newLocalEvent(projectID, actor, body string, now time.Time) (domain.Event, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.Event{}, err
	}

	actor = strings.TrimSpace(actor)
	body = strings.TrimSpace(body)
	event := domain.Event{
		ID:          id,
		ProjectID:   projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   localDefaultChannelID,
		ChannelType: domain.ChannelTypeLocal,
		ActorName:   actor,
		Body:        body,
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeLocal),
			Actor:      actor,
			ObservedAt: now,
		},
		CreatedAt: now,
	}
	return event, nil
}

type Reporter struct {
	logger *slog.Logger
}

func NewReporter(logger *slog.Logger) *Reporter {
	if logger == nil {
		logger = slog.Default()
	}
	return &Reporter{logger: logger}
}

func (r *Reporter) Report(_ context.Context, event domain.Event, report domain.ReportMessage) ([]domain.ReportReceipt, error) {
	text := strings.TrimSpace(report.Text)
	chunks := channels.SplitText(text, localOutboundMessageMaxChars)
	for index, chunk := range chunks {
		if strings.TrimSpace(chunk) == "" && len(report.Attachments) == 0 {
			continue
		}
		r.logger.Info("local report",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("channel_id", event.ChannelID),
			slog.String("message", chunk),
			slog.Int("chunk", index+1),
			slog.Int("chunks", len(chunks)),
			slog.Int("ignored_attachments", len(report.Attachments)),
		)
	}
	return nil, nil
}
