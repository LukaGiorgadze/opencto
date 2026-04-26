package activities

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/memory/sqlite"
)

func TestSummarizeObservationTruncatesLongStdout(t *testing.T) {
	stdout := strings.Repeat("file.go\n", 900)

	summary := summarizeObservation(stdout, "", nil)
	if summary == strings.TrimSpace(stdout) {
		t.Fatalf("expected long stdout to be summarized, got original output")
	}
	if !strings.Contains(summary, "output truncated") {
		t.Fatalf("expected truncation notice, got %q", summary)
	}
}

func TestSummarizeObservationUsesStderrWhenStdoutEmpty(t *testing.T) {
	summary := summarizeObservation("", "command failed\nwith stderr", nil)
	if summary != "command failed\nwith stderr" {
		t.Fatalf("unexpected stderr summary: %q", summary)
	}
}

func TestLoadContextReturnsProjectAndActiveWorkItems(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "memory.db"), "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 4, 23, 17, 31, 0, 0, time.UTC)
	events := []domain.Event{
		{
			ID:          "event-source",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "you should remove git and create it in hello-world",
			CreatedAt:   base,
		},
		{
			ID:          "event-path",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "`/Users/luka/projects/opencto/hello-world`",
			CreatedAt:   base.Add(10 * time.Second),
		},
		{
			ID:          "event-yes",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "yes",
			CreatedAt:   base.Add(20 * time.Second),
		},
		{
			ID:          "event-current",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "do it",
			CreatedAt:   base.Add(30 * time.Second),
		},
	}
	for _, event := range events {
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatalf("append event %s: %v", event.ID, err)
		}
	}

	workItem := domain.WorkItem{
		ID:        "work-item-1",
		ProjectID: "default",
		Title:     "Inspect workspace",
		Status:    domain.WorkItemStatusPending,
		CreatedAt: base,
		UpdatedAt: base,
	}
	if err := store.UpsertWorkItem(context.Background(), workItem); err != nil {
		t.Fatalf("upsert work item: %v", err)
	}

	activities := Activities{
		Store:   store,
		Project: domain.Project{ID: "default", Name: "OpenCTO"},
	}

	loaded, err := activities.LoadContext(context.Background(), events[len(events)-1])
	if err != nil {
		t.Fatalf("load context: %v", err)
	}

	if loaded.Project.ID != "default" {
		t.Fatalf("expected project id to be carried through, got %q", loaded.Project.ID)
	}
	if len(loaded.ActiveWorkItems) != 1 {
		t.Fatalf("expected one active work item, got %d", len(loaded.ActiveWorkItems))
	}
	if loaded.ActiveWorkItems[0].ID != workItem.ID {
		t.Fatalf("unexpected work item id: %s", loaded.ActiveWorkItems[0].ID)
	}
}
