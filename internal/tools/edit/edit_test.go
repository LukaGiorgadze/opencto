package edit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEditReplacesUniqueString(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "hello world\n")
	result, err := Edit(Request{
		FilePath:  path,
		OldString: "world",
		NewString: "OpenCTO",
	})
	if err != nil {
		t.Fatalf("edit file: %v", err)
	}
	if result.Replacements != 1 {
		t.Fatalf("expected one replacement, got %d", result.Replacements)
	}
	assertFileContents(t, path, "hello OpenCTO\n")
}

func TestEditRejectsMissingOldString(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "alpha\n")
	_, err := Edit(Request{
		FilePath:  path,
		OldString: "beta",
		NewString: "gamma",
	})
	if !errors.Is(err, ErrOldStringNotFound) {
		t.Fatalf("expected ErrOldStringNotFound, got %v", err)
	}
	assertFileContents(t, path, "alpha\n")
}

func TestEditRequiresUniqueOldStringByDefault(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "name name\n")
	_, err := Edit(Request{
		FilePath:  path,
		OldString: "name",
		NewString: "title",
	})
	if !errors.Is(err, ErrOldStringNotUnique) {
		t.Fatalf("expected ErrOldStringNotUnique, got %v", err)
	}
	assertFileContents(t, path, "name name\n")
}

func TestEditReplacesAllOccurrences(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "name name\n")
	result, err := Edit(Request{
		FilePath:   path,
		OldString:  "name",
		NewString:  "title",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("edit file: %v", err)
	}
	if result.Replacements != 2 {
		t.Fatalf("expected two replacements, got %d", result.Replacements)
	}
	assertFileContents(t, path, "title title\n")
}

func TestEditValidatesRequest(t *testing.T) {
	workspaceRoot := t.TempDir()
	t.Setenv("OPENCTO_WORKSPACE", workspaceRoot)
	relativePath := filepath.Join(workspaceRoot, "relative.txt")
	if err := os.WriteFile(relativePath, []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write relative fixture: %v", err)
	}
	result, err := Edit(Request{FilePath: "relative.txt", OldString: "a", NewString: "b"})
	if err != nil {
		t.Fatalf("edit relative file: %v", err)
	}
	if result.FilePath != relativePath {
		t.Fatalf("expected resolved path %q, got %q", relativePath, result.FilePath)
	}

	path := filepath.Join(t.TempDir(), "file.txt")
	if _, err := Edit(Request{FilePath: path, OldString: "", NewString: "b"}); !errors.Is(err, ErrOldStringRequired) {
		t.Fatalf("expected ErrOldStringRequired, got %v", err)
	}
	if _, err := Edit(Request{FilePath: path, OldString: "a", NewString: "a"}); !errors.Is(err, ErrStringsMatch) {
		t.Fatalf("expected ErrStringsMatch, got %v", err)
	}
}

func TestEditCanRequirePriorRead(t *testing.T) {
	t.Parallel()

	path := writeTempFile(t, "before\n")
	tool := NewReadAwareTool(staticReadTracker{read: map[string]bool{}})
	_, err := tool.Run(context.Background(), Request{
		FilePath:  path,
		OldString: "before",
		NewString: "after",
	})
	if !errors.Is(err, ErrFileNotRead) {
		t.Fatalf("expected ErrFileNotRead, got %v", err)
	}
	assertFileContents(t, path, "before\n")
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
