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
	"path"
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
	ErrWorkflowIDRequired   = errors.New("workflow_id is required")
	ErrWorkflowNameRequired = errors.New("workflow name is required")
	ErrStepIDRequired       = errors.New("step id is required")
	ErrStepCommandMissing   = errors.New("step command is required")
	ErrStepArgsMissing      = errors.New("step args are required for external commands")
)

type Manifest struct {
	Name               string             `json:"name" yaml:"name"`
	Description        string             `json:"description" yaml:"description"`
	Schedule           Schedule           `json:"schedule" yaml:"schedule"`
	NotificationPolicy NotificationPolicy `json:"notification_policy" yaml:"notification_policy"`
	Env                []string           `json:"env" yaml:"env"`
	Steps              []Step             `json:"steps" yaml:"steps"`
}

type Schedule struct {
	Cron           string `json:"cron" yaml:"cron"`
	OneShotAt      string `json:"one_shot_at" yaml:"one_shot_at"`
	TimeZoneName   string `json:"time_zone_name" yaml:"time_zone_name"`
	OverlapPolicy  string `json:"overlap_policy" yaml:"overlap_policy"`
	CatchupWindow  string `json:"catchup_window" yaml:"catchup_window"`
	PauseOnFailure bool   `json:"pause_on_failure" yaml:"pause_on_failure"`
}

type NotificationPolicy struct {
	OnFailure bool `json:"on_failure" yaml:"on_failure"`
}

type Step struct {
	ID                     string      `json:"id" yaml:"id"`
	Command                string      `json:"command" yaml:"command"`
	Args                   []string    `json:"args" yaml:"args"`
	StartToCloseTimeout    string      `json:"start_to_close_timeout" yaml:"start_to_close_timeout"`
	ScheduleToCloseTimeout string      `json:"schedule_to_close_timeout" yaml:"schedule_to_close_timeout"`
	RetryPolicy            RetryPolicy `json:"retry_policy" yaml:"retry_policy"`
}

type RetryPolicy struct {
	InitialInterval        string   `json:"initial_interval" yaml:"initial_interval"`
	BackoffCoefficient     float64  `json:"backoff_coefficient" yaml:"backoff_coefficient"`
	MaximumInterval        string   `json:"maximum_interval" yaml:"maximum_interval"`
	MaximumAttempts        int32    `json:"maximum_attempts" yaml:"maximum_attempts"`
	NonRetryableErrorTypes []string `json:"non_retryable_error_types" yaml:"non_retryable_error_types"`
}

type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

func WorkflowDir(workspaceRoot, workflowID string) (string, error) {
	root, err := OpenCTODir(workspaceRoot)
	if err != nil {
		return "", err
	}
	id, err := NormalizeWorkflowID(workflowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "workflows", id), nil
}

func WorkflowRunsDir(workspaceRoot, workflowID string) (string, error) {
	root, err := OpenCTODir(workspaceRoot)
	if err != nil {
		return "", err
	}
	id, err := NormalizeWorkflowID(workflowID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "workflow-runs", id), nil
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

func OpenCTODir(workspaceRoot string) (string, error) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	if filepath.Base(filepath.Clean(root)) == ".opencto" {
		return root, nil
	}
	return filepath.Join(root, ".opencto"), nil
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
	if err := validateManifestYAMLRequiredFields(data); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateManifestYAMLRequiredFields(data []byte) error {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return err
	}
	root, err := yamlDocumentMapping(&document, "workflow manifest")
	if err != nil {
		return err
	}
	fields, err := yamlMappingFields(root, "workflow manifest")
	if err != nil {
		return err
	}
	if err := requireYAMLFields(fields, "workflow manifest", []string{
		"name",
		"description",
		"schedule",
		"notification_policy",
		"env",
		"steps",
	}); err != nil {
		return err
	}

	scheduleFields, err := yamlRequiredMappingField(fields, "schedule", "workflow manifest")
	if err != nil {
		return err
	}
	if err := requireYAMLFields(scheduleFields, "schedule", []string{
		"cron",
		"one_shot_at",
		"time_zone_name",
		"overlap_policy",
		"catchup_window",
		"pause_on_failure",
	}); err != nil {
		return err
	}

	notificationFields, err := yamlRequiredMappingField(fields, "notification_policy", "workflow manifest")
	if err != nil {
		return err
	}
	if err := requireYAMLFields(notificationFields, "notification_policy", []string{"on_failure"}); err != nil {
		return err
	}

	retryRequired := []string{
		"initial_interval",
		"backoff_coefficient",
		"maximum_interval",
		"maximum_attempts",
		"non_retryable_error_types",
	}
	steps := fields["steps"]
	if steps.Kind != yaml.SequenceNode {
		return fmt.Errorf("steps must be a list")
	}
	for index, step := range steps.Content {
		path := fmt.Sprintf("steps[%d]", index)
		stepFields, err := yamlMappingFields(step, path)
		if err != nil {
			return err
		}
		if err := requireYAMLFields(stepFields, path, []string{
			"id",
			"command",
			"args",
			"start_to_close_timeout",
			"schedule_to_close_timeout",
			"retry_policy",
		}); err != nil {
			return err
		}
		retryFields, err := yamlRequiredMappingField(stepFields, "retry_policy", path)
		if err != nil {
			return err
		}
		if err := requireYAMLFields(retryFields, path+".retry_policy", retryRequired); err != nil {
			return err
		}
	}
	return nil
}

func yamlDocumentMapping(document *yaml.Node, path string) (*yaml.Node, error) {
	if document.Kind == yaml.DocumentNode && len(document.Content) == 1 {
		return document.Content[0], nil
	}
	if document.Kind == yaml.MappingNode {
		return document, nil
	}
	return nil, fmt.Errorf("%s must be a mapping", path)
}

func yamlMappingFields(node *yaml.Node, path string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s must be a mapping", path)
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index+1 < len(node.Content); index += 2 {
		key := node.Content[index]
		value := node.Content[index+1]
		if key.Kind != yaml.ScalarNode {
			return nil, fmt.Errorf("%s contains a non-scalar key", path)
		}
		fields[key.Value] = value
	}
	return fields, nil
}

func yamlRequiredMappingField(fields map[string]*yaml.Node, name, path string) (map[string]*yaml.Node, error) {
	node, ok := fields[name]
	if !ok {
		return nil, fmt.Errorf("%s.%s is required", path, name)
	}
	return yamlMappingFields(node, path+"."+name)
}

func requireYAMLFields(fields map[string]*yaml.Node, path string, required []string) error {
	for _, field := range required {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("%s.%s is required", path, field)
		}
	}
	return nil
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

func CommitBundle(ctx context.Context, dir, message string, files []File) (string, error) {
	if err := EnsureGitRepo(ctx, dir); err != nil {
		return "", err
	}
	paths := []string{}
	seen := map[string]bool{}
	for _, file := range files {
		rel, err := cleanBundleFilePath(file.Path)
		if err != nil {
			return "", err
		}
		path := filepath.ToSlash(rel)
		if seen[path] {
			continue
		}
		seen[path] = true
		paths = append(paths, path)
	}
	if err := runGit(ctx, dir, "add", "--", ManifestFilename, ".gitignore"); err != nil {
		return "", err
	}
	if len(paths) > 0 {
		args := append([]string{"add", "-f", "--"}, paths...)
		if err := runGit(ctx, dir, args...); err != nil {
			return "", err
		}
	}
	if err := runGit(ctx, dir, "add", "-A", "--", "src"); err != nil {
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
	if strings.TrimSpace(manifest.Name) == "" {
		return ErrWorkflowNameRequired
	}
	if manifest.Env == nil {
		return fmt.Errorf("env is required; use [] when the workflow has no global env assignments")
	}
	for index, envVar := range manifest.Env {
		if _, _, err := ParseEnvAssignment(envVar); err != nil {
			return fmt.Errorf("env[%d]: %w", index, err)
		}
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
	if strings.TrimSpace(manifest.Schedule.TimeZoneName) == "" {
		return fmt.Errorf("schedule.time_zone_name is required")
	}
	if strings.TrimSpace(manifest.Schedule.CatchupWindow) == "" {
		return fmt.Errorf("schedule.catchup_window is required")
	}
	if _, err := ParseCatchupWindow(manifest.Schedule.CatchupWindow); err != nil {
		return err
	}
	if strings.TrimSpace(manifest.Schedule.OverlapPolicy) == "" {
		return fmt.Errorf("schedule.overlap_policy is required")
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
		if strings.ContainsAny(step.Command, " \t\r\n") {
			return fmt.Errorf("step %q: command must be one executable path or name; put arguments in args", id)
		}
		if step.Args == nil {
			return fmt.Errorf("step %q: args is required; use [] only for a workflow-local executable that needs no args", id)
		}
		if len(cleanStrings(step.Args)) == 0 && !workflowLocalCommand(step.Command) {
			return fmt.Errorf("step %q: %w: %q needs args such as [\"run\", \"./src/app\"] or [\"src/script.sh\"]", id, ErrStepArgsMissing, step.Command)
		}
		if len(step.Args) > 0 && !workflowLocalCommand(step.Command) && sameExecutableToken(step.Command, step.Args[0]) {
			return fmt.Errorf("step %q: args[0] must not repeat command %q; put the executable only in command and only its arguments in args", id, strings.TrimSpace(step.Command))
		}
		for index, arg := range step.Args {
			if strings.TrimSpace(arg) == "" {
				return fmt.Errorf("step %q: args[%d] is required", id, index)
			}
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
		if step.RetryPolicy.BackoffCoefficient < 0 || (step.RetryPolicy.BackoffCoefficient > 0 && step.RetryPolicy.BackoffCoefficient < 1) {
			return fmt.Errorf("step %q: retry_policy.backoff_coefficient must be 0 or at least 1", id)
		}
		if step.RetryPolicy.MaximumAttempts < 0 {
			return fmt.Errorf("step %q: retry_policy.maximum_attempts must not be negative", id)
		}
		if step.RetryPolicy.NonRetryableErrorTypes == nil {
			return fmt.Errorf("step %q: retry_policy.non_retryable_error_types is required; use [] when unset", id)
		}
		for index, errorType := range step.RetryPolicy.NonRetryableErrorTypes {
			if strings.TrimSpace(errorType) == "" {
				return fmt.Errorf("step %q: retry_policy.non_retryable_error_types[%d] is required", id, index)
			}
		}
	}
	return nil
}

func workflowLocalCommand(command string) bool {
	command = filepath.ToSlash(strings.TrimSpace(command))
	command = strings.TrimPrefix(command, "./")
	return command == "src" || strings.HasPrefix(command, "src/")
}

func sameExecutableToken(command, arg string) bool {
	arg = filepath.ToSlash(strings.TrimSpace(arg))
	arg = strings.TrimPrefix(arg, "./")
	if strings.Contains(arg, "/") {
		return false
	}
	commandToken := executableToken(command)
	argToken := executableToken(arg)
	return commandToken != "" && argToken != "" && commandToken == argToken
}

func executableToken(value string) string {
	value = filepath.ToSlash(strings.TrimSpace(value))
	value = strings.TrimPrefix(value, "./")
	if value == "." || value == "" || strings.ContainsAny(value, " \t\r\n") {
		return ""
	}
	return strings.TrimSuffix(path.Base(value), ".exe")
}

func cleanStrings(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func ParseEnvAssignment(value string) (string, string, error) {
	assignment := strings.TrimSpace(value)
	if assignment == "" {
		return "", "", fmt.Errorf("env assignment is required")
	}
	if strings.Contains(assignment, "{{") || strings.Contains(assignment, "}}") {
		return "", "", fmt.Errorf("env assignment must not use template syntax")
	}
	name, envValue, ok := strings.Cut(assignment, "=")
	if !ok {
		return "", "", fmt.Errorf("env assignment %q must be NAME=value", value)
	}
	name = strings.TrimSpace(name)
	if !envNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("env name %q must match %s", name, envNamePattern.String())
	}
	if strings.HasPrefix(name, "OPENCTO_") {
		return "", "", fmt.Errorf("env name %q is reserved for OpenCTO runtime variables", name)
	}
	if strings.ContainsRune(envValue, 0) {
		return "", "", fmt.Errorf("env value for %q must not contain NUL", name)
	}
	return name, envValue, nil
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
	lines := []string{
		"# Generated by OpenCTO. Do not edit.",
		"# Workflow Git tracks workflow.yml and src/. Keep runtime outputs and dependency caches out.",
		".DS_Store",
		"*.log",
		"output/",
		"artifacts/",
		"steps/",
		"tmp/",
		"temp/",
		".cache/",
		"node_modules/",
		"venv/",
		".venv/",
		"__pycache__/",
		".pytest_cache/",
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
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
	if strings.HasPrefix(rel, "src/") {
		return filepath.FromSlash(rel), nil
	}
	return "", fmt.Errorf("file path %q must be under src/", value)
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
	slugPattern    = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)
	safeIDPattern  = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)
	envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func slugify(value string) string {
	value = strings.TrimSpace(value)
	value = slugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	return strings.ToLower(value)
}
