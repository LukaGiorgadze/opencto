package local

import (
	"context"
	"log/slog"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
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
	id, err := domain.NewID()
	if err != nil {
		return domain.Event{}, err
	}

	event := domain.Event{
		ID:          id,
		ProjectID:   i.projectID,
		Kind:        domain.EventKindMessage,
		ChannelType: domain.ChannelTypeLocal,
		ActorName:   actor,
		Body:        body,
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeLocal),
			Actor:      actor,
			ObservedAt: time.Now().UTC(),
		},
		CreatedAt: time.Now().UTC(),
	}

	if err := i.dispatcher.EnqueueEvent(ctx, event); err != nil {
		return domain.Event{}, err
	}
	i.logger.Info("local event injected", slog.String("project_id", i.projectID), slog.String("event_id", event.ID))
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
	r.logger.Info("local report",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("message", report.Text),
		slog.Int("attachments", len(report.Attachments)),
	)
	return nil, nil
}
