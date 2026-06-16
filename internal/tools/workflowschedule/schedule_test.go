package workflowschedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	temporalclient "go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/timestamppb"

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
		Client:         client,
		WorkflowLister: client,
		Store:          store,
		TaskQueue:      "opencto",
		WorkspaceRoot:  workspaceRoot,
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
		SourceEvent: domain.Event{ID: "event-1", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1"},
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
	createdManifest, err := workflowbundle.LoadManifest(workflowPath)
	if err != nil {
		t.Fatalf("load created manifest: %v", err)
	}
	if createdManifest.NotificationPolicy.ChannelType != string(domain.ChannelTypeDiscord) ||
		createdManifest.NotificationPolicy.ChannelID != "channel-1" {
		t.Fatalf("expected notification target to be backfilled, got %#v", createdManifest.NotificationPolicy)
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
	if input.WorkflowID != "daily-etl" || input.CommitHash != result.CommitHash ||
		input.SourceEvent.ChannelType != domain.ChannelTypeDiscord ||
		input.SourceEvent.ChannelID != "channel-1" ||
		input.SourceEvent.ThreadID != "" {
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
		Client:         client,
		WorkflowLister: client,
		Store:          store,
		TaskQueue:      "opencto",
		WorkspaceRoot:  workspaceRoot,
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

func TestWorkflowUpdatePreservesOriginalNotificationChannel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, _, client := newWorkflowTestExecutor(t, workspaceRoot)
	manifest := testWorkflowManifest("finance2049 availability")
	writeAuthoredWorkflow(t, ctx, workspaceRoot, "finance2049", manifest, []workflowbundle.File{{
		Path:    "src/check_site.py",
		Content: "print('old')\n",
	}})
	if _, err := executor.Create(ctx, CreateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Prompt:     "publish authored test workflow",
		SourceEvent: domain.Event{
			ID:          "event-create",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-original",
		},
	}); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	workflowPath, err := workflowbundle.WorkflowDir(workspaceRoot, "finance2049")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest.Description = "updated description"
	if err := workflowbundle.WriteManifest(workflowPath, manifest); err != nil {
		t.Fatalf("write authored manifest update: %v", err)
	}

	if _, err := executor.Update(ctx, UpdateRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance2049",
		Prompt:     "publish authored manifest update",
		SourceEvent: domain.Event{
			ID:          "event-update",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-update",
		},
	}); err != nil {
		t.Fatalf("update workflow: %v", err)
	}
	action, ok := client.handle.lastUpdate.Schedule.Action.(*temporalclient.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("expected workflow action, got %T", client.handle.lastUpdate.Schedule.Action)
	}
	input, ok := action.Args[0].(workflowrun.Input)
	if !ok {
		t.Fatalf("expected workflow input, got %T", action.Args[0])
	}
	if input.SourceEvent.ChannelID != "channel-original" {
		t.Fatalf("expected update to preserve original channel, got %#v", input.SourceEvent)
	}
	updatedManifest, err := workflowbundle.LoadManifest(workflowPath)
	if err != nil {
		t.Fatalf("load updated manifest: %v", err)
	}
	if updatedManifest.NotificationPolicy.ChannelID != "channel-original" {
		t.Fatalf("expected manifest target to remain original, got %#v", updatedManifest.NotificationPolicy)
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

func TestPrepareUpdateAuthoringMissingWorkflowIsNonRetryable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, _, _ := newWorkflowTestExecutor(t, t.TempDir())
	_, err := executor.PrepareAuthoring(ctx, AuthoringRequest{
		ProjectID:  "project-1",
		Operation:  OperationUpdate,
		WorkflowID: "missing-workflow",
		Prompt:     "update workflow",
	})
	if err == nil {
		t.Fatal("expected missing workflow error")
	}
	if !errors.Is(err, ErrWorkflowNotFound) {
		t.Fatalf("expected ErrWorkflowNotFound, got %v", err)
	}
	if !IsNonRetryableAuthoringError(err) {
		t.Fatalf("expected non-retryable authoring error, got %v", err)
	}
	if err.Error() != `workflow "missing-workflow" not found` {
		t.Fatalf("unexpected error message %q", err.Error())
	}
}

func TestPrepareUpdateAuthoringMissingSourceIsNonRetryable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	executor, store, _ := newWorkflowTestExecutor(t, workspaceRoot)
	if err := store.UpsertScheduledWorkflow(ctx, domain.ScheduledWorkflow{
		ID:                 "orphan-workflow",
		ProjectID:          "project-1",
		Name:               "orphan workflow",
		Status:             domain.ScheduledWorkflowStatusActive,
		CurrentCommitHash:  "abc123",
		TemporalScheduleID: "project-1:workflow:orphan-workflow",
		WorkflowPath:       filepath.Join(workspaceRoot, "workflows", "orphan-workflow"),
	}); err != nil {
		t.Fatalf("upsert scheduled workflow: %v", err)
	}

	_, err := executor.PrepareAuthoring(ctx, AuthoringRequest{
		ProjectID:  "project-1",
		Operation:  OperationUpdate,
		WorkflowID: "orphan-workflow",
		Prompt:     "update workflow",
	})
	if err == nil {
		t.Fatal("expected missing source error")
	}
	if !errors.Is(err, ErrWorkflowSourceNotFound) {
		t.Fatalf("expected ErrWorkflowSourceNotFound, got %v", err)
	}
	if !IsNonRetryableAuthoringError(err) {
		t.Fatalf("expected non-retryable authoring error, got %v", err)
	}
	if err.Error() != `workflow "orphan-workflow" source directory not found` {
		t.Fatalf("unexpected error message %q", err.Error())
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
	logsPath := filepath.Join(workspaceRoot, ".state", "workflow-logs", "finance2049")
	if err := os.MkdirAll(filepath.Join(logsPath, "run-1", "download", "attempt-1"), 0o755); err != nil {
		t.Fatalf("mkdir workflow logs: %v", err)
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
	if _, err := os.Stat(logsPath); !os.IsNotExist(err) {
		t.Fatalf("expected workflow logs path to be deleted, stat err=%v", err)
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

func TestWorkflowOperationListUsesTemporalExecutions(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, _, client := newWorkflowTestExecutor(t, t.TempDir())
	client.workflowExecutions = []*workflowpb.WorkflowExecutionInfo{{
		Execution: &commonpb.WorkflowExecution{
			WorkflowId: "default:project",
			RunId:      "run-project",
		},
		Type:      &commonpb.WorkflowType{Name: "ProjectWorkflow"},
		Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		TaskQueue: "opencto",
		StartTime: timestamppb.New(time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)),
	}}

	result, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationList})
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(result.Workflows) != 1 {
		t.Fatalf("expected one Temporal workflow, got %#v", result.Workflows)
	}
	entry := result.Workflows[0]
	if entry.ID != "default:project" || entry.TemporalWorkflowID != "default:project" || entry.TemporalRunID != "run-project" {
		t.Fatalf("unexpected workflow execution ids: %#v", entry)
	}
	if entry.WorkflowType != "ProjectWorkflow" || entry.ExecutionStatus != "running" || entry.Status != "running" {
		t.Fatalf("unexpected workflow execution metadata: %#v", entry)
	}
	if client.lastListWorkflowRequest == nil || client.lastListWorkflowRequest.Query != "CloseTime is null" {
		t.Fatalf("expected open-workflow Temporal query, got %#v", client.lastListWorkflowRequest)
	}
	if !strings.Contains(result.Observation(), "default:project [ProjectWorkflow] (running)") {
		t.Fatalf("expected Temporal workflow in observation, got %q", result.Observation())
	}
}

func TestWorkflowOperationListEnrichesTemporalExecutionsFromScheduledWorkflowStore(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, _, client := newWorkflowTestExecutor(t, t.TempDir())
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}})
	client.workflowExecutions = []*workflowpb.WorkflowExecutionInfo{{
		Execution: &commonpb.WorkflowExecution{
			WorkflowId: created.ScheduleID + ":run",
			RunId:      "run-finance",
		},
		Type:      &commonpb.WorkflowType{Name: workflowrun.WorkflowName},
		Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		TaskQueue: "opencto",
		StartTime: timestamppb.New(time.Date(2026, 6, 14, 10, 0, 0, 0, time.UTC)),
	}}

	result, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationList, IncludeCompleted: true})
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(result.Workflows) != 1 {
		t.Fatalf("expected one Temporal workflow, got %#v", result.Workflows)
	}
	entry := result.Workflows[0]
	if entry.ID != "finance2049" || entry.Name != "finance2049 availability" || entry.TemporalScheduleID != created.ScheduleID {
		t.Fatalf("expected scheduled workflow metadata, got %#v", entry)
	}
	if entry.Status != string(domain.ScheduledWorkflowStatusActive) || entry.ExecutionStatus != "running" {
		t.Fatalf("expected registered and execution statuses, got %#v", entry)
	}
	if client.lastListWorkflowRequest == nil || client.lastListWorkflowRequest.Query != "" {
		t.Fatalf("expected include_completed list to omit open-only query, got %#v", client.lastListWorkflowRequest)
	}
	expectedObservation := "finance2049 availability [" + workflowrun.WorkflowName + "] temporal_workflow_id=" + created.ScheduleID + ":run (active/running)"
	if !strings.Contains(result.Observation(), expectedObservation) {
		t.Fatalf("expected enriched workflow in observation, got %q", result.Observation())
	}
}

func TestWorkflowOperationListIncludesIdleScheduledWorkflowRecords(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	executor, _, _ := newWorkflowTestExecutor(t, t.TempDir())
	created := createAuthoredWorkflow(t, ctx, executor, "finance2049", testWorkflowManifest("finance2049 availability"), []workflowbundle.File{{Path: "src/check_site.py", Content: "print('ok')\n"}})

	result, err := executor.Operation(ctx, OperationRequest{ProjectID: "project-1", Operation: OperationList})
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(result.Workflows) != 1 {
		t.Fatalf("expected one idle scheduled workflow, got %#v", result.Workflows)
	}
	entry := result.Workflows[0]
	if entry.ID != "finance2049" || entry.Name != "finance2049 availability" || entry.TemporalScheduleID != created.ScheduleID {
		t.Fatalf("expected scheduled workflow metadata, got %#v", entry)
	}
	if entry.TemporalWorkflowID != "" || entry.TemporalRunID != "" || entry.WorkflowType != "" {
		t.Fatalf("expected no Temporal execution metadata for idle scheduled workflow, got %#v", entry)
	}
	if entry.Status != string(domain.ScheduledWorkflowStatusActive) || entry.ExecutionStatus != "idle" {
		t.Fatalf("expected active/idle statuses, got %#v", entry)
	}
	if !strings.Contains(result.Observation(), "finance2049 availability (active/idle)") {
		t.Fatalf("expected idle scheduled workflow in observation, got %q", result.Observation())
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
	created                 []temporalclient.ScheduleOptions
	handle                  *fakeScheduleHandle
	entries                 []*temporalclient.ScheduleListEntry
	workflowExecutions      []*workflowpb.WorkflowExecutionInfo
	lastListWorkflowRequest *workflowservice.ListWorkflowExecutionsRequest
	listWorkflowErr         error
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
		Client:         client,
		WorkflowLister: client,
		Store:          store,
		TaskQueue:      "opencto",
		WorkspaceRoot:  workspaceRoot,
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

func (f *fakeScheduleClient) ListWorkflow(_ context.Context, request *workflowservice.ListWorkflowExecutionsRequest) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	f.lastListWorkflowRequest = request
	if f.listWorkflowErr != nil {
		return nil, f.listWorkflowErr
	}
	executions := f.workflowExecutions
	if request != nil && request.PageSize > 0 && int(request.PageSize) < len(executions) {
		executions = executions[:int(request.PageSize)]
	}
	return &workflowservice.ListWorkflowExecutionsResponse{Executions: executions}, nil
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
