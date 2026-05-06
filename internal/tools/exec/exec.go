package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

var (
	ErrEmptyCommand = errors.New("command is required")
)

const defaultTailBytes int64 = 16 << 10

type Request struct {
	ProjectID          string
	WorkItemID         string
	ToolCallID         string
	ProcessID          string
	Intent             string
	Command            string
	Args               []string
	Actions            []Action
	WorkingDir         string
	StateDir           string
	Timeout            time.Duration
	GracePeriod        time.Duration
	TailBytes          int64
	ProcessScope       domain.ProcessScope
	Environment        map[string]string
	FallbackCandidates []domain.ToolType
}

type Action struct {
	Intent      string            `json:"intent,omitempty"`
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
	WorkingDir  string            `json:"working_dir,omitempty"`
	TimeoutMs   int               `json:"timeout_ms,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
}

type BatchInput struct {
	Actions []Action `json:"actions"`
}

type Result struct {
	Stdout           string
	Stderr           string
	ExitCode         int
	WorkingDirectory string
	StdoutLogPath    string
	StderrLogPath    string
	StdoutTruncated  bool
	StderrTruncated  bool
	ManagedProcess   *domain.ManagedProcess
	StartedAt        time.Time
	CompletedAt      time.Time
	Duration         time.Duration
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

func DecodeBatchInput(raw json.RawMessage) (BatchInput, error) {
	var input BatchInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return BatchInput{}, err
	}
	if len(input.Actions) == 0 {
		return BatchInput{}, ErrEmptyCommand
	}
	for _, action := range input.Actions {
		if strings.TrimSpace(action.Command) == "" {
			return BatchInput{}, ErrEmptyCommand
		}
	}
	return input, nil
}

type SafeExecutor struct {
	logger *slog.Logger
}

func NewSafeExecutor(logger *slog.Logger) *SafeExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &SafeExecutor{logger: logger}
}

func (e *SafeExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if len(req.Actions) > 0 {
		return e.runBatch(ctx, req)
	}
	return e.runSingle(ctx, req)
}

func (e *SafeExecutor) runBatch(ctx context.Context, req Request) (Result, error) {
	startedAt := time.Now()
	workingDir, err := ResolveWorkingDir(req.WorkingDir)
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := commandContext(ctx, req.Timeout)
	if cancel != nil {
		defer cancel()
	}

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode := 0

	for index, action := range req.Actions {
		subReq := execRequestForAction(req, action)
		result, err := e.runSingle(runCtx, subReq)
		if result.ManagedProcess != nil {
			return result, err
		}
		appendActionOutput(&stdout, index, action.Command, action.Args, result.Stdout)
		appendActionOutput(&stderr, index, action.Command, action.Args, result.Stderr)
		exitCode = result.ExitCode
		if err != nil {
			completedAt := time.Now()
			return Result{
				Stdout:           stdout.String(),
				Stderr:           stderr.String(),
				ExitCode:         exitCode,
				WorkingDirectory: workingDir,
				StartedAt:        startedAt,
				CompletedAt:      completedAt,
				Duration:         completedAt.Sub(startedAt),
			}, err
		}
	}

	completedAt := time.Now()
	return Result{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		ExitCode:         exitCode,
		WorkingDirectory: workingDir,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}, nil
}

func (e *SafeExecutor) runSingle(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Result{}, ErrEmptyCommand
	}

	startedAt := time.Now().UTC()

	workingDir, err := ResolveWorkingDir(req.WorkingDir)
	if err != nil {
		return Result{}, err
	}

	cmd := osexec.Command(req.Command, req.Args...)
	cmd.Dir = workingDir
	cmd.Env = mergeEnv(req.Environment)
	setProcessGroup(cmd)

	stdoutFile, stderrFile, stdoutPath, stderrPath, err := commandLogFiles(req)
	if err != nil {
		return Result{}, err
	}
	defer stdoutFile.Close()
	defer stderrFile.Close()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	if err := cmd.Start(); err != nil {
		completedAt := time.Now().UTC()
		result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, err, nil)
		return result, err
	}

	process := domain.ManagedProcess{
		ID:               strings.TrimSpace(req.ProcessID),
		ProjectID:        req.ProjectID,
		WorkItemID:       req.WorkItemID,
		ToolCallID:       req.ToolCallID,
		Command:          req.Command,
		Args:             append([]string(nil), req.Args...),
		WorkingDirectory: workingDir,
		PID:              cmd.Process.Pid,
		PGID:             processGroupID(cmd.Process.Pid),
		Status:           domain.ProcessStatusRunning,
		StdoutLogPath:    stdoutPath,
		StderrLogPath:    stderrPath,
		StartedAt:        startedAt,
		UpdatedAt:        startedAt,
		Metadata: domain.Metadata{
			"intent":        req.Intent,
			"process_scope": string(startProcessScope(req.ProcessScope)),
		},
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	timeout := commandTimer(req.Timeout)
	defer stopTimer(timeout)
	grace := commandTimer(req.GracePeriod)
	defer stopTimer(grace)
	canPromote := req.GracePeriod > 0 && process.ID != "" && strings.TrimSpace(req.StateDir) != ""

	for {
		select {
		case waitErr := <-waitCh:
			completedAt := time.Now().UTC()
			result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, waitErr, nil)
			e.logCommand(req, result)
			return result, waitErr
		case <-ctx.Done():
			_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
			waitErr := <-waitCh
			if ctx.Err() != nil {
				waitErr = ctx.Err()
			}
			completedAt := time.Now().UTC()
			result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, waitErr, nil)
			e.logCommand(req, result)
			return result, waitErr
		case <-timerC(timeout):
			_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
			<-waitCh
			waitErr := context.DeadlineExceeded
			completedAt := time.Now().UTC()
			result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, waitErr, nil)
			e.logCommand(req, result)
			return result, waitErr
		case <-timerC(grace):
			if !canPromote {
				continue
			}
			root, err := processStateDir(req.StateDir)
			if err != nil {
				_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
				<-waitCh
				completedAt := time.Now().UTC()
				result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, err, nil)
				e.logCommand(req, result)
				return result, err
			}
			if err := os.MkdirAll(filepath.Join(root, "processes"), 0o755); err != nil {
				_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
				<-waitCh
				completedAt := time.Now().UTC()
				result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, err, nil)
				e.logCommand(req, result)
				return result, err
			}
			if err := writeManagedProcess(root, process); err != nil {
				_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
				<-waitCh
				completedAt := time.Now().UTC()
				result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, err, nil)
				e.logCommand(req, result)
				return result, err
			}
			e.trackPromotedProcess(waitCh, root, process, time.Until(startedAt.Add(req.Timeout)))
			completedAt := time.Now().UTC()
			result := e.resultFromLogs(req, workingDir, stdoutPath, stderrPath, startedAt, completedAt, nil, &process)
			e.logCommand(req, result)
			return result, nil
		}
	}
}

func (e *SafeExecutor) resultFromLogs(req Request, workingDir, stdoutPath, stderrPath string, startedAt, completedAt time.Time, err error, process *domain.ManagedProcess) Result {
	stdout, stdoutTruncated := tailFile(stdoutPath, req.TailBytes)
	stderr, stderrTruncated := tailFile(stderrPath, req.TailBytes)
	code := exitCode(err)
	if process != nil {
		code = 0
	}
	return Result{
		Stdout:           stdout,
		Stderr:           stderr,
		ExitCode:         code,
		WorkingDirectory: workingDir,
		StdoutLogPath:    stdoutPath,
		StderrLogPath:    stderrPath,
		StdoutTruncated:  stdoutTruncated,
		StderrTruncated:  stderrTruncated,
		ManagedProcess:   process,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}
}

func (e *SafeExecutor) logCommand(req Request, result Result) {
	e.logger.Info("command executed",
		slog.String("project_id", req.ProjectID),
		slog.String("intent", req.Intent),
		slog.String("command", req.Command),
		slog.Any("args", req.Args),
		slog.String("cwd", result.WorkingDirectory),
		slog.Duration("duration", result.Duration),
		slog.Int("exit_code", result.ExitCode),
		slog.String("stdout_log_path", result.StdoutLogPath),
		slog.String("stderr_log_path", result.StderrLogPath),
	)
}

func (e *SafeExecutor) trackPromotedProcess(waitCh <-chan error, stateDir string, process domain.ManagedProcess, timeout time.Duration) {
	go func() {
		var err error
		if timeout > 0 {
			timer := time.NewTimer(timeout)
			select {
			case err = <-waitCh:
				timer.Stop()
			case <-timer.C:
				_ = terminateManagedProcess(process.PID, process.PGID, 2*time.Second)
				err = <-waitCh
				if err == nil {
					err = context.DeadlineExceeded
				}
			}
		} else {
			err = <-waitCh
		}

		updated := process
		if current, readErr := readManagedProcess(stateDir, process.ID); readErr == nil {
			updated = current
		}
		if updated.Status == domain.ProcessStatusRunning {
			if err != nil {
				updated.Status = domain.ProcessStatusFailed
			} else {
				updated.Status = domain.ProcessStatusExited
			}
			updated.UpdatedAt = time.Now().UTC()
			_ = writeManagedProcess(stateDir, updated)
		}
	}()
}

func commandLogFiles(req Request) (*os.File, *os.File, string, string, error) {
	logDir, err := commandLogDir(req.StateDir)
	if err != nil {
		return nil, nil, "", "", err
	}
	name := strings.TrimSpace(req.ProcessID)
	if name == "" {
		name = "exec-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	stdoutPath := filepath.Join(logDir, name+".stdout.log")
	stderrPath := filepath.Join(logDir, name+".stderr.log")
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

func commandLogDir(stateDir string) (string, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		dir := filepath.Join(os.TempDir(), "opencto-exec-logs")
		return dir, os.MkdirAll(dir, 0o755)
	}
	root, err := processStateDir(stateDir)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "logs")
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
	tail, err := readTail(path, limit)
	if err != nil {
		return "", false
	}
	return tail, info.Size() > limit
}

func commandTimer(timeout time.Duration) *time.Timer {
	if timeout <= 0 {
		return nil
	}
	return time.NewTimer(timeout)
}

func timerC(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}

func stopTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

func commandContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, nil
	}
	return context.WithTimeout(ctx, timeout)
}

func execRequestForAction(parent Request, action Action) Request {
	timeout := time.Duration(action.TimeoutMs) * time.Millisecond
	return Request{
		ProjectID:          parent.ProjectID,
		WorkItemID:         parent.WorkItemID,
		ToolCallID:         parent.ToolCallID,
		ProcessID:          parent.ProcessID,
		Intent:             firstNonEmpty(action.Intent, parent.Intent),
		Command:            action.Command,
		Args:               append([]string(nil), action.Args...),
		WorkingDir:         firstNonEmpty(action.WorkingDir, parent.WorkingDir),
		StateDir:           parent.StateDir,
		Timeout:            timeout,
		GracePeriod:        parent.GracePeriod,
		TailBytes:          parent.TailBytes,
		ProcessScope:       parent.ProcessScope,
		Environment:        mergeStringMaps(parent.Environment, action.Environment),
		FallbackCandidates: append([]domain.ToolType(nil), parent.FallbackCandidates...),
	}
}

func appendActionOutput(builder *strings.Builder, index int, command string, args []string, output string) {
	output = strings.TrimSpace(output)
	if output == "" {
		return
	}
	if builder.Len() > 0 {
		builder.WriteString("\n\n")
	}
	builder.WriteString("command ")
	builder.WriteString(strconv.Itoa(index + 1))
	builder.WriteString(": ")
	builder.WriteString(commandLine(command, args))
	builder.WriteString("\n")
	builder.WriteString(output)
}

func commandLine(command string, args []string) string {
	parts := append([]string{command}, args...)
	return strings.Join(parts, " ")
}

func mergeStringMaps(base, overrides map[string]string) map[string]string {
	if len(base) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := map[string]string{}
	maps.Copy(merged, base)
	maps.Copy(merged, overrides)
	return merged
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

func ResolveWorkingDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		current, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current working dir: %w", err)
		}
		dir = current
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve working dir: %w", err)
	}
	return absDir, nil
}

func ResolvePath(baseDir, path string) (string, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		return absPath, nil
	}

	workingDir, err := ResolveWorkingDir(baseDir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(filepath.Join(workingDir, path))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	return absPath, nil
}

func mergeEnv(overrides map[string]string) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			current[parts[0]] = parts[1]
		}
	}
	maps.Copy(current, overrides)

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

	var exitErr *osexec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			return status.ExitStatus()
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}
	return 1
}
