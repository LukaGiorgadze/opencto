package browser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/opencto/opencto/internal/config"
	exectool "github.com/opencto/opencto/internal/tools/exec"
)

const (
	agentBrowserExecutable = "agent-browser"
	defaultTailBytes       = 16 << 10
)

var (
	ErrCommandRequired       = errors.New("browser command is required")
	ErrInvalidTimeout        = errors.New("browser timeout must be non-negative")
	ErrSessionFlagNotAllowed = errors.New("browser session must be provided with the session field, not args")
)

type Request struct {
	ProjectID     string            `json:"-"`
	Intent        string            `json:"-"`
	WorkingDir    string            `json:"-"`
	WorkspaceRoot string            `json:"-"`
	Timeout       time.Duration     `json:"-"`
	TailBytes     int64             `json:"-"`
	StateDir      string            `json:"-"`
	Environment   map[string]string `json:"-"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Session       string            `json:"session,omitempty"`
	TimeoutMs     int               `json:"timeout_ms,omitempty"`
	Idempotency   string            `json:"idempotency,omitempty"`
	Description   string            `json:"description,omitempty"`
	Destructive   bool              `json:"destructive,omitempty"`
	WorkItemID    string            `json:"-"`
	Actions       []Action          `json:"actions,omitempty"`
}

type Action struct {
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	TimeoutMs   int      `json:"timeout_ms,omitempty"`
	Idempotency string   `json:"idempotency,omitempty"`
	Description string   `json:"description,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
}

type Result struct {
	Stdout           string
	Stderr           string
	ExitCode         int
	WorkingDirectory string
	Executable       string
	FinalArgs        []string
	Session          string
	Command          string
	HoistedArgs      []string
	Args             []string
	ArtifactPaths    []string
	StdoutLogPath    string
	StderrLogPath    string
	StdoutTruncated  bool
	StderrTruncated  bool
	StartedAt        time.Time
	CompletedAt      time.Time
	Duration         time.Duration
	Actions          []ActionResult
}

type ActionResult struct {
	Stdout          string
	Stderr          string
	ExitCode        int
	Session         string
	Command         string
	HoistedArgs     []string
	Args            []string
	ArtifactPaths   []string
	StdoutLogPath   string
	StderrLogPath   string
	StdoutTruncated bool
	StderrTruncated bool
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type SafeExecutor struct {
	logger *slog.Logger
	runner commandRunner
}

type commandInvocation struct {
	executable string
	args       []string
	dir        string
	env        []string
	stateDir   string
	tailBytes  int64
	logName    string
}

type commandOutput struct {
	stdout          string
	stderr          string
	stdoutLogPath   string
	stderrLogPath   string
	stdoutTruncated bool
	stderrTruncated bool
}

type commandRunner func(context.Context, commandInvocation) (commandOutput, error)

func NewSafeExecutor(logger *slog.Logger) *SafeExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &SafeExecutor{
		logger: logger,
		runner: runCommand,
	}
}

func NewTool() *SafeExecutor {
	return NewSafeExecutor(nil)
}

func Execute(ctx context.Context, req Request) (Result, error) {
	return NewSafeExecutor(nil).Run(ctx, req)
}

func (e *SafeExecutor) Execute(ctx context.Context, req Request) (Result, error) {
	return e.Run(ctx, req)
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e.runner == nil {
		e.runner = runCommand
	}
	if len(req.Actions) > 0 {
		return e.runBatch(ctx, req)
	}
	return e.runSingle(ctx, req)
}

func (e *SafeExecutor) runBatch(ctx context.Context, req Request) (Result, error) {
	startedAt := time.Now()
	workingDir, err := exectool.ResolveWorkingDir(firstNonEmpty(req.WorkspaceRoot, req.WorkingDir))
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var stdout strings.Builder
	var stderr strings.Builder
	results := make([]ActionResult, 0, len(req.Actions))
	var artifactPaths []string
	exitCode := 0
	session := "batch"
	stdoutTruncated := false
	stderrTruncated := false

	for index, action := range req.Actions {
		result, err := e.runSingle(runCtx, requestForAction(req, action))
		appendActionOutput(&stdout, index, actionSummary(result.Command, result.Args), result.Stdout)
		appendActionOutput(&stderr, index, actionSummary(result.Command, result.Args), result.Stderr)
		exitCode = result.ExitCode
		stdoutTruncated = stdoutTruncated || result.StdoutTruncated
		stderrTruncated = stderrTruncated || result.StderrTruncated
		if index == 0 {
			session = result.Session
		} else if session != result.Session {
			session = "batch"
		}
		artifactPaths = append(artifactPaths, result.ArtifactPaths...)
		results = append(results, ActionResult{
			Stdout:          result.Stdout,
			Stderr:          result.Stderr,
			ExitCode:        result.ExitCode,
			Session:         result.Session,
			Command:         result.Command,
			HoistedArgs:     append([]string(nil), result.HoistedArgs...),
			Args:            append([]string(nil), result.Args...),
			ArtifactPaths:   append([]string(nil), result.ArtifactPaths...),
			StdoutLogPath:   result.StdoutLogPath,
			StderrLogPath:   result.StderrLogPath,
			StdoutTruncated: result.StdoutTruncated,
			StderrTruncated: result.StderrTruncated,
		})
		if err != nil {
			completedAt := time.Now()
			return Result{
				Stdout:           stdout.String(),
				Stderr:           stderr.String(),
				ExitCode:         exitCode,
				WorkingDirectory: workingDir,
				Executable:       agentBrowserExecutable,
				Session:          session,
				Command:          "batch",
				ArtifactPaths:    uniqueArtifactPaths(artifactPaths),
				StdoutLogPath:    result.StdoutLogPath,
				StderrLogPath:    result.StderrLogPath,
				StdoutTruncated:  result.StdoutTruncated,
				StderrTruncated:  result.StderrTruncated,
				StartedAt:        startedAt,
				CompletedAt:      completedAt,
				Duration:         completedAt.Sub(startedAt),
				Actions:          results,
			}, fmt.Errorf("browser action %d: %w", index+1, err)
		}
	}

	completedAt := time.Now()
	return Result{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		ExitCode:         exitCode,
		WorkingDirectory: workingDir,
		Executable:       agentBrowserExecutable,
		Session:          session,
		Command:          "batch",
		ArtifactPaths:    uniqueArtifactPaths(artifactPaths),
		StdoutLogPath:    firstActionLogPath(results, true),
		StderrLogPath:    firstActionLogPath(results, false),
		StdoutTruncated:  stdoutTruncated,
		StderrTruncated:  stderrTruncated,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
		Actions:          results,
	}, nil
}

func (e *SafeExecutor) runSingle(ctx context.Context, req Request) (Result, error) {
	normalized, err := normalizeRequest(req)
	if err != nil {
		return Result{}, err
	}
	workingDir, err := exectool.ResolveWorkingDir(firstNonEmpty(normalized.WorkspaceRoot, normalized.WorkingDir))
	if err != nil {
		return Result{}, err
	}

	startedAt := time.Now()

	runCtx := ctx
	var cancel context.CancelFunc
	if normalized.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, normalized.Timeout)
		defer cancel()
	}

	hoistedArgs, commandArgs := splitAgentBrowserArgs(normalized.Args)
	finalArgs := agentBrowserArgs(normalized, hoistedArgs, commandArgs)
	invocation := commandInvocation{
		executable: agentBrowserExecutable,
		args:       finalArgs,
		dir:        workingDir,
		env:        mergeEnv(map[string]string{config.EnvOpenCTOWorkspace: workingDir}, normalized.Environment),
		stateDir:   normalized.StateDir,
		tailBytes:  normalized.TailBytes,
		logName:    browserLogName(normalized.ProjectID, normalized.WorkItemID, normalized.Session, normalized.Command),
	}
	output, runErr := e.runner(runCtx, invocation)
	completedAt := time.Now()

	result := Result{
		Stdout:           output.stdout,
		Stderr:           output.stderr,
		ExitCode:         exitCode(runErr),
		WorkingDirectory: workingDir,
		Executable:       agentBrowserExecutable,
		FinalArgs:        append([]string(nil), finalArgs...),
		Session:          normalized.Session,
		Command:          normalized.Command,
		HoistedArgs:      append([]string(nil), hoistedArgs...),
		Args:             append([]string(nil), commandArgs...),
		StdoutLogPath:    output.stdoutLogPath,
		StderrLogPath:    output.stderrLogPath,
		StdoutTruncated:  output.stdoutTruncated,
		StderrTruncated:  output.stderrTruncated,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}
	result.ArtifactPaths = detectArtifactPaths(workingDir, result.Stdout+"\n"+result.Stderr)

	e.logger.Info("browser command executed",
		slog.String("project_id", normalized.ProjectID),
		slog.String("work_item_id", normalized.WorkItemID),
		slog.String("intent", normalized.Intent),
		slog.String("session", normalized.Session),
		slog.String("command", normalized.Command),
		slog.Any("hoisted_args", hoistedArgs),
		slog.Any("args", commandArgs),
		slog.String("working_dir", workingDir),
		slog.Duration("duration", result.Duration),
		slog.Int("exit_code", result.ExitCode),
		slog.Int("artifact_count", len(result.ArtifactPaths)),
	)

	return result, runErr
}

func requestForAction(parent Request, action Action) Request {
	return Request{
		ProjectID:     parent.ProjectID,
		Intent:        firstNonEmpty(action.Description, parent.Intent),
		WorkingDir:    parent.WorkingDir,
		WorkspaceRoot: parent.WorkspaceRoot,
		StateDir:      parent.StateDir,
		TailBytes:     parent.TailBytes,
		Environment:   parent.Environment,
		Command:       action.Command,
		Args:          append([]string(nil), action.Args...),
		Session:       parent.Session,
		TimeoutMs:     action.TimeoutMs,
		Idempotency:   action.Idempotency,
		Description:   action.Description,
		Destructive:   action.Destructive,
		WorkItemID:    parent.WorkItemID,
	}
}

func normalizeRequest(req Request) (Request, error) {
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" {
		return Request{}, ErrCommandRequired
	}
	if isSessionFlag(req.Command) {
		return Request{}, fmt.Errorf("%w: %s", ErrSessionFlagNotAllowed, req.Command)
	}
	if req.TimeoutMs < 0 {
		return Request{}, ErrInvalidTimeout
	}
	if req.Timeout <= 0 && req.TimeoutMs > 0 {
		req.Timeout = time.Duration(req.TimeoutMs) * time.Millisecond
	}
	if err := rejectSessionFlags(req.Args); err != nil {
		return Request{}, err
	}

	req.Session = strings.TrimSpace(req.Session)
	if req.Session == "" {
		req.Session = defaultSession(req.ProjectID)
	} else {
		req.Session = sessionWithProject(req.ProjectID, req.Session)
	}
	req.Args = append([]string(nil), req.Args...)
	return req, nil
}

func appendActionOutput(builder *strings.Builder, index int, summary string, output string) {
	if strings.TrimSpace(output) == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n")
	}
	_, _ = fmt.Fprintf(builder, "action %d: %s\n", index+1, summary)
	builder.WriteString(output)
	if !strings.HasSuffix(output, "\n") {
		builder.WriteString("\n")
	}
}

func actionSummary(command string, args []string) string {
	command = strings.TrimSpace(command)
	if len(args) == 0 {
		return command
	}
	return command + " " + strings.Join(args, " ")
}

func uniqueArtifactPaths(paths []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" || seen[path] {
			continue
		}
		seen[path] = true
		unique = append(unique, path)
	}
	sort.Strings(unique)
	return unique
}

func firstActionLogPath(results []ActionResult, stdout bool) string {
	for _, result := range results {
		value := result.StderrLogPath
		if stdout {
			value = result.StdoutLogPath
		}
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func agentBrowserArgs(req Request, globalArgs []string, commandArgs []string) []string {
	args := make([]string, 0, len(globalArgs)+len(commandArgs)+3)
	args = append(args, "--session", req.Session)
	args = append(args, globalArgs...)
	args = append(args, req.Command)
	args = append(args, commandArgs...)
	return args
}

func splitAgentBrowserArgs(args []string) ([]string, []string) {
	globalArgs := make([]string, 0, len(args))
	commandArgs := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		if arg == "" {
			commandArgs = append(commandArgs, args[i])
			continue
		}
		if isAgentBrowserGlobalFlagWithValue(arg) {
			globalArgs = append(globalArgs, args[i])
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
				globalArgs = append(globalArgs, args[i])
			}
			continue
		}
		if isAgentBrowserBooleanGlobalFlag(arg) {
			globalArgs = append(globalArgs, args[i])
			if i+1 < len(args) && isBooleanValue(args[i+1]) {
				i++
				globalArgs = append(globalArgs, args[i])
			}
			continue
		}
		commandArgs = append(commandArgs, args[i])
	}
	return globalArgs, commandArgs
}

func isAgentBrowserBooleanGlobalFlag(arg string) bool {
	switch arg {
	case "--allow-file-access",
		"--annotate",
		"--auto-connect",
		"--content-boundaries",
		"--confirm-interactive",
		"--debug",
		"--headed",
		"--ignore-https-errors",
		"--json",
		"--no-auto-dialog",
		"--quiet",
		"--verbose",
		"-q",
		"-v":
		return true
	default:
		return false
	}
}

func isAgentBrowserGlobalFlagWithValue(arg string) bool {
	for _, flag := range []string{
		"--action-policy",
		"--allowed-domains",
		"--args",
		"--cdp",
		"--color-scheme",
		"--config",
		"--device",
		"--download-path",
		"--enable",
		"--engine",
		"--executable-path",
		"--extension",
		"--headers",
		"--init-script",
		"--max-output",
		"--model",
		"--profile",
		"--provider",
		"-p",
		"--proxy",
		"--proxy-bypass",
		"--screenshot-dir",
		"--screenshot-format",
		"--screenshot-quality",
		"--session-name",
		"--state",
		"--user-agent",
	} {
		if arg == flag || strings.HasPrefix(arg, flag+"=") {
			return true
		}
	}
	return false
}

func isBooleanValue(arg string) bool {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "true", "false":
		return true
	default:
		return false
	}
}

func runCommand(ctx context.Context, invocation commandInvocation) (commandOutput, error) {
	cmd := exec.CommandContext(ctx, invocation.executable, invocation.args...)
	cmd.Dir = invocation.dir
	cmd.Env = invocation.env

	stdoutFile, stderrFile, stdoutPath, stderrPath, err := browserLogFiles(invocation)
	if err != nil {
		return commandOutput{}, err
	}
	defer stdoutFile.Close()
	defer stderrFile.Close()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	err = cmd.Run()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	stdout, stdoutTruncated := tailFile(stdoutPath, invocation.tailBytes)
	stderr, stderrTruncated := tailFile(stderrPath, invocation.tailBytes)
	return commandOutput{
		stdout:          stdout,
		stderr:          stderr,
		stdoutLogPath:   stdoutPath,
		stderrLogPath:   stderrPath,
		stdoutTruncated: stdoutTruncated,
		stderrTruncated: stderrTruncated,
	}, err
}

func browserLogFiles(invocation commandInvocation) (*os.File, *os.File, string, string, error) {
	dir, err := browserLogDir(invocation.stateDir)
	if err != nil {
		return nil, nil, "", "", err
	}
	name := strings.TrimSpace(invocation.logName)
	if name == "" {
		name = "browser"
	}
	name = name + "-" + fmt.Sprint(time.Now().UnixNano())
	stdoutPath := filepath.Join(dir, name+".stdout.log")
	stderrPath := filepath.Join(dir, name+".stderr.log")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, "", "", err
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = stdoutFile.Close()
		return nil, nil, "", "", err
	}
	return stdoutFile, stderrFile, stdoutPath, stderrPath, nil
}

func browserLogDir(stateDir string) (string, error) {
	if strings.TrimSpace(stateDir) == "" {
		dir := filepath.Join(os.TempDir(), "opencto-browser-logs")
		return dir, os.MkdirAll(dir, 0o755)
	}
	dir := filepath.Join(stateDir, "logs")
	return dir, os.MkdirAll(dir, 0o755)
}

func tailFile(path string, limit int64) (string, bool) {
	if limit <= 0 {
		limit = defaultTailBytes
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	data, err := readTail(path, limit)
	if err != nil {
		return "", false
	}
	return data, info.Size() > limit
}

func readTail(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := int64(0)
	if limit > 0 && info.Size() > limit {
		offset = info.Size() - limit
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func rejectSessionFlags(args []string) error {
	for _, arg := range args {
		if isSessionFlag(arg) {
			return fmt.Errorf("%w: %s", ErrSessionFlagNotAllowed, arg)
		}
	}
	return nil
}

func isSessionFlag(arg string) bool {
	arg = strings.TrimSpace(arg)
	return arg == "--session" ||
		strings.HasPrefix(arg, "--session=")
}

func defaultSession(projectID string) string {
	return "opencto-" + sanitizeSessionPart(projectID)
}

func sessionWithProject(projectID, session string) string {
	prefix := defaultSession(projectID)
	session = sanitizeSessionPart(session)
	if session == prefix || strings.HasPrefix(session, prefix+"-") {
		return session
	}
	return prefix + "-" + session
}

func browserLogName(projectID, workItemID, session, command string) string {
	parts := []string{"browser"}
	for _, value := range []string{projectID, workItemID, session, command} {
		part := sanitizeSessionPart(value)
		if part != "" && part != "default" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "-")
}

func sanitizeSessionPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if valid {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" {
		return "default"
	}
	return result
}

var artifactLinkPattern = regexp.MustCompile(`\(([^)\n]+\.(?:ya?ml|png|jpe?g|pdf|zip|webm|mp4|json))\)`)

func detectArtifactPaths(workingDir, output string) []string {
	seen := map[string]bool{}
	var paths []string

	add := func(candidate string) {
		candidate = strings.Trim(candidate, " \t\r\n\"'`,;")
		if strings.ContainsAny(candidate, "[]()") {
			return
		}
		if candidate == "" || !looksLikeArtifact(candidate) {
			return
		}
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workingDir, candidate)
		}
		candidate = filepath.Clean(candidate)
		if seen[candidate] {
			return
		}
		seen[candidate] = true
		paths = append(paths, candidate)
	}

	for _, match := range artifactLinkPattern.FindAllStringSubmatch(output, -1) {
		add(match[1])
	}
	for _, field := range strings.Fields(output) {
		add(field)
	}
	sort.Strings(paths)
	return paths
}

func looksLikeArtifact(candidate string) bool {
	normalized := filepath.ToSlash(candidate)
	if strings.Contains(normalized, ".agent-browser/") {
		return true
	}
	ext := strings.ToLower(filepath.Ext(candidate))
	switch ext {
	case ".yaml", ".yml", ".png", ".jpg", ".jpeg", ".pdf", ".zip", ".webm", ".mp4", ".json":
		return true
	default:
		return false
	}
}

func mergeEnv(defaults map[string]string, overrides map[string]string) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			current[parts[0]] = parts[1]
		}
	}
	for key, value := range defaults {
		if strings.TrimSpace(current[key]) == "" {
			current[key] = value
		}
	}
	for key, value := range overrides {
		current[key] = value
	}
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+current[key])
	}
	return result
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}
	return 1
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
