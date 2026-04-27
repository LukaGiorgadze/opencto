package shell

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSafeExecutorRejectsWorkingDirectoryEscape(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := executor.Run(context.Background(), Request{
		Command:       "pwd",
		WorkingDir:    filepath.Dir(workspace),
		WorkspaceRoot: workspace,
	})
	if err != ErrWorkingDirectoryEscape {
		t.Fatalf("expected ErrWorkingDirectoryEscape, got %v", err)
	}
}

func TestSafeExecutorRunsCommandInsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:       executable,
		Args:          []string{"-test.run=TestHelperProcess", "--", "hello"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		Timeout:       time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if result.Stdout != "hello" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 1 {
		_, _ = os.Stdout.WriteString(args[1])
	}
	os.Exit(0)
}
