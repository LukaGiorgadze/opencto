package shell

import (
	"context"
	"io"
	"log/slog"
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
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:       "sh",
		Args:          []string{"-c", "printf hello"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		Timeout:       time.Second,
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if result.Stdout != "hello" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}
