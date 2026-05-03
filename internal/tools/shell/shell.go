package shell

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/exec"
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

type Request struct {
	ProjectID          string
	Intent             string
	Command            string
	Args               []string
	Actions            []Action
	WorkingDir         string
	Timeout            time.Duration
	Environment        map[string]string
	FallbackCandidates []domain.ToolType
}

type Action struct {
	Intent      string            `json:"intent,omitempty"`
	Command     string            `json:"command"`
	Args        []string          `json:"args,omitempty"`
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
	req.WorkingDir = workingDir

	runCtx, cancel := commandContext(ctx, req.Timeout)
	if cancel != nil {
		defer cancel()
	}

	var stdout strings.Builder
	var stderr strings.Builder
	exitCode := 0

	for index, action := range req.Actions {
		subReq := shellRequestForAction(req, action)
		result, err := e.runSingle(runCtx, subReq)
		appendActionOutput(&stdout, index, action.Command, action.Args, result.Stdout)
		appendActionOutput(&stderr, index, action.Command, action.Args, result.Stderr)
		exitCode = result.ExitCode
		if err != nil {
			completedAt := time.Now()
			return Result{
				Stdout:           stdout.String(),
				Stderr:           stderr.String(),
				ExitCode:         exitCode,
				WorkingDirectory: req.WorkingDir,
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
		WorkingDirectory: req.WorkingDir,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}, nil
}

func (e *SafeExecutor) runSingle(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Result{}, ErrEmptyCommand
	}

	startedAt := time.Now()

	workingDir, err := ResolveWorkingDir(req.WorkingDir)
	if err != nil {
		return Result{}, err
	}

	runCtx, cancel := commandContext(ctx, req.Timeout)
	if cancel != nil {
		defer cancel()
	}

	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = workingDir
	cmd.Env = mergeEnv(req.Environment)
	setProcessGroup(cmd)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = runCommandWithContext(runCtx, cmd)
	completedAt := time.Now()

	result := Result{
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		ExitCode:         exitCode(err),
		WorkingDirectory: workingDir,
		StartedAt:        startedAt,
		CompletedAt:      completedAt,
		Duration:         completedAt.Sub(startedAt),
	}

	e.logger.Info("command executed",
		slog.String("project_id", req.ProjectID),
		slog.String("intent", req.Intent),
		slog.String("command", req.Command),
		slog.Any("args", req.Args),
		slog.String("cwd", workingDir),
		slog.Duration("duration", result.Duration),
		slog.Int("exit_code", result.ExitCode),
	)

	if err != nil {
		return result, err
	}

	return result, nil
}

func commandContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, nil
	}
	return context.WithTimeout(ctx, timeout)
}

func shellRequestForAction(parent Request, action Action) Request {
	timeout := time.Duration(action.TimeoutMs) * time.Millisecond
	return Request{
		ProjectID:          parent.ProjectID,
		Intent:             firstNonEmpty(action.Intent, parent.Intent),
		Command:            action.Command,
		Args:               append([]string(nil), action.Args...),
		WorkingDir:         parent.WorkingDir,
		Timeout:            timeout,
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

func runCommandWithContext(ctx context.Context, cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	select {
	case err := <-waitCh:
		return err
	case <-ctx.Done():
		if cmd.Process != nil {
			_ = terminateProcessGroup(cmd.Process.Pid, 2*time.Second)
		}
		err := <-waitCh
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
}

func ResolveWorkingDir(dir string) (string, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("OPENCTO_WORKSPACE"))
	}
	if dir == "" {
		return "", fmt.Errorf("working dir is required")
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

	var exitErr *exec.ExitError
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
