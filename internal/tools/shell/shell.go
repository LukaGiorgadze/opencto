package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

var (
	ErrWorkingDirectoryEscape = errors.New("working directory escapes workspace root")
	ErrEmptyCommand           = errors.New("command is required")
)

type Request struct {
	ProjectID             string
	Intent                string
	Command               string
	Args                  []string
	WorkingDir            string
	WorkspaceRoot         string
	Timeout               time.Duration
	Environment           map[string]string
	AllowOutsideWorkspace bool
	FallbackCandidates    []domain.ToolType
}

type Result struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	StartedAt   time.Time
	CompletedAt time.Time
	Duration    time.Duration
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
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
	if strings.TrimSpace(req.Command) == "" {
		return Result{}, ErrEmptyCommand
	}

	startedAt := time.Now()

	workingDir, err := secureWorkingDir(req.WorkspaceRoot, req.WorkingDir, req.AllowOutsideWorkspace)
	if err != nil {
		return Result{}, err
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, req.Command, req.Args...)
	cmd.Dir = workingDir
	cmd.Env = mergeEnv(req.Environment)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()
	completedAt := time.Now()

	result := Result{
		Stdout:      stdout.String(),
		Stderr:      stderr.String(),
		ExitCode:    exitCode(err),
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    completedAt.Sub(startedAt),
	}

	e.logger.Info("shell command executed",
		slog.String("project_id", req.ProjectID),
		slog.String("intent", req.Intent),
		slog.String("command", req.Command),
		slog.Any("args", req.Args),
		slog.String("working_dir", workingDir),
		slog.Duration("duration", result.Duration),
		slog.Int("exit_code", result.ExitCode),
	)

	if err != nil {
		return result, err
	}

	return result, nil
}

func secureWorkingDir(workspaceRoot, workingDir string, allowOutside bool) (string, error) {
	root := workspaceRoot
	if root == "" {
		root = "."
	}

	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}

	dir := workingDir
	if dir == "" {
		dir = absRoot
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve working dir: %w", err)
	}

	if allowOutside {
		return absDir, nil
	}
	if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(os.PathSeparator)) {
		return "", ErrWorkingDirectoryEscape
	}
	return absDir, nil
}

func mergeEnv(overrides map[string]string) []string {
	current := map[string]string{}
	for _, entry := range os.Environ() {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 {
			current[parts[0]] = parts[1]
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
