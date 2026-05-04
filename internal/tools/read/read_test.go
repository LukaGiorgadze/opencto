package read

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFormatsFileWithLineNumbers(t *testing.T) {
	t.Parallel()

	path := writeReadTempFile(t, "alpha\nbeta\n")
	result, err := newReadTestExecutor().Run(context.Background(), Request{FilePath: path})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	expected := "     1\talpha\n     2\tbeta\n"
	if result.Content != expected {
		t.Fatalf("unexpected content:\n%s", result.Content)
	}
	if result.LinesRead != 2 || result.TotalLines != 2 || result.Truncated {
		t.Fatalf("unexpected read metadata: %#v", result)
	}
	if result.BytesRead != len("alpha\nbeta\n") {
		t.Fatalf("expected byte count, got %#v", result)
	}
}

func TestReadAppliesOffsetAndLimit(t *testing.T) {
	t.Parallel()

	path := writeReadTempFile(t, "one\ntwo\nthree\nfour\n")
	result, err := newReadTestExecutor().Run(context.Background(), Request{
		FilePath: path,
		Offset:   1,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	expected := "     2\ttwo\n     3\tthree\n"
	if result.Content != expected {
		t.Fatalf("unexpected content:\n%s", result.Content)
	}
	if result.LinesRead != 2 || result.TotalLines != 4 || !result.Truncated {
		t.Fatalf("unexpected read metadata: %#v", result)
	}
}

func TestReadValidatesRequest(t *testing.T) {
	executor := newReadTestExecutor()
	if _, err := executor.Run(context.Background(), Request{}); !errors.Is(err, ErrFilePathRequired) {
		t.Fatalf("expected ErrFilePathRequired, got %v", err)
	}

	workspaceRoot := t.TempDir()
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve cwd: %v", err)
	}
	if err := os.Chdir(workspaceRoot); err != nil {
		t.Fatalf("change cwd: %v", err)
	}
	defer func() {
		if err := os.Chdir(oldCwd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	relativePath := filepath.Join(workspaceRoot, "relative.txt")
	if err := os.WriteFile(relativePath, []byte("relative\n"), 0o644); err != nil {
		t.Fatalf("write relative fixture: %v", err)
	}
	result, err := executor.Run(context.Background(), Request{FilePath: "relative.txt"})
	if err != nil {
		t.Fatalf("read relative file: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(relativePath)
	if err != nil {
		t.Fatalf("resolve expected path: %v", err)
	}
	gotPath, err := filepath.EvalSymlinks(result.FilePath)
	if err != nil {
		t.Fatalf("resolve actual path: %v", err)
	}
	if gotPath != wantPath {
		t.Fatalf("expected resolved path %q, got %q", wantPath, gotPath)
	}

	path := filepath.Join(t.TempDir(), "file.txt")
	if _, err := executor.Run(context.Background(), Request{FilePath: path, Offset: -1}); !errors.Is(err, ErrOffsetInvalid) {
		t.Fatalf("expected ErrOffsetInvalid, got %v", err)
	}
	if _, err := executor.Run(context.Background(), Request{FilePath: path, Limit: -1}); !errors.Is(err, ErrLimitInvalid) {
		t.Fatalf("expected ErrLimitInvalid, got %v", err)
	}
}

func TestReadRejectsDirectories(t *testing.T) {
	t.Parallel()

	_, err := newReadTestExecutor().Run(context.Background(), Request{FilePath: t.TempDir()})
	if err == nil {
		t.Fatalf("expected directory read to fail")
	}
}

func TestReadHonorsContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := writeReadTempFile(t, "content\n")
	_, err := newReadTestExecutor().Run(ctx, Request{FilePath: path})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func newReadTestExecutor() *SafeExecutor {
	return NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func writeReadTempFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}
