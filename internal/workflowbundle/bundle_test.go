package workflowbundle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowDirUsesProvidedWorkspaceRoot(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), ".opencto")
	got, err := WorkflowDir(root, "daily")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	want := filepath.Join(root, "workflows", "daily")
	if got != want {
		t.Fatalf("expected workflow dir %q, got %q", want, got)
	}
}

func TestWorkflowDirUsesWorkflowsDirectoryUnderWorkspaceRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	got, err := WorkflowDir(root, "daily")
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	want := filepath.Join(root, "workflows", "daily")
	if got != want {
		t.Fatalf("expected workflow dir %q, got %q", want, got)
	}
}

func TestWriteBundleRejectsManifestFile(t *testing.T) {
	t.Parallel()

	err := WriteBundle(context.Background(), t.TempDir(), testManifest(), []File{{
		Path:    ManifestFilename,
		Content: "version: 1\n",
	}})
	if err == nil {
		t.Fatal("expected workflow.yml file write to be rejected")
	}
	if !strings.Contains(err.Error(), "reserved for OpenCTO workflow runtime or metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteBundleAllowsWorkflowOwnedFilesOutsideSrc(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := WriteBundle(context.Background(), dir, testManifest(), []File{{
		Path:    "README.md",
		Content: "workflow notes\n",
	}}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "README.md"))
	if err != nil {
		t.Fatalf("read workflow-owned file: %v", err)
	}
	if string(data) != "workflow notes\n" {
		t.Fatalf("unexpected workflow-owned file content: %q", string(data))
	}
}

func TestWriteBundleRejectsIgnoredNestedPath(t *testing.T) {
	t.Parallel()

	err := WriteBundle(context.Background(), t.TempDir(), testManifest(), []File{{
		Path:    "src/node_modules/helper.js",
		Content: "console.log('ignored')\n",
	}})
	if err == nil {
		t.Fatal("expected nested ignored path to be rejected")
	}
	if !strings.Contains(err.Error(), "reserved for OpenCTO workflow runtime or metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateManifestRejectsNonPositiveRequiredDuration(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0s", "-1s"} {
		manifest := testManifest()
		manifest.Steps[0].StartToCloseTimeout = value
		if err := ValidateManifest(manifest); err == nil {
			t.Fatalf("expected start_to_close_timeout %q to be rejected", value)
		}
	}
}

func TestValidateManifestRejectsExternalCommandWithoutArgs(t *testing.T) {
	t.Parallel()

	manifest := testManifest()
	manifest.Steps[0].Command = "go"
	manifest.Steps[0].Args = []string{}
	err := ValidateManifest(manifest)
	if err == nil {
		t.Fatal("expected external command without args to be rejected")
	}
	if !strings.Contains(err.Error(), ErrStepArgsMissing.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateManifestRejectsRepeatedExecutableArgument(t *testing.T) {
	t.Parallel()

	manifest := testManifest()
	manifest.Steps[0].Command = "python3"
	manifest.Steps[0].Args = []string{"python3", "src/check_site.py"}
	err := ValidateManifest(manifest)
	if err == nil {
		t.Fatal("expected repeated executable argument to be rejected")
	}
	if !strings.Contains(err.Error(), "args[0] must not repeat command") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateManifestRejectsUnsupportedNonRetryableErrorType(t *testing.T) {
	t.Parallel()

	manifest := testManifest()
	manifest.Steps[0].RetryPolicy.NonRetryableErrorTypes = []string{"4xx"}
	err := ValidateManifest(manifest)
	if err == nil {
		t.Fatal("expected unsupported non-retryable error type to fail")
	}
	if !strings.Contains(err.Error(), StepFailureErrorType) {
		t.Fatalf("expected supported error type in error, got %v", err)
	}
}

func TestValidateManifestAcceptsWorkflowStepFailedNonRetryableErrorType(t *testing.T) {
	t.Parallel()

	manifest := testManifest()
	manifest.Steps[0].RetryPolicy.NonRetryableErrorTypes = []string{StepFailureErrorType}
	if err := ValidateManifest(manifest); err != nil {
		t.Fatalf("expected supported non-retryable error type to pass: %v", err)
	}
}

func TestLoadManifestRejectsUnknownYAMLFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`name: test workflow
description: ""
schedule:
  cron: "0 9 * * *"
  one_shot_at: ""
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
steps:
  - id: step
    command: echo
    args: ["ok"]
    start_to_close_timeout: 1m
    schedule_to_close_timeout: ""
    retry_policy:
      initial_interval: ""
      backoff_coefficient: 0
      maximum_interval: ""
      maximum_attempts: 1
      non_retryable_error_types: []
unknown: true
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if _, err := LoadManifest(dir); err == nil {
		t.Fatal("expected unknown yaml field to be rejected")
	}
}

func TestLoadManifestDoesNotRequireVersion(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`name: test workflow
description: ""
schedule:
  cron: "0 9 * * *"
  one_shot_at: ""
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
steps:
  - id: step
    command: echo
    args: ["ok"]
    start_to_close_timeout: 1m
    schedule_to_close_timeout: ""
    retry_policy:
      initial_interval: ""
      backoff_coefficient: 0
      maximum_interval: ""
      maximum_attempts: 1
      non_retryable_error_types: []
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load manifest without version: %v", err)
	}
	if manifest.Name != "test workflow" {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
}

func TestLoadManifestDefaultsOptionalStepFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`name: test workflow
description: ""
schedule:
  cron: "0 9 * * *"
  one_shot_at: ""
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
steps:
  - id: step
    command: src/check.sh
    start_to_close_timeout: 1m
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("load manifest with optional step fields omitted: %v", err)
	}
	step := manifest.Steps[0]
	if step.Args == nil || len(step.Args) != 0 {
		t.Fatalf("expected omitted args to default to empty slice, got %#v", step.Args)
	}
	if step.ScheduleToCloseTimeout != "" {
		t.Fatalf("expected empty schedule_to_close_timeout, got %q", step.ScheduleToCloseTimeout)
	}
	if step.RetryPolicy.NonRetryableErrorTypes == nil || len(step.RetryPolicy.NonRetryableErrorTypes) != 0 {
		t.Fatalf("expected omitted non_retryable_error_types to default to empty slice, got %#v", step.RetryPolicy.NonRetryableErrorTypes)
	}
}

func TestLoadManifestRejectsExternalCommandWithOmittedArgs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`name: test workflow
description: ""
schedule:
  cron: "0 9 * * *"
  one_shot_at: ""
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy:
  on_failure: true
steps:
  - id: step
    command: python3
    start_to_close_timeout: 1m
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected external command with omitted args to fail")
	}
	if !strings.Contains(err.Error(), ErrStepArgsMissing.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadManifestRejectsMissingRequiredYAMLFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	data := []byte(`name: test workflow
description: ""
schedule:
  cron: "0 9 * * *"
  one_shot_at: ""
  overlap_policy: skip
  catchup_window: 10m
  pause_on_failure: false
notification_policy: {}
steps:
  - id: step
    command: echo
    args: ["ok"]
    start_to_close_timeout: 1m
    schedule_to_close_timeout: ""
    retry_policy:
      initial_interval: ""
      backoff_coefficient: 0
      maximum_interval: ""
      maximum_attempts: 1
      non_retryable_error_types: []
`)
	if err := os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	_, err := LoadManifest(dir)
	if err == nil {
		t.Fatal("expected missing required yaml field to be rejected")
	}
	if !strings.Contains(err.Error(), "notification_policy.on_failure") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteBundleOmitsEmptyOptionalStepFields(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	manifest := testManifest()
	manifest.Steps = []Step{{
		ID:                  "step",
		Command:             "src/check.sh",
		StartToCloseTimeout: "1m",
	}}
	files := []File{{Path: "src/check.sh", Content: "echo ok\n", Executable: true}}
	if err := WriteBundle(ctx, dir, manifest, files); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ManifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	text := string(data)
	for _, omitted := range []string{"args:", "schedule_to_close_timeout:", "retry_policy:"} {
		if strings.Contains(text, omitted) {
			t.Fatalf("expected %s to be omitted from manifest:\n%s", omitted, text)
		}
	}
}

func TestCommitBundleRespectsGitIgnoreForGeneratedFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	files := []File{{
		Path:    "src/app.sh",
		Content: "echo ok\n",
	}}
	if err := WriteBundle(ctx, dir, testManifest(), files); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if _, err := CommitBundle(ctx, dir, "initial", files); err != nil {
		t.Fatalf("commit bundle: %v", err)
	}
	unrequested := filepath.Join(dir, "src", "node_modules", "dependency.txt")
	if err := os.MkdirAll(filepath.Dir(unrequested), 0o755); err != nil {
		t.Fatalf("mkdir unrequested: %v", err)
	}
	if err := os.WriteFile(unrequested, []byte("large generated dependency\n"), 0o644); err != nil {
		t.Fatalf("write unrequested: %v", err)
	}

	manifest := testManifest()
	manifest.Description = "updated"
	if err := WriteBundle(ctx, dir, manifest, nil); err != nil {
		t.Fatalf("write update: %v", err)
	}
	hash, err := CommitBundle(ctx, dir, "update manifest", nil)
	if err != nil {
		t.Fatalf("commit update: %v", err)
	}
	snapshot := t.TempDir()
	if err := ArchiveCommit(ctx, dir, hash, snapshot); err != nil {
		t.Fatalf("archive commit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(snapshot, "src", "node_modules", "dependency.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected unrequested file to be absent from archived snapshot, stat err=%v", err)
	}
}

func TestCommitBundleTracksExistingWorkflowFileUpdates(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	files := []File{{
		Path:    "src/app.sh",
		Content: "echo old\n",
	}}
	if err := WriteBundle(ctx, dir, testManifest(), files); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if _, err := CommitBundle(ctx, dir, "initial", files); err != nil {
		t.Fatalf("commit bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "app.sh"), []byte("echo new\n"), 0o644); err != nil {
		t.Fatalf("update tracked source: %v", err)
	}
	hash, err := CommitBundle(ctx, dir, "update", nil)
	if err != nil {
		t.Fatalf("commit update: %v", err)
	}
	snapshot := t.TempDir()
	if err := ArchiveCommit(ctx, dir, hash, snapshot); err != nil {
		t.Fatalf("archive commit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "src", "app.sh"))
	if err != nil {
		t.Fatalf("read archived source: %v", err)
	}
	if string(data) != "echo new\n" {
		t.Fatalf("expected tracked source update in archive, got %q", string(data))
	}
}

func TestCommitBundleTracksNewWorkflowFiles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	files := []File{{
		Path:    "src/app.sh",
		Content: "echo old\n",
	}}
	if err := WriteBundle(ctx, dir, testManifest(), files); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	if _, err := CommitBundle(ctx, dir, "initial", files); err != nil {
		t.Fatalf("commit bundle: %v", err)
	}
	helperPath := filepath.Join(dir, "lib", "helper.sh")
	if err := os.MkdirAll(filepath.Dir(helperPath), 0o755); err != nil {
		t.Fatalf("mkdir new workflow file: %v", err)
	}
	if err := os.WriteFile(helperPath, []byte("echo helper\n"), 0o644); err != nil {
		t.Fatalf("write new workflow file: %v", err)
	}
	hash, err := CommitBundle(ctx, dir, "add helper", nil)
	if err != nil {
		t.Fatalf("commit new source: %v", err)
	}
	snapshot := t.TempDir()
	if err := ArchiveCommit(ctx, dir, hash, snapshot); err != nil {
		t.Fatalf("archive commit: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "lib", "helper.sh"))
	if err != nil {
		t.Fatalf("read archived helper: %v", err)
	}
	if string(data) != "echo helper\n" {
		t.Fatalf("expected new source file in archive, got %q", string(data))
	}
}

func TestParseOptionalDurationRejectsNegativeDuration(t *testing.T) {
	t.Parallel()

	if _, err := ParseOptionalDuration("schedule_to_close_timeout", "-1s"); err == nil {
		t.Fatal("expected negative optional duration to be rejected")
	}
}

func testManifest() Manifest {
	return Manifest{
		Name: "test workflow",
		Schedule: Schedule{
			Cron:          "0 9 * * *",
			OverlapPolicy: OverlapPolicySkip,
			CatchupWindow: "10m",
		},
		NotificationPolicy: NotificationPolicy{OnFailure: true},
		Steps: []Step{{
			ID:                  "step",
			Command:             "echo",
			Args:                []string{"ok"},
			StartToCloseTimeout: "1m",
			RetryPolicy: RetryPolicy{
				NonRetryableErrorTypes: []string{},
			},
		}},
	}
}
