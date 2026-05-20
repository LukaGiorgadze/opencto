package workflowschedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

func testWorkflowManifest(name string) workflowbundle.Manifest {
	return workflowbundle.Manifest{
		Name:        name,
		Description: "checks website availability",
		Schedule: workflowbundle.Schedule{
			Cron:          "* * * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "python3",
			Args:                []string{"src/check_site.py"},
			StartToCloseTimeout: "30s",
			RetryPolicy:         workflowbundle.RetryPolicy{MaximumAttempts: 1},
		}},
	}
}

func writeAuthoredWorkflow(t *testing.T, ctx context.Context, workspaceRoot, workflowID string, manifest workflowbundle.Manifest, files []workflowbundle.File) string {
	t.Helper()
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := workflowbundle.WriteBundle(ctx, workflowPath, manifest, files); err != nil {
		t.Fatalf("write authored workflow: %v", err)
	}
	return workflowPath
}

func createAuthoredWorkflow(t *testing.T, ctx context.Context, executor *TemporalExecutor, workflowID string, manifest workflowbundle.Manifest, files []workflowbundle.File) Result {
	t.Helper()
	writeAuthoredWorkflow(t, ctx, executor.WorkspaceRoot, workflowID, manifest, files)
	result, err := executor.Create(ctx, CreateRequest{
		ProjectID:  "project-1",
		WorkflowID: workflowID,
		Prompt:     "publish authored test workflow",
	})
	if err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return result
}

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

	manifest := workflowbundle.Manifest{
		Name:        "daily etl",
		Description: "runs an etl workflow",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 2 * * *",
			OverlapPolicy: workflowbundle.OverlapPolicyBufferOne,
			CatchupWindow: "5m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Steps: []workflowbundle.Step{{
			ID:                  "download",
			Command:             "python",
			Args:                []string{"src/etl.py", "download"},
			StartToCloseTimeout: "10m",
			RetryPolicy: workflowbundle.RetryPolicy{
				MaximumAttempts: 3,
			},
		}},
	}
	writeAuthoredWorkflow(t, ctx, workspaceRoot, "daily-etl", manifest, []workflowbundle.File{{
		Path:    "src/etl.py",
		Content: "print('ok')\n",
	}})
	result, err := executor.Create(ctx, CreateRequest{
		ProjectID:   "project-1",
		WorkflowID:  "daily-etl",
		Prompt:      "publish authored test workflow",
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
	if options.Spec.TimeZoneName != "Asia/Tbilisi" {
		t.Fatalf("expected runtime timezone in Temporal schedule, got %#v", options.Spec)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "daily-etl")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifestData, err := os.ReadFile(filepath.Join(workflowPath, workflowbundle.ManifestFilename))
	if err != nil {
		t.Fatalf("read workflow manifest: %v", err)
	}
	if strings.Contains(string(manifestData), "time_zone_name") {
		t.Fatalf("workflow manifest should not persist time_zone_name:\n%s", string(manifestData))
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

	manifest := testWorkflowManifest("finance2049 availability")
	manifest.Steps[0].RetryPolicy.MaximumAttempts = 2
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", manifest, []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
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
		Prompt:        "publish authored test workflow update",
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
	manifest := testWorkflowManifest("daily etl")
	manifest.Steps[0].ID = "download"
	manifest.Steps[0].Command = "python"
	manifest.Steps[0].Args = []string{"src/etl.py"}
	manifest.Steps[0].StartToCloseTimeout = "10m"
	writeAuthoredWorkflow(t, ctx, executor.WorkspaceRoot, "daily-etl", manifest, []workflowbundle.File{{
		Path:    "src/etl.py",
		Content: "print('ok')\n",
	}})
	request := CreateRequest{
		ProjectID:  "project-1",
		WorkflowID: "daily-etl",
		Prompt:     "publish authored test workflow",
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

func TestPrepareCreateAuthoringReplacesUnregisteredLocalBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, _ := newWorkflowTestExecutor(t, workspaceRoot)
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

	plan, err := executor.PrepareAuthoring(ctx, AuthoringRequest{
		ProjectID:  "project-1",
		Operation:  OperationCreate,
		WorkflowID: "daily-etl",
		Prompt:     "create daily etl",
	})
	if err != nil {
		t.Fatalf("prepare workflow authoring: %v", err)
	}
	if plan.WorkflowID != "daily-etl" || plan.WorkflowPath != workflowPath || !plan.RemoveOnFailure {
		t.Fatalf("unexpected authoring plan: %#v", plan)
	}
	if _, err := os.Stat(filepath.Join(workflowPath, "src", "old.py")); !os.IsNotExist(err) {
		t.Fatalf("expected stale source file to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(workflowPath, ".git")); err != nil {
		t.Fatalf("expected prepared git repo: %v", err)
	}
}

func TestPrepareUpdateAuthoringCheckpointsDirtyWorkflowFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, _ := newWorkflowTestExecutor(t, workspaceRoot)
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "check_site.py"), []byte("print('dirty')\n"), 0o644); err != nil {
		t.Fatalf("dirty workflow source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "helper.py"), []byte("print('helper')\n"), 0o644); err != nil {
		t.Fatalf("write helper source: %v", err)
	}

	plan, err := executor.PrepareAuthoring(ctx, AuthoringRequest{
		ProjectID:  "project-1",
		Operation:  OperationUpdate,
		WorkflowID: "finance2049",
		Prompt:     "update workflow",
	})
	if err != nil {
		t.Fatalf("prepare workflow authoring: %v", err)
	}
	if plan.RestoreCommitHash == "" || plan.RestoreCommitHash == created.CommitHash {
		t.Fatalf("expected dirty checkpoint commit, created=%q plan=%#v", created.CommitHash, plan)
	}
	subject, err := gitOutput(ctx, workflowPath, "show", "-s", "--format=%s", plan.RestoreCommitHash)
	if err != nil {
		t.Fatalf("read checkpoint subject: %v", err)
	}
	if strings.TrimSpace(subject) != "Checkpoint local edits for workflow finance2049" {
		t.Fatalf("unexpected checkpoint subject %q", strings.TrimSpace(subject))
	}
	if status, err := gitOutput(ctx, workflowPath, "status", "--porcelain"); err != nil {
		t.Fatalf("git status: %v", err)
	} else if strings.TrimSpace(status) != "" {
		t.Fatalf("expected clean workflow repo after checkpoint, got %q", status)
	}
	source, err := gitOutput(ctx, workflowPath, "show", plan.RestoreCommitHash+":src/check_site.py")
	if err != nil {
		t.Fatalf("read checkpoint source: %v", err)
	}
	if source != "print('dirty')\n" {
		t.Fatalf("expected dirty source in checkpoint, got %q", source)
	}
	helper, err := gitOutput(ctx, workflowPath, "show", plan.RestoreCommitHash+":src/helper.py")
	if err != nil {
		t.Fatalf("read checkpoint helper: %v", err)
	}
	if helper != "print('helper')\n" {
		t.Fatalf("expected helper source in checkpoint, got %q", helper)
	}
}

func TestWorkflowUpdatePublishesAuthoredManifestChanges(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, _ := newWorkflowTestExecutor(t, workspaceRoot)
	manifest := testWorkflowManifest("finance2049 availability")
	createAuthoredWorkflow(t, ctx, executor, "finance2049", manifest, []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest.Description = ""
	manifest.NotificationPolicy.OnFailure = false
	if err := workflowbundle.WriteManifest(workflowPath, manifest); err != nil {
		t.Fatalf("write authored manifest update: %v", err)
	}
	updated, err := executor.Update(ctx, UpdateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Prompt:     "publish authored manifest update",
	})
	if err != nil {
		t.Fatalf("update workflow schedule: %v", err)
	}
	snapshot := t.TempDir()
	if err := workflowbundle.ArchiveCommit(ctx, workflowPath, updated.CommitHash, snapshot); err != nil {
		t.Fatalf("archive updated commit: %v", err)
	}
	archivedManifest, err := workflowbundle.LoadManifest(snapshot)
	if err != nil {
		t.Fatalf("load archived manifest: %v", err)
	}
	if archivedManifest.Description != "" || archivedManifest.NotificationPolicy.OnFailure {
		t.Fatalf("expected optional fields to be cleared, got %#v", archivedManifest)
	}
}

func TestWorkflowUpdateRollsBackTemporalScheduleWhenStorePersistFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	manifest := testWorkflowManifest("finance2049 availability")
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", manifest, []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest.Description = "updated description"
	if err := workflowbundle.WriteManifest(workflowPath, manifest); err != nil {
		t.Fatalf("write authored manifest update: %v", err)
	}

	executor.Store = &failingScheduledWorkflowStore{Store: store, upsertErr: errors.New("db unavailable")}
	_, err = executor.Update(ctx, UpdateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Prompt:     "publish authored manifest update",
	})
	if err == nil || !strings.Contains(err.Error(), "db unavailable") {
		t.Fatalf("expected store error, got %v", err)
	}
	if client.handle.updated != 2 {
		t.Fatalf("expected update plus rollback, got %d update calls", client.handle.updated)
	}
	if got := scheduleCommitHash(t, client.handle.currentSchedule); got != created.CommitHash {
		t.Fatalf("expected Temporal schedule rollback to commit %s, got %s", created.CommitHash, got)
	}
}

func TestWorkflowUpdateReturnsStoreReadErrorBeforeTemporalUpdate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	manifest := testWorkflowManifest("finance2049 availability")
	createAuthoredWorkflow(t, ctx, executor, "finance2049", manifest, []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest.Description = "updated description"
	if err := workflowbundle.WriteManifest(workflowPath, manifest); err != nil {
		t.Fatalf("write authored manifest update: %v", err)
	}

	executor.Store = &failingScheduledWorkflowStore{Store: store, getErr: errors.New("db read unavailable")}
	_, err = executor.Update(ctx, UpdateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Prompt:     "publish authored manifest update",
	})
	if err == nil || !strings.Contains(err.Error(), "db read unavailable") {
		t.Fatalf("expected store read error, got %v", err)
	}
	if client.handle.updated != 0 {
		t.Fatalf("Temporal update should not run after store read failure, got %d update calls", client.handle.updated)
	}
}

func TestWorkflowDeleteHardDeletesScheduleStoreAndFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}})
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
	createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}})
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
	createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}})
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
	if client.handle.lastTriggerOptions.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_UNSPECIFIED {
		t.Fatalf("expected trigger to use schedule overlap policy, got %s", client.handle.lastTriggerOptions.Overlap)
	}
}

func TestWorkflowTriggerPublishesDirtyWorkflowRepo(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('old')\n"}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "check_site.py"), []byte("print('dirty')\n"), 0o644); err != nil {
		t.Fatalf("dirty workflow source: %v", err)
	}

	result, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationTrigger, WorkflowID: "finance2049"})
	if err != nil {
		t.Fatalf("trigger workflow: %v", err)
	}
	if result.CommitHash == "" || result.CommitHash == created.CommitHash {
		t.Fatalf("expected trigger to publish dirty commit, created=%q result=%#v", created.CommitHash, result)
	}
	subject, err := gitOutput(ctx, workflowPath, "show", "-s", "--format=%s", result.CommitHash)
	if err != nil {
		t.Fatalf("read published subject: %v", err)
	}
	if strings.TrimSpace(subject) != "Apply manual edits to workflow finance2049" {
		t.Fatalf("unexpected published subject %q", strings.TrimSpace(subject))
	}
	if client.handle.triggered != 1 || client.handle.updated != 1 {
		t.Fatalf("expected schedule update and trigger, handle=%#v", client.handle)
	}
	stored, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "finance2049")
	if err != nil || !ok {
		t.Fatalf("stored workflow: ok=%t err=%v", ok, err)
	}
	if stored.CurrentCommitHash != result.CommitHash {
		t.Fatalf("expected stored commit %q, got %q", result.CommitHash, stored.CurrentCommitHash)
	}
	if status, err := gitOutput(ctx, workflowPath, "status", "--porcelain"); err != nil {
		t.Fatalf("git status: %v", err)
	} else if strings.TrimSpace(status) != "" {
		t.Fatalf("expected clean workflow repo after trigger publish, got %q", status)
	}
	source, err := gitOutput(ctx, workflowPath, "show", result.CommitHash+":src/check_site.py")
	if err != nil {
		t.Fatalf("read published source: %v", err)
	}
	if source != "print('dirty')\n" {
		t.Fatalf("expected dirty source to be published, got %q", source)
	}
}

func TestWorkflowTriggerPublishesUnpublishedCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, client := newWorkflowTestExecutor(t, workspaceRoot)
	createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('old')\n"}})
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowPath, "src", "check_site.py"), []byte("print('new')\n"), 0o644); err != nil {
		t.Fatalf("edit workflow source: %v", err)
	}
	manualCommit, err := workflowbundle.CommitBundle(ctx, workflowPath, "manual unpublished commit", nil)
	if err != nil {
		t.Fatalf("commit workflow source: %v", err)
	}

	result, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationTrigger, WorkflowID: "finance2049"})
	if err != nil {
		t.Fatalf("trigger workflow: %v", err)
	}
	if result.CommitHash != manualCommit {
		t.Fatalf("expected trigger to publish manual commit %q, got %#v", manualCommit, result)
	}
	if client.handle.triggered != 1 || client.handle.updated != 1 {
		t.Fatalf("expected schedule update and trigger, handle=%#v", client.handle)
	}
	stored, ok, err := store.GetScheduledWorkflow(ctx, "project-1", "finance2049")
	if err != nil || !ok {
		t.Fatalf("stored workflow: ok=%t err=%v", ok, err)
	}
	if stored.CurrentCommitHash != manualCommit {
		t.Fatalf("expected stored commit %q, got %q", manualCommit, stored.CurrentCommitHash)
	}
}

func TestWorkflowPauseReturnsStoreErrors(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, store, _ := newWorkflowTestExecutor(t, t.TempDir())
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if _, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationPause, WorkflowID: "finance2049"}); err == nil {
		t.Fatal("expected pause to return store error")
	}
}

type fakeScheduleClient struct {
	created []temporalclient.ScheduleOptions
	handle  *fakeScheduleHandle
	entries []*temporalclient.ScheduleListEntry
}

type failingScheduledWorkflowStore struct {
	*sqlitestore.Store
	getErr    error
	upsertErr error
}

func (s *failingScheduledWorkflowStore) GetScheduledWorkflow(ctx context.Context, projectID, workflowID string) (domain.ScheduledWorkflow, bool, error) {
	if s.getErr != nil {
		return domain.ScheduledWorkflow{}, false, s.getErr
	}
	return s.Store.GetScheduledWorkflow(ctx, projectID, workflowID)
}

func (s *failingScheduledWorkflowStore) UpsertScheduledWorkflow(ctx context.Context, item domain.ScheduledWorkflow) error {
	if s.upsertErr != nil {
		return s.upsertErr
	}
	return s.Store.UpsertScheduledWorkflow(ctx, item)
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
	handle.currentSchedule = scheduleFromOptions(options)
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

func scheduleFromOptions(options temporalclient.ScheduleOptions) temporalclient.Schedule {
	remaining := options.RemainingActions
	return temporalclient.Schedule{
		Action: options.Action,
		Spec:   &options.Spec,
		Policy: &temporalclient.SchedulePolicies{
			Overlap:        options.Overlap,
			CatchupWindow:  options.CatchupWindow,
			PauseOnFailure: options.PauseOnFailure,
		},
		State: &temporalclient.ScheduleState{
			Note:             options.Note,
			Paused:           options.Paused,
			LimitedActions:   remaining > 0,
			RemainingActions: remaining,
		},
	}
}

func scheduleCommitHash(t *testing.T, schedule temporalclient.Schedule) string {
	t.Helper()
	action, ok := schedule.Action.(*temporalclient.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("expected workflow action, got %T", schedule.Action)
	}
	if len(action.Args) != 1 {
		t.Fatalf("expected one workflow arg, got %#v", action.Args)
	}
	input, ok := action.Args[0].(workflowrun.Input)
	if !ok {
		t.Fatalf("expected workflowrun input, got %T", action.Args[0])
	}
	return input.CommitHash
}

type fakeScheduleHandle struct {
	id                 string
	deleted            int
	deleteErr          error
	updated            int
	triggered          int
	paused             int
	unpaused           int
	currentSchedule    temporalclient.Schedule
	lastUpdate         *temporalclient.ScheduleUpdate
	lastTriggerOptions temporalclient.ScheduleTriggerOptions
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
		update, err := options.DoUpdate(temporalclient.ScheduleUpdateInput{
			Description: temporalclient.ScheduleDescription{Schedule: f.currentSchedule},
		})
		f.lastUpdate = update
		if err != nil {
			return err
		}
		if update != nil && update.Schedule != nil {
			f.currentSchedule = *update.Schedule
		}
	}
	return nil
}

func (f *fakeScheduleHandle) Describe(context.Context) (*temporalclient.ScheduleDescription, error) {
	return &temporalclient.ScheduleDescription{}, nil
}

func (f *fakeScheduleHandle) Trigger(_ context.Context, options temporalclient.ScheduleTriggerOptions) error {
	f.triggered++
	f.lastTriggerOptions = options
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
