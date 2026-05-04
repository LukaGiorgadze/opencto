package glob

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGlobMatchesRecursivePattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "main.go"), "package main\n", time.Unix(100, 0))
	writeFile(t, filepath.Join(root, "internal", "app.go"), "package internal\n", time.Unix(200, 0))
	writeFile(t, filepath.Join(root, "README.md"), "# docs\n", time.Unix(300, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Pattern: "**/*.go",
		Path:    root,
	})
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}

	expected := []string{
		filepath.Join(root, "internal", "app.go"),
		filepath.Join(root, "main.go"),
	}
	assertMatches(t, result.Matches, expected)
	if result.Root != root {
		t.Fatalf("expected root %q, got %q", root, result.Root)
	}
}

func TestGlobMatchesNestedPrefixPattern(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src", "app.ts"), "", time.Unix(100, 0))
	writeFile(t, filepath.Join(root, "src", "pages", "home.ts"), "", time.Unix(200, 0))
	writeFile(t, filepath.Join(root, "test", "app.ts"), "", time.Unix(300, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Pattern: "src/**/*.ts",
		Path:    root,
	})
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}

	expected := []string{
		filepath.Join(root, "src", "pages", "home.ts"),
		filepath.Join(root, "src", "app.ts"),
	}
	assertMatches(t, result.Matches, expected)
}

func TestGlobReturnsNewestMatchesFirst(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldest := filepath.Join(root, "old.txt")
	newest := filepath.Join(root, "new.txt")
	writeFile(t, oldest, "", time.Unix(100, 0))
	writeFile(t, newest, "", time.Unix(200, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Pattern: "*.txt",
		Path:    root,
	})
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}

	assertMatches(t, result.Matches, []string{newest, oldest})
}

func TestGlobValidatesRequest(t *testing.T) {
	t.Parallel()

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if _, err := executor.Run(context.Background(), Request{}); !errors.Is(err, ErrPatternRequired) {
		t.Fatalf("expected ErrPatternRequired, got %v", err)
	}
	if _, err := executor.Run(context.Background(), Request{Pattern: "*.go", Path: "undefined"}); !errors.Is(err, ErrPathRequired) {
		t.Fatalf("expected ErrPathRequired, got %v", err)
	}
}

func TestGlobCanUseFilePath(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file.txt")
	writeFile(t, file, "", time.Unix(100, 0))
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{Pattern: "*.txt", Path: file})
	if err != nil {
		t.Fatalf("glob file path: %v", err)
	}
	assertMatches(t, result.Matches, []string{file})
}

func TestGlobRejectsInvalidPattern(t *testing.T) {
	t.Parallel()

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := executor.Run(context.Background(), Request{
		Pattern: "[abc",
		Path:    t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "unterminated character class") {
		t.Fatalf("expected invalid pattern error, got %v", err)
	}
}

func writeFile(t *testing.T, path string, content string, modTime time.Time) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set file times: %v", err)
	}
}

func assertMatches(t *testing.T, got []string, expected []string) {
	t.Helper()

	if len(got) != len(expected) {
		t.Fatalf("expected %d matches, got %d: %#v", len(expected), len(got), got)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("match %d: expected %q, got %q", i, expected[i], got[i])
		}
	}
}
