package workflowschedule

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/tools/postprocess"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func TestWorkflowBundlePostProcessorAnnotatesWorkflowSourceChange(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, ".opencto", "workflows", "daily-check", "src", "check.py")
	result := NewWorkflowBundlePostProcessor().ProcessToolResult(context.Background(), postprocess.Request{
		WorkspaceRoot: workspaceRoot,
		Tool:          domain.ToolTypeEdit,
		Status:        domain.ExecutionStatusSucceeded,
	}, postprocess.Result{
		Observation: "edited: " + filePath,
		Metadata:    map[string]string{"file_path": filePath},
	})

	if !strings.Contains(result.Observation, "workflow_bundle_changed: true") ||
		!strings.Contains(result.Observation, "workflow_id: daily-check") ||
		!strings.Contains(result.Observation, "required_next_tool: WorkflowUpdate") {
		t.Fatalf("expected workflow bundle notice, got %q", result.Observation)
	}
	if result.Metadata["workflow_bundle_changed"] != "true" ||
		result.Metadata["workflow_id"] != "daily-check" ||
		result.Metadata["required_next_tool"] != WorkflowUpdateToolName {
		t.Fatalf("expected workflow bundle metadata, got %#v", result.Metadata)
	}
}

func TestWorkflowBundlePostProcessorAnnotatesWorkflowManifestChangeFromInput(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, ".opencto", "workflows", "daily-check", workflowbundle.ManifestFilename)
	input, err := json.Marshal(map[string]string{"file_path": filePath})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	result := NewWorkflowBundlePostProcessor().ProcessToolResult(context.Background(), postprocess.Request{
		WorkspaceRoot: workspaceRoot,
		Tool:          domain.ToolTypeWrite,
		Status:        domain.ExecutionStatusSucceeded,
		Input:         input,
	}, postprocess.Result{Observation: "wrote manifest"})

	if !strings.Contains(result.Observation, "workflow_bundle_changed: true") ||
		result.Metadata["workflow_id"] != "daily-check" {
		t.Fatalf("expected workflow bundle notice, got result=%#v", result)
	}
}

func TestWorkflowBundlePostProcessorIgnoresNonWorkflowFile(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, "src", "app.py")
	result := NewWorkflowBundlePostProcessor().ProcessToolResult(context.Background(), postprocess.Request{
		WorkspaceRoot: workspaceRoot,
		Tool:          domain.ToolTypeEdit,
		Status:        domain.ExecutionStatusSucceeded,
	}, postprocess.Result{
		Observation: "edited: " + filePath,
		Metadata:    map[string]string{"file_path": filePath},
	})

	if strings.Contains(result.Observation, "workflow_bundle_changed") {
		t.Fatalf("did not expect workflow bundle notice, got %q", result.Observation)
	}
	if _, ok := result.Metadata["workflow_bundle_changed"]; ok {
		t.Fatalf("did not expect workflow bundle metadata, got %#v", result.Metadata)
	}
}

func TestWorkflowBundlePostProcessorDoesNotMarkFailedMutationDirty(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, ".opencto", "workflows", "daily-check", "src", "check.py")
	result := NewWorkflowBundlePostProcessor().ProcessToolResult(context.Background(), postprocess.Request{
		WorkspaceRoot: workspaceRoot,
		Tool:          domain.ToolTypeEdit,
		Status:        domain.ExecutionStatusFailed,
		Error:         "old_string was not found",
	}, postprocess.Result{
		Observation: "error: old_string was not found",
		Metadata:    map[string]string{"file_path": filePath},
	})

	if strings.Contains(result.Observation, "workflow_bundle_changed") {
		t.Fatalf("did not expect failed mutation to mark workflow dirty, got %q", result.Observation)
	}
	if _, ok := result.Metadata["workflow_bundle_changed"]; ok {
		t.Fatalf("did not expect workflow bundle metadata, got %#v", result.Metadata)
	}
}

func TestWorkflowIDFromBundlePathIgnoresGitInternals(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	filePath := filepath.Join(workspaceRoot, ".opencto", "workflows", "daily-check", ".git", "HEAD")
	if workflowID, ok := WorkflowIDFromBundlePath(workspaceRoot, filePath); ok {
		t.Fatalf("did not expect git internals to resolve as workflow files, got %q", workflowID)
	}
}
