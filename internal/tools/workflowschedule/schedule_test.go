package workflowschedule

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	sqlitestore "github.com/opencto/opencto/internal/storage/sqlite"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func TestCreateWorkflowScheduleCommitsBundleAndCreatesTemporalSchedule(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	store, err := sqlitestore.Open(ctx, filepath.Join(workspaceRoot, "opencto.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	client := &fakeScheduleClient{}
	executor := &TemporalExecutor{
		Client:        client,
		Store:         store,
		TaskQueue:     "opencto",
		WorkspaceRoot: workspaceRoot,
		Now: func() time.Time {
			return time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
		},
		ResolveTimeZone: func() (*time.Location, string, error) {
			return time.FixedZone("Asia/Tbilisi", 4*60*60), "Asia/Tbilisi", nil
		},
	}

	result, err := executor.Run(ctx, Request{
		ProjectID:   "project-1",
		Operation:   OperationCreate,
		WorkflowID:  "daily-etl",
		Name:        "daily etl",
		Description: "runs an etl workflow",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 2 * * *",
			OverlapPolicy: workflowbundle.OverlapPolicyBufferOne,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Env:                []string{"GITHUB_TOKEN"},
		Steps: []workflowbundle.Step{{
			ID:                  "download",
			Command:             "python",
			Args:                []string{"src/etl.py", "download"},
			StartToCloseTimeout: "10m",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 3,
			},
		}},
		Files: []workflowbundle.File{{
			Path:    "src/etl.py",
			Content: "print('ok')\n",
		}},
		SourceEvent: domain.Event{ID: "event-1", ProjectID: "project-1", ChannelID: "channel-1"},
	})
	if err != nil {
		t.Fatalf("create workflow schedule: %v", err)
	}
	if result.WorkflowID != "daily-etl" || result.CommitHash == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(client.created) != 1 {
		t.Fatalf("expected one schedule create, got %d", len(client.created))
	}
	options := client.created[0]
	if options.ID != workflowrun.ScheduleID("project-1", "daily-etl") ||
		options.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE ||
		options.CatchupWindow != 5*time.Minute {
		t.Fatalf("unexpected schedule options: %#v", options)
	}
	action, ok := options.Action.(*temporalclient.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("expected workflow action, got %T", options.Action)
	}
	if action.Workflow != workflowrun.WorkflowName || action.TaskQueue != "opencto" {
		t.Fatalf("unexpected action: %#v", action)
	}
	input, ok := action.Args[0].(workflowrun.Input)
	if !ok {
		t.Fatalf("expected workflow run input, got %T", action.Args[0])
	}
	if input.WorkflowID != "daily-etl" || input.CommitHash != result.CommitHash || input.SourceEvent.ChannelID != "channel-1" {
		t.Fatalf("unexpected workflow input: %#v", input)
	}
	workflow, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "daily-etl")
	if err != nil || !ok {
		t.Fatalf("stored workflow: ok=%t err=%v", ok, err)
	}
	if workflow.CurrentCommitHash != result.CommitHash || workflow.TemporalScheduleID != result.ScheduleID {
		t.Fatalf("unexpected stored workflow: %#v", workflow)
	}
}

type fakeScheduleClient struct {
	created []temporalclient.ScheduleOptions
	handle  *fakeScheduleHandle
	entries []*temporalclient.ScheduleListEntry
}

func (f *fakeScheduleClient) Create(_ context.Context, options temporalclient.ScheduleOptions) (temporalclient.ScheduleHandle, error) {
	f.created = append(f.created, options)
	handle := f.handle
	if handle == nil {
		handle = &fakeScheduleHandle{}
		f.handle = handle
	}
	handle.id = options.ID
	return handle, nil
}

func (f *fakeScheduleClient) List(context.Context, temporalclient.ScheduleListOptions) (temporalclient.ScheduleListIterator, error) {
	return &fakeScheduleIterator{entries: f.entries}, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, id string) temporalclient.ScheduleHandle {
	handle := f.handle
	if handle == nil {
		handle = &fakeScheduleHandle{}
		f.handle = handle
	}
	handle.id = id
	return handle
}

type fakeScheduleHandle struct {
	id        string
	deleted   int
	updated   int
	triggered int
	paused    int
	unpaused  int
}

func (f *fakeScheduleHandle) GetID() string {
	return f.id
}

func (f *fakeScheduleHandle) Delete(context.Context) error {
	f.deleted++
	return nil
}

func (f *fakeScheduleHandle) Backfill(context.Context, temporalclient.ScheduleBackfillOptions) error {
	return nil
}

func (f *fakeScheduleHandle) Update(_ context.Context, options temporalclient.ScheduleUpdateOptions) error {
	f.updated++
	if options.DoUpdate != nil {
		_, err := options.DoUpdate(temporalclient.ScheduleUpdateInput{})
		return err
	}
	return nil
}

func (f *fakeScheduleHandle) Describe(context.Context) (*temporalclient.ScheduleDescription, error) {
	return &temporalclient.ScheduleDescription{}, nil
}

func (f *fakeScheduleHandle) Trigger(context.Context, temporalclient.ScheduleTriggerOptions) error {
	f.triggered++
	return nil
}

func (f *fakeScheduleHandle) Pause(context.Context, temporalclient.SchedulePauseOptions) error {
	f.paused++
	return nil
}

func (f *fakeScheduleHandle) Unpause(context.Context, temporalclient.ScheduleUnpauseOptions) error {
	f.unpaused++
	return nil
}

type fakeScheduleIterator struct {
	entries []*temporalclient.ScheduleListEntry
	index   int
}

func (f *fakeScheduleIterator) HasNext() bool {
	return f.index < len(f.entries)
}

func (f *fakeScheduleIterator) Next() (*temporalclient.ScheduleListEntry, error) {
	entry := f.entries[f.index]
	f.index++
	return entry, nil
}
