package main

import (
	"context"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
)

type captureChannelReporter struct {
	reports int
	typing  int
}

func (r *captureChannelReporter) Report(context.Context, domain.Event, domain.ReportMessage) ([]domain.ReportReceipt, error) {
	r.reports++
	return nil, nil
}

func (r *captureChannelReporter) NotifyTyping(context.Context, domain.Event) error {
	r.typing++
	return nil
}

func TestChannelReporterRoutesByChannelType(t *testing.T) {
	t.Parallel()

	localReporter := &captureChannelReporter{}
	discordReporter := &captureChannelReporter{}
	reporter := newChannelReporter(localReporter, map[domain.ChannelType]activities.Reporter{
		domain.ChannelTypeDiscord: discordReporter,
	})

	if _, err := reporter.Report(context.Background(), domain.Event{ChannelType: domain.ChannelTypeCLI}, domain.ReportMessage{Text: "local"}); err != nil {
		t.Fatalf("report cli: %v", err)
	}
	if _, err := reporter.Report(context.Background(), domain.Event{ChannelType: domain.ChannelTypeDiscord}, domain.ReportMessage{Text: "discord"}); err != nil {
		t.Fatalf("report discord: %v", err)
	}

	if localReporter.reports != 1 {
		t.Fatalf("expected one local report, got %d", localReporter.reports)
	}
	if discordReporter.reports != 1 {
		t.Fatalf("expected one discord report, got %d", discordReporter.reports)
	}
}

func TestChannelReporterTypingOnlyUsesDiscord(t *testing.T) {
	t.Parallel()

	localReporter := &captureChannelReporter{}
	discordReporter := &captureChannelReporter{}
	reporter := newChannelReporter(localReporter, map[domain.ChannelType]activities.Reporter{
		domain.ChannelTypeDiscord: discordReporter,
	})

	if err := reporter.NotifyTyping(context.Background(), domain.Event{ChannelType: domain.ChannelTypeCLI}); err != nil {
		t.Fatalf("notify cli typing: %v", err)
	}
	if err := reporter.NotifyTyping(context.Background(), domain.Event{ChannelType: domain.ChannelTypeDiscord}); err != nil {
		t.Fatalf("notify discord typing: %v", err)
	}

	if localReporter.typing != 0 {
		t.Fatalf("expected no local typing notifications, got %d", localReporter.typing)
	}
	if discordReporter.typing != 1 {
		t.Fatalf("expected one discord typing notification, got %d", discordReporter.typing)
	}
}

func TestChannelReporterErrorsWhenChannelTypeIsNotConfigured(t *testing.T) {
	t.Parallel()

	localReporter := &captureChannelReporter{}
	reporter := newChannelReporter(localReporter, nil)

	_, err := reporter.Report(context.Background(), domain.Event{ChannelType: domain.ChannelTypeDiscord}, domain.ReportMessage{Text: "discord"})
	if err == nil {
		t.Fatal("expected missing channel reporter error")
	}
	if !strings.Contains(err.Error(), `channel_type "discord" is not enabled in config`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if localReporter.reports != 0 {
		t.Fatalf("expected missing discord reporter not to fall back to local, got %d local reports", localReporter.reports)
	}
}
