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

func TestGlobUsesCwdForRelativePath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appDir := filepath.Join(root, "app")
	srcFile := filepath.Join(appDir, "src", "main.go")
	otherFile := filepath.Join(root, "src", "main.go")
	writeFile(t, srcFile, "", time.Unix(100, 0))
	writeFile(t, otherFile, "", time.Unix(200, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Cwd:     appDir,
		Pattern: "*.go",
		Path:    "src",
	})
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}

	assertMatches(t, result.Matches, []string{srcFile})
	if result.Root != filepath.Join(appDir, "src") {
		t.Fatalf("expected root %q, got %q", filepath.Join(appDir, "src"), result.Root)
	}
}

func TestGlobUsesCwdWhenPathIsEmpty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	appFile := filepath.Join(root, "app", "main.go")
	workspaceFile := filepath.Join(root, "main.go")
	writeFile(t, appFile, "", time.Unix(100, 0))
	writeFile(t, workspaceFile, "", time.Unix(200, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Cwd:     filepath.Join(root, "app"),
		Pattern: "*.go",
	})
	if err != nil {
		t.Fatalf("glob files: %v", err)
	}

	assertMatches(t, result.Matches, []string{appFile})
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

func TestGlobRunsBatchActions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	goFile := filepath.Join(root, "main.go")
	mdFile := filepath.Join(root, "README.md")
	writeFile(t, goFile, "", time.Unix(100, 0))
	writeFile(t, mdFile, "", time.Unix(200, 0))

	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Path: root,
		Actions: []Action{
			{Pattern: "*.go"},
			{Pattern: "*.md"},
		},
	})
	if err != nil {
		t.Fatalf("glob batch: %v", err)
	}
	if len(result.Actions) != 2 {
		t.Fatalf("expected two action results, got %#v", result.Actions)
	}
	assertMatches(t, result.Actions[0].Matches, []string{goFile})
	assertMatches(t, result.Actions[1].Matches, []string{mdFile})
	assertMatches(t, result.Matches, []string{goFile, mdFile})
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
