package workflowbundle

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	ManifestFilename = "workflow.yml"

	DefaultCatchupWindow = 10 * time.Minute

	OverlapPolicySkip           = "skip"
	OverlapPolicyBufferOne      = "buffer_one"
	OverlapPolicyBufferAll      = "buffer_all"
	OverlapPolicyCancelOther    = "cancel_other"
	OverlapPolicyTerminateOther = "terminate_other"
	OverlapPolicyAllowAll       = "allow_all"
)

var (
	ErrWorkflowIDRequired = errors.New("workflow_id is required")
	ErrStepIDRequired     = errors.New("step id is required")
	ErrStepCommandMissing = errors.New("step command is required")
)

type Manifest struct {
	Version            int                `json:"version" yaml:"version"`
	Name               string             `json:"name" yaml:"name"`
	Description        string             `json:"description,omitempty" yaml:"description,omitempty"`
	Schedule           Schedule           `json:"schedule" yaml:"schedule"`
	NotificationPolicy NotificationPolicy `json:"notification_policy" yaml:"notification_policy"`
	Env                []string           `json:"env,omitempty" yaml:"env,omitempty"`
	Steps              []Step             `json:"steps" yaml:"steps"`
}

type Schedule struct {
	Cron           string `json:"cron,omitempty" yaml:"cron,omitempty"`
	OneShotAt      string `json:"one_shot_at,omitempty" yaml:"one_shot_at,omitempty"`
	TimeZoneName   string `json:"time_zone_name,omitempty" yaml:"time_zone_name,omitempty"`
	OverlapPolicy  string `json:"overlap_policy,omitempty" yaml:"overlap_policy,omitempty"`
	CatchupWindow  string `json:"catchup_window,omitempty" yaml:"catchup_window,omitempty"`
	PauseOnFailure bool   `json:"pause_on_failure,omitempty" yaml:"pause_on_failure,omitempty"`
}

type NotificationPolicy struct {
	OnFailure bool `json:"on_failure" yaml:"on_failure"`
}

type Step struct {
	ID                     string      `json:"id" yaml:"id"`
	Command                string      `json:"command" yaml:"command"`
	Args                   []string    `json:"args,omitempty" yaml:"args,omitempty"`
	StartToCloseTimeout    string      `json:"start_to_close_timeout,omitempty" yaml:"start_to_close_timeout,omitempty"`
	ScheduleToCloseTimeout string      `json:"schedule_to_close_timeout,omitempty" yaml:"schedule_to_close_timeout,omitempty"`
	RetryPolicy            RetryPolicy `json:"retry_policy,omitempty" yaml:"retry_policy,omitempty"`
}

type RetryPolicy struct {
	InitialInterval        string   `json:"initial_interval,omitempty" yaml:"initial_interval,omitempty"`
	BackoffCoefficient     float64  `json:"backoff_coefficient,omitempty" yaml:"backoff_coefficient,omitempty"`
	MaximumInterval        string   `json:"maximum_interval,omitempty" yaml:"maximum_interval,omitempty"`
	MaximumAttempts        int32    `json:"maximum_attempts,omitempty" yaml:"maximum_attempts,omitempty"`
	NonRetryableErrorTypes []string `json:"non_retryable_error_types,omitempty" yaml:"non_retryable_error_types,omitempty"`
}

type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

func WorkflowDir(workspaceRoot, workflowID string) (string, error) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	id, err := NormalizeWorkflowID(workflowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".opencto", "workflows", id), nil
}

func WorkflowRunsDir(workspaceRoot, workflowID string) (string, error) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	id, err := NormalizeWorkflowID(workflowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, ".opencto", "workflow-runs", id), nil
}

func WorkflowRunDir(workspaceRoot, workflowID, runID string) (string, error) {
	base, err := WorkflowRunsDir(workspaceRoot, workflowID)
	if err != nil {
		return "", err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "", fmt.Errorf("run_id is required")
	}
	return filepath.Join(base, runID), nil
}

func NormalizeWorkflowID(value string) (string, error) {
	slug := slugify(value)
	if slug == "" {
		return "", ErrWorkflowIDRequired
	}
	return slug, nil
}

func WriteBundle(ctx context.Context, dir string, manifest Manifest, files []File) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("workflow directory is required")
	}
	for _, file := range files {
		if _, err := cleanBundleFilePath(file.Path); err != nil {
			return err
		}
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return err
	}
	if err := ensureGitIgnore(dir); err != nil {
		return err
	}
	if err := WriteManifest(dir, manifest); err != nil {
		return err
	}
	for _, file := range files {
		if err := WriteBundleFile(dir, file); err != nil {
			return err
		}
	}
	return EnsureGitRepo(ctx, dir)
}

func WriteManifest(dir string, manifest Manifest) error {
	data, err := yaml.Marshal(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ManifestFilename), data, 0o644)
}

func LoadManifest(dir string) (Manifest, error) {
	data, err := os.ReadFile(filepath.Join(dir, ManifestFilename))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func WriteBundleFile(dir string, file File) error {
	rel, err := cleanBundleFilePath(file.Path)
	if err != nil {
		return err
	}
	target := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if file.Executable {
		mode = 0o755
	}
	return os.WriteFile(target, []byte(file.Content), mode)
}

func EnsureGitRepo(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if err := runGit(ctx, dir, "init"); err != nil {
		return err
	}
	return runGit(ctx, dir, "branch", "-M", "main")
}

func CommitAll(ctx context.Context, dir, message string) (string, error) {
	if err := EnsureGitRepo(ctx, dir); err != nil {
		return "", err
	}
	if err := runGit(ctx, dir, "add", "--all"); err != nil {
		return "", err
	}
	if strings.TrimSpace(message) == "" {
		message = "Update workflow"
	}
	status, err := gitOutput(ctx, dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) != "" {
		if err := runGit(ctx, dir, "-c", "user.name=OpenCTO", "-c", "user.email=opencto@local", "-c", "commit.gpgsign=false", "commit", "-m", message); err != nil {
			return "", err
		}
	}
	hash, err := gitOutput(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	hash = strings.TrimSpace(hash)
	if !fullCommitHash(hash) {
		return "", fmt.Errorf("git returned invalid commit hash %q", hash)
	}
	return hash, nil
}

func ArchiveCommit(ctx context.Context, repoDir, commitHash, targetDir string) error {
	commitHash = strings.TrimSpace(commitHash)
	if !fullCommitHash(commitHash) {
		return fmt.Errorf("commit_hash must be a full git SHA")
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", commitHash)
	output, err := cmd.Output()
	if err != nil {
		return gitCommandError(cmd, err)
	}
	reader := tar.NewReader(bytes.NewReader(output))
	for {
		header, err := reader.Next()
		switch {
		case errors.Is(err, io.EOF):
			return nil
		case err != nil:
			return err
		}
		if header == nil {
			continue
		}
		rel, err := cleanArchivePath(header.Name)
		if err != nil {
			return err
		}
		target := filepath.Join(targetDir, rel)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, reader)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func ValidateManifest(manifest Manifest) error {
	if manifest.Version == 0 {
		manifest.Version = 1
	}
	if manifest.Version != 1 {
		return fmt.Errorf("unsupported workflow manifest version %d", manifest.Version)
	}
	cron := strings.TrimSpace(manifest.Schedule.Cron)
	oneShot := strings.TrimSpace(manifest.Schedule.OneShotAt)
	switch {
	case cron == "" && oneShot == "":
		return fmt.Errorf("schedule.cron or schedule.one_shot_at is required")
	case cron != "" && oneShot != "":
		return fmt.Errorf("schedule.cron and schedule.one_shot_at cannot both be set")
	}
	if oneShot != "" {
		if _, err := time.Parse(time.RFC3339, oneShot); err != nil {
			return fmt.Errorf("parse schedule.one_shot_at as RFC3339: %w", err)
		}
	}
	if _, err := ParseCatchupWindow(manifest.Schedule.CatchupWindow); err != nil {
		return err
	}
	if _, err := NormalizeOverlapPolicy(manifest.Schedule.OverlapPolicy); err != nil {
		return err
	}
	if len(manifest.Steps) == 0 {
		return fmt.Errorf("at least one step is required")
	}
	seen := map[string]bool{}
	for _, step := range manifest.Steps {
		id, err := NormalizeStepID(step.ID)
		if err != nil {
			return err
		}
		if seen[id] {
			return fmt.Errorf("duplicate step id %q", id)
		}
		seen[id] = true
		if strings.TrimSpace(step.Command) == "" {
			return fmt.Errorf("%w: %s", ErrStepCommandMissing, id)
		}
		if _, err := ParseRequiredDuration("start_to_close_timeout", step.StartToCloseTimeout); err != nil {
			return fmt.Errorf("step %q: %w", id, err)
		}
		if _, err := ParseOptionalDuration("schedule_to_close_timeout", step.ScheduleToCloseTimeout); err != nil {
			return fmt.Errorf("step %q: %w", id, err)
		}
		if _, err := ParseOptionalDuration("retry_policy.initial_interval", step.RetryPolicy.InitialInterval); err != nil {
			return fmt.Errorf("step %q: %w", id, err)
		}
		if _, err := ParseOptionalDuration("retry_policy.maximum_interval", step.RetryPolicy.MaximumInterval); err != nil {
			return fmt.Errorf("step %q: %w", id, err)
		}
	}
	return nil
}

func NormalizeStepID(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ErrStepIDRequired
	}
	if !safeIDPattern.MatchString(value) {
		return "", fmt.Errorf("step id %q must match %s", value, safeIDPattern.String())
	}
	return value, nil
}

func NormalizeOverlapPolicy(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", OverlapPolicySkip:
		return OverlapPolicySkip, nil
	case OverlapPolicyBufferOne:
		return OverlapPolicyBufferOne, nil
	case OverlapPolicyBufferAll:
		return OverlapPolicyBufferAll, nil
	case OverlapPolicyCancelOther:
		return OverlapPolicyCancelOther, nil
	case OverlapPolicyTerminateOther:
		return OverlapPolicyTerminateOther, nil
	case OverlapPolicyAllowAll:
		return OverlapPolicyAllowAll, nil
	default:
		return "", fmt.Errorf("unsupported overlap_policy %q", value)
	}
}

func ParseCatchupWindow(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultCatchupWindow, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse schedule.catchup_window: %w", err)
	}
	return duration, nil
}

func ParseRequiredDuration(field, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s is required", field)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be greater than 0", field)
	}
	return duration, nil
}

func ParseOptionalDuration(field, value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", field, err)
	}
	if duration < 0 {
		return 0, fmt.Errorf("%s must not be negative", field)
	}
	return duration, nil
}

func StepHash(workflowID, commitHash, runID, stepID string) string {
	sum := sha1.Sum([]byte(strings.Join([]string{workflowID, commitHash, runID, stepID}, "\x00")))
	return hex.EncodeToString(sum[:])[:16]
}

func ensureGitIgnore(dir string) error {
	path := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	const content = "/tmp/\n/artifacts/\n/runs/\n*.log\n.DS_Store\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func cleanBundleFilePath(value string) (string, error) {
	rel := filepath.Clean(strings.TrimSpace(value))
	if rel == "." || rel == "" {
		return "", fmt.Errorf("file path is required")
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || rel == ".." || strings.Contains(rel, "/../") {
		return "", fmt.Errorf("file path %q escapes workflow bundle", value)
	}
	if rel == ".gitignore" || strings.HasPrefix(rel, "src/") {
		return filepath.FromSlash(rel), nil
	}
	return "", fmt.Errorf("file path %q must be .gitignore or under src/", value)
}

func cleanArchivePath(value string) (string, error) {
	rel := filepath.Clean(strings.TrimSpace(value))
	if rel == "." || rel == "" {
		return "", fmt.Errorf("archive path is required")
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || rel == ".." || strings.Contains(rel, "/../") {
		return "", fmt.Errorf("archive path %q escapes target", value)
	}
	return filepath.FromSlash(rel), nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", gitCommandError(cmd, err)
	}
	return string(output), nil
}

func gitCommandError(cmd *exec.Cmd, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
}

func fullCommitHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

var (
	slugPattern   = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)
	safeIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
)

func slugify(value string) string {
	value = strings.TrimSpace(value)
	value = slugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	return strings.ToLower(value)
}
