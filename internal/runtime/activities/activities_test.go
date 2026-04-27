package activities

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

type stubProjectStore struct {
	pending []domain.WorkItem
}

func (s stubProjectStore) Append(context.Context, domain.Event) error {
	return nil
}

func (s stubProjectStore) ListByProject(context.Context, string, int) ([]domain.Event, error) {
	return nil, nil
}

func (s stubProjectStore) ListPending(context.Context, string) ([]domain.WorkItem, error) {
	return append([]domain.WorkItem(nil), s.pending...), nil
}

func (s stubProjectStore) UpsertWorkItem(context.Context, domain.WorkItem) error {
	return nil
}

func (s stubProjectStore) GetWorkItem(context.Context, string, string) (domain.WorkItem, error) {
	return domain.WorkItem{}, nil
}

func (s stubProjectStore) UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error {
	return nil
}

func (s stubProjectStore) UpsertToolInvocation(context.Context, domain.ToolInvocation) error {
	return nil
}

func TestFullObservationKeepsLongStdout(t *testing.T) {
	stdout := strings.Repeat("file.go\n", 900)

	observation := fullObservation(stdout, "", nil)
	if observation != "stdout:\n"+strings.TrimSpace(stdout) {
		t.Fatalf("expected full stdout, got %q", observation)
	}
}

func TestFullObservationIncludesAllStreamsAndError(t *testing.T) {
	observation := fullObservation("command output", "command failed\nwith stderr", errors.New("exit status 1"))
	expected := "stdout:\ncommand output\n\nstderr:\ncommand failed\nwith stderr\n\nerror:\nexit status 1"
	if observation != expected {
		t.Fatalf("unexpected observation: %q", observation)
	}
}

func TestLoadContextReturnsProjectAndActiveWorkItems(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 4, 23, 17, 31, 0, 0, time.UTC)
	event := domain.Event{
		ID:          "event-current",
		ProjectID:   "default",
		Kind:        domain.EventKindMessage,
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		ActorName:   "luka",
		Body:        "do it",
		CreatedAt:   base.Add(30 * time.Second),
	}

	workItem := domain.WorkItem{
		ID:        "work-item-1",
		ProjectID: "default",
		Title:     "Inspect workspace",
		Status:    domain.WorkItemStatusPending,
		CreatedAt: base,
		UpdatedAt: base,
	}

	activities := Activities{
		Store:   stubProjectStore{pending: []domain.WorkItem{workItem}},
		Project: domain.Project{ID: "default", Name: "OpenCTO"},
	}

	loaded, err := activities.LoadContext(context.Background(), event)
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
