package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "new.txt")
	executor := NewSafeExecutor(nil)
	result, err := executor.Run(context.Background(), Request{
		FilePath: path,
		Content:  "hello OpenCTO\n",
	})
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
	if result.FilePath != path {
		t.Fatalf("expected file path %q, got %q", path, result.FilePath)
	}
	if result.BytesWritten != len("hello OpenCTO\n") {
		t.Fatalf("expected %d bytes written, got %d", len("hello OpenCTO\n"), result.BytesWritten)
	}
	if result.Overwritten {
		t.Fatalf("expected new file to report overwritten false")
	}
	assertFileContents(t, path, "hello OpenCTO\n")
}

func TestWriteCreatesParentDirectories(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nested", "dir", "file.txt")
	executor := NewSafeExecutor(nil)
	if _, err := executor.Run(context.Background(), Request{FilePath: path, Content: "created\n"}); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	assertFileContents(t, path, "created\n")
}

func TestWriteOverwritesExistingFile(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "before\n")
	executor := NewSafeExecutor(nil)
	result, err := executor.Run(context.Background(), Request{
		FilePath: path,
		Content:  "after\n",
	})
	if err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	if !result.Overwritten {
		t.Fatalf("expected existing file to report overwritten true")
	}
	assertFileContents(t, path, "after\n")
}

func TestWriteCanRequirePriorReadForOverwrite(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "before\n")
	executor := NewReadAwareSafeExecutor(nil, staticReadTracker{read: map[string]bool{}})
	_, err := executor.Run(context.Background(), Request{
		FilePath: path,
		Content:  "after\n",
	})
	if !errors.Is(err, ErrFileNotRead) {
		t.Fatalf("expected ErrFileNotRead, got %v", err)
	}
	assertFileContents(t, path, "before\n")
}

func TestWriteAllowsReadAwareNewFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "new.txt")
	executor := NewReadAwareSafeExecutor(nil, staticReadTracker{read: map[string]bool{}})
	if _, err := executor.Run(context.Background(), Request{FilePath: path, Content: "new\n"}); err != nil {
		t.Fatalf("write new file: %v", err)
	}
	assertFileContents(t, path, "new\n")
}

func TestWriteAllowsReadAwareOverwriteAfterRead(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "before\n")
	executor := NewReadAwareSafeExecutor(nil, staticReadTracker{read: map[string]bool{path: true}})
	if _, err := executor.Run(context.Background(), Request{FilePath: path, Content: "after\n"}); err != nil {
		t.Fatalf("rewrite file after read: %v", err)
	}
	assertFileContents(t, path, "after\n")
}

func TestWriteValidatesRequest(t *testing.T) {
	t.Parallel()

	executor := NewSafeExecutor(nil)
	if _, err := executor.Run(context.Background(), Request{}); !errors.Is(err, ErrFilePathRequired) {
		t.Fatalf("expected ErrFilePathRequired, got %v", err)
	}
	if _, err := executor.Run(context.Background(), Request{FilePath: "relative.txt", Content: "x"}); !errors.Is(err, ErrFilePathNotAbsolute) {
		t.Fatalf("expected ErrFilePathNotAbsolute, got %v", err)
	}
}

func TestWriteRejectsWorkspaceEscape(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	executor := NewSafeExecutor(nil)
	_, err := executor.Run(context.Background(), Request{
		FilePath:      outside,
		Content:       "outside\n",
		WorkspaceRoot: workspace,
	})
	if !errors.Is(err, ErrFilePathEscape) {
		t.Fatalf("expected ErrFilePathEscape, got %v", err)
	}
}

func TestWriteRejectsDirectoryPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	executor := NewSafeExecutor(nil)
	_, err := executor.Run(context.Background(), Request{
		FilePath: dir,
		Content:  "not a file\n",
	})
	if err == nil {
		t.Fatalf("expected directory write to fail")
	}
}

func TestWriteHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := filepath.Join(t.TempDir(), "file.txt")
	executor := NewSafeExecutor(nil)
	_, err := executor.Run(ctx, Request{FilePath: path, Content: "content\n"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected file not to be written, got stat error %v", statErr)
	}
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func assertFileContents(t *testing.T, path string, expected string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(content) != expected {
		t.Fatalf("unexpected file contents: %q", content)
	}
}

type staticReadTracker struct {
	read map[string]bool
}

func (t staticReadTracker) HasRead(filePath string) bool {
	return t.read[filePath]
}
