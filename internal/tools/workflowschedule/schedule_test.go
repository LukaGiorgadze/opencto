package workflowschedule

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	sqlitestore "github.com/opencto/opencto/internal/storage/sqlite"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func TestWorkflowCreateCommitsBundleAndCreatesTemporalSchedule(t *testing.T) {
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

	result, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
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

func TestWorkflowUpdateCommitsDirtyWorkflowFiles(t *testing.T) {
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

	created, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "finance2049",
		Name:        "finance2049 availability",
		Description: "checks website availability",
		Schedule: workflowbundle.Schedule{
			Cron:          "* * * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Env:                []string{"CHECK_TOKEN"},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "python3",
			Args:                []string{"src/check_site.py"},
			StartToCloseTimeout: "30s",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 2,
			},
		}},
		Files: []workflowbundle.File{{
			Path:    "src/check_site.py",
			Content: "print('old')\n",
		}},
	})
	if err != nil {
		t.Fatalf("create workflow schedule: %v", err)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "check_site.py"), []byte("print('new')\n"), 0o644); err != nil {
		t.Fatalf("edit workflow source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "helper.py"), []byte("print('helper')\n"), 0o644); err != nil {
		t.Fatalf("write workflow helper: %v", err)
	}

	updated, err := executor.Update(ctx, UpdateRequest{
		ProjectID:     "project-1",
		WorkflowID:    "finance2049",
		CommitMessage: "update finance2049 workflow code",
	})
	if err != nil {
		t.Fatalf("update workflow schedule: %v", err)
	}
	if updated.CommitHash == "" || updated.CommitHash == created.CommitHash {
		t.Fatalf("expected new commit hash, created=%q updated=%q", created.CommitHash, updated.CommitHash)
	}
	if client.handle == nil || client.handle.updated != 1 || client.handle.lastUpdate == nil {
		t.Fatalf("expected one schedule update, handle=%#v", client.handle)
	}
	action, ok := client.handle.lastUpdate.Schedule.Action.(*temporalclient.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("expected workflow action, got %T", client.handle.lastUpdate.Schedule.Action)
	}
	input, ok := action.Args[0].(workflowrun.Input)
	if !ok {
		t.Fatalf("expected workflow input, got %T", action.Args[0])
	}
	if input.CommitHash != updated.CommitHash {
		t.Fatalf("expected updated commit hash in schedule input, got %#v", input)
	}
	snapshot := t.TempDir()
	if err := workflowbundle.ArchiveCommit(ctx, workflowPath, updated.CommitHash, snapshot); err != nil {
		t.Fatalf("archive updated commit: %v", err)
	}
	manifest, err := workflowbundle.LoadManifest(snapshot)
	if err != nil {
		t.Fatalf("load archived manifest: %v", err)
	}
	if len(manifest.Env) != 1 || manifest.Env[0] != "CHECK_TOKEN" {
		t.Fatalf("expected sparse update to preserve env, got %#v", manifest.Env)
	}
	source, err := os.ReadFile(filepath.Join(snapshot, "src", "check_site.py"))
	if err != nil {
		t.Fatalf("read archived source: %v", err)
	}
	if string(source) != "print('new')\n" {
		t.Fatalf("expected edited source in commit, got %q", string(source))
	}
	helper, err := os.ReadFile(filepath.Join(snapshot, "src", "helper.py"))
	if err != nil {
		t.Fatalf("read archived helper: %v", err)
	}
	if string(helper) != "print('helper')\n" {
		t.Fatalf("expected new helper in commit, got %q", string(helper))
	}
}

func TestWorkflowCreateFailsWhenWorkflowAlreadyExists(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, store, _ := newWorkflowTestExecutor(t, t.TempDir())
	request := CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "daily-etl",
		Name:        "daily etl",
		Description: "runs an etl workflow",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 2 * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Env:                []string{},
		Steps: []workflowbundle.Step{{
			ID:                  "download",
			Command:             "python",
			Args:                []string{"src/etl.py"},
			StartToCloseTimeout: "10m",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 1,
			},
		}},
		Files: []workflowbundle.File{{
			Path:    "src/etl.py",
			Content: "print('ok')\n",
		}},
	}
	if _, err := executor.Create(ctx, request); err != nil {
		t.Fatalf("first create workflow: %v", err)
	}
	if _, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "daily-etl"); err != nil || !ok {
		t.Fatalf("stored workflow: ok=%t err=%v", ok, err)
	}
	if _, err := executor.Create(ctx, request); err == nil {
		t.Fatal("expected duplicate workflow create to fail")
	}
}

func TestWorkflowCreateIgnoresUnregisteredLocalBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, client := newWorkflowTestExecutor(t, workspaceRoot)
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "daily-etl")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(workflowPath, "src"), 0o755); err != nil {
		t.Fatalf("mkdir stale bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "old.py"), []byte("print('old')\n"), 0o644); err != nil {
		t.Fatalf("write stale source: %v", err)
	}

	result, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "daily-etl",
		Name:        "daily etl",
		Description: "runs an etl workflow",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 2 * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		Env: []string{},
		Steps: []workflowbundle.Step{{
			ID:                  "download",
			Command:             "python",
			Args:                []string{"src/etl.py"},
			StartToCloseTimeout: "10m",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 1,
			},
		}},
		Files: []workflowbundle.File{{Path: "src/etl.py", Content: "print('ok')\n"}},
	})
	if err != nil {
		t.Fatalf("create workflow from stale bundle: %v", err)
	}
	if result.CommitHash == "" || len(client.created) != 1 {
		t.Fatalf("expected workflow to be created, result=%#v created=%d", result, len(client.created))
	}
}

func TestWorkflowUpdateCanClearOptionalManifestFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, _ := newWorkflowTestExecutor(t, workspaceRoot)
	if _, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "finance2049",
		Name:        "finance2049 availability",
		Description: "checks website availability",
		Schedule: workflowbundle.Schedule{
			Cron:          "* * * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Env:                []string{"CHECK_TOKEN"},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "python3",
			Args:                []string{"src/check_site.py"},
			StartToCloseTimeout: "30s",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 2,
			},
		}},
		Files: []workflowbundle.File{{
			Path:    "src/check_site.py",
			Content: "print('old')\n",
		}},
	}); err != nil {
		t.Fatalf("create workflow schedule: %v", err)
	}

	description := ""
	onFailure := false
	updated, err := executor.Update(ctx, UpdateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "finance2049",
		Description: &description,
		NotificationPolicy: &NotificationPolicyPatch{
			OnFailure: &onFailure,
		},
		Env: []string{},
	})
	if err != nil {
		t.Fatalf("update workflow schedule: %v", err)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	snapshot := t.TempDir()
	if err := workflowbundle.ArchiveCommit(ctx, workflowPath, updated.CommitHash, snapshot); err != nil {
		t.Fatalf("archive updated commit: %v", err)
	}
	manifest, err := workflowbundle.LoadManifest(snapshot)
	if err != nil {
		t.Fatalf("load archived manifest: %v", err)
	}
	if manifest.Description != "" || manifest.NotificationPolicy.OnFailure || len(manifest.Env) != 0 {
		t.Fatalf("expected optional fields to be cleared, got %#v", manifest)
	}
}

func TestWorkflowDeleteHardDeletesScheduleStoreAndFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	if _, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "finance2049",
		Name:        "finance2049 availability",
		Description: "checks website availability",
		Schedule: workflowbundle.Schedule{
			Cron:          "* * * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Env:                []string{},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "python3",
			Args:                []string{"src/check_site.py"},
			StartToCloseTimeout: "30s",
			RetryPolicy:         workflowbundle.RetryPolicy{MaximumAttempts: 1},
		}},
		Files: []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}},
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	runPath, err := workflowbundle.WorkflowRunDir(workspaceRoot, "finance2049", "run-1")
	if err != nil {
		t.Fatalf("workflow run dir: %v", err)
	}
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		t.Fatalf("mkdir workflow run: %v", err)
	}

	result, err := executor.Delete(ctx, DeleteRequest{ProjectID: "project-1", WorkflowID: "finance2049"})
	if err != nil {
		t.Fatalf("delete workflow: %v", err)
	}
	if result.Operation != OperationDelete || client.handle == nil || client.handle.deleted != 1 {
		t.Fatalf("unexpected delete result=%#v handle=%#v", result, client.handle)
	}
	if _, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "finance2049"); err != nil || ok {
		t.Fatalf("expected stored workflow to be hard deleted, ok=%t err=%v", ok, err)
	}
	if _, err := os.Stat(workflowPath); !os.IsNotExist(err) {
		t.Fatalf("expected workflow path to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(runPath)); !os.IsNotExist(err) {
		t.Fatalf("expected workflow runs path to be deleted, stat err=%v", err)
	}
}

func TestWorkflowDeleteHardDeletesWhenScheduleIsAlreadyMissing(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	if _, err := executor.Create(ctx, CreateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Name:       "finance2049 availability",
		Schedule: workflowbundle.Schedule{
			Cron:          "* * * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		Env: []string{},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "python3",
			Args:                []string{"src/check_site.py"},
			StartToCloseTimeout: "30s",
			RetryPolicy:         workflowbundle.RetryPolicy{MaximumAttempts: 1},
		}},
		Files: []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}},
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	runPath, err := workflowbundle.WorkflowRunDir(workspaceRoot, "finance2049", "run-1")
	if err != nil {
		t.Fatalf("workflow run dir: %v", err)
	}
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		t.Fatalf("mkdir workflow run: %v", err)
	}
	client.handle.deleteErr = serviceerror.NewNotFound("schedule not found")

	if _, err := executor.Delete(ctx, DeleteRequest{ProjectID: "project-1", WorkflowID: "finance2049"}); err != nil {
		t.Fatalf("delete missing schedule: %v", err)
	}
	if _, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "finance2049"); err != nil || ok {
		t.Fatalf("expected stored workflow to be hard deleted, ok=%t err=%v", ok, err)
	}
	if _, err := os.Stat(workflowPath); !os.IsNotExist(err) {
		t.Fatalf("expected workflow path to be deleted, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Dir(runPath)); !os.IsNotExist(err) {
		t.Fatalf("expected workflow runs path to be deleted, stat err=%v", err)
	}
}

func TestWorkflowOperationDispatchesControlActions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, _, client := newWorkflowTestExecutor(t, t.TempDir())
	if _, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationTrigger, WorkflowID: "finance2049"}); err != nil {
		t.Fatalf("trigger workflow: %v", err)
	}
	if _, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationPause, WorkflowID: "finance2049"}); err != nil {
		t.Fatalf("pause workflow: %v", err)
	}
	if _, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationResume, WorkflowID: "finance2049"}); err != nil {
		t.Fatalf("resume workflow: %v", err)
	}
	if client.handle == nil || client.handle.triggered != 1 || client.handle.paused != 1 || client.handle.unpaused != 1 {
		t.Fatalf("unexpected control calls: %#v", client.handle)
	}
}

type fakeScheduleClient struct {
	created []temporalclient.ScheduleOptions
	handle  *fakeScheduleHandle
	entries []*temporalclient.ScheduleListEntry
}

func newWorkflowTestExecutor(t *testing.T, workspaceRoot string) (*TemporalExecutor, *sqlitestore.Store, *fakeScheduleClient) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, filepath.Join(workspaceRoot, "opencto.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
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
	return executor, store, client
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
	id         string
	deleted    int
	deleteErr  error
	updated    int
	triggered  int
	paused     int
	unpaused   int
	lastUpdate *temporalclient.ScheduleUpdate
}

func (f *fakeScheduleHandle) GetID() string {
	return f.id
}

func (f *fakeScheduleHandle) Delete(context.Context) error {
	f.deleted++
	return f.deleteErr
}

func (f *fakeScheduleHandle) Backfill(context.Context, temporalclient.ScheduleBackfillOptions) error {
	return nil
}

func (f *fakeScheduleHandle) Update(_ context.Context, options temporalclient.ScheduleUpdateOptions) error {
	f.updated++
	if options.DoUpdate != nil {
		update, err := options.DoUpdate(temporalclient.ScheduleUpdateInput{})
		f.lastUpdate = update
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
