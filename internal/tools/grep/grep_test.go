package grep

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestUnmarshalTreatsNullableDefaultsAsUnset(t *testing.T) {
	t.Parallel()

	var req Request
	if err := json.Unmarshal([]byte(`{
		"pattern": "needle",
		"path": null,
		"glob": null,
		"type": null,
		"output_mode": null,
		"-A": null,
		"-B": null,
		"-C": null,
		"-i": null,
		"-n": null,
		"multiline": null,
		"context": null,
		"head_limit": null,
		"offset": null
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	normalized, err := normalizeRequest(req)
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !normalized.LineNumbers {
		t.Fatalf("expected nullable -n to use the default true behavior")
	}
	if normalized.HeadLimit != defaultHeadLimit {
		t.Fatalf("expected nullable head_limit to use default %d, got %d", defaultHeadLimit, normalized.HeadLimit)
	}
}

func TestRunDefaultsToConfiguredWorkspace(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspaceRoot, "example.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	result, err := NewSafeExecutor(nil).Run(context.Background(), Request{
		Pattern:       "needle",
		WorkspaceRoot: workspaceRoot,
		OutputMode:    OutputModeContent,
	})
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if result.WorkingDirectory != workspaceRoot {
		t.Fatalf("expected working directory %q, got %q", workspaceRoot, result.WorkingDirectory)
	}
	if !strings.Contains(result.Stdout, "needle") {
		t.Fatalf("expected grep output to contain fixture, got %q", result.Stdout)
	}
}
