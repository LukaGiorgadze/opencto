package grep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeRequestUsesDefaultsWhenFieldsAreOmitted(t *testing.T) {
	t.Parallel()

	var req Request
	if err := json.Unmarshal([]byte(`{"pattern":"needle"}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	normalized, err := normalizeRequest(req)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !normalized.LineNumbers {
		t.Fatalf("expected omitted -n to use the default true behavior")
	}
	if normalized.HeadLimit != defaultHeadLimit {
		t.Fatalf("expected omitted head_limit to use default %d, got %d", defaultHeadLimit, normalized.HeadLimit)
	}
}

func TestRunUsesWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "example.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := NewSafeExecutor(nil).Run(context.Background(), Request{
		Pattern:    "needle",
		WorkingDir: workingDir,
		OutputMode: OutputModeContent,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if result.WorkingDirectory != workingDir {
		t.Fatalf("expected working directory %q, got %q", workingDir, result.WorkingDirectory)
	}
	if !strings.Contains(result.Stdout, "needle") {
		t.Fatalf("expected grep output to contain fixture, got %q", result.Stdout)
	}
}

func TestRunSearchesAbsolutePathOutsideWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	outsideDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outsideDir, "example.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := NewSafeExecutor(nil).Run(context.Background(), Request{
		Pattern:    "needle",
		Path:       outsideDir,
		WorkingDir: workingDir,
		OutputMode: OutputModeContent,
	})
	if err != nil {
		t.Fatalf("grep outside path: %v", err)
	}
	if !strings.Contains(result.Stdout, "needle") {
		t.Fatalf("expected grep output to contain fixture, got %q", result.Stdout)
	}
}

func TestRunExecutesBatchActions(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(workingDir, "example.txt"), []byte("needle\nthread\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	result, err := NewSafeExecutor(nil).Run(context.Background(), Request{
		WorkingDir: workingDir,
		Actions: []Action{
			{Pattern: "needle", OutputMode: OutputModeContent},
			{Pattern: "thread", OutputMode: OutputModeContent},
		},
	})
	if err != nil {
		t.Fatalf("grep batch: %v", err)
	}
	if len(result.Actions) != 2 {
		t.Fatalf("expected two action results, got %#v", result.Actions)
	}
	if !strings.Contains(result.Stdout, "needle") || !strings.Contains(result.Stdout, "thread") {
		t.Fatalf("expected batch output to include both matches, got %q", result.Stdout)
	}
}
