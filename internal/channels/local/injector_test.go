package local

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func TestNewLocalEventUsesDefaultChannel(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	event, err := newLocalEvent("project-1", "  luka  ", "\n  do the thing  \n", now)
	if err != nil {
		t.Fatalf("new local event: %v", err)
	}

	if event.ProjectID != "project-1" ||
		event.ChannelType != domain.ChannelTypeLocal ||
		event.ChannelID != localDefaultChannelID ||
		event.ActorName != "luka" ||
		event.Body != "do the thing" {
		t.Fatalf("unexpected event: %#v", event)
	}
	if event.Provenance.Source != string(domain.ChannelTypeLocal) ||
		event.Provenance.Actor != "luka" ||
		!event.Provenance.ObservedAt.Equal(now) ||
		!event.CreatedAt.Equal(now) {
		t.Fatalf("unexpected provenance/timestamps: %#v", event)
	}
}

func TestReporterTrimsTextAndIgnoresAttachments(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&logs, nil)))

	_, err := reporter.Report(context.Background(), domain.Event{
		ID:        "event-1",
		ProjectID: "project-1",
		ChannelID: localDefaultChannelID,
	}, domain.ReportMessage{
		Text: "  done  ",
		Attachments: []domain.ReportAttachment{{
			Path: "/does/not/exist.png",
		}},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	output := logs.String()
	if !strings.Contains(output, `"message":"done"`) ||
		!strings.Contains(output, `"ignored_attachments":1`) {
		t.Fatalf("unexpected log output: %s", output)
	}
}
