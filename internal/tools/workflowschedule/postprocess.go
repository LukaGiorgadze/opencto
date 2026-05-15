package workflowschedule

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/tools/postprocess"
	"github.com/opencto/opencto/internal/workflowbundle"
)

type WorkflowBundlePostProcessor struct{}

func NewWorkflowBundlePostProcessor() WorkflowBundlePostProcessor {
	return WorkflowBundlePostProcessor{}
}

func (WorkflowBundlePostProcessor) ProcessToolResult(_ context.Context, req postprocess.Request, result postprocess.Result) postprocess.Result {
	if req.Status != domain.ExecutionStatusSucceeded {
		return result
	}
	if req.Tool != domain.ToolTypeEdit && req.Tool != domain.ToolTypeWrite {
		return result
	}
	workflowID, ok := WorkflowIDFromBundlePath(req.WorkspaceRoot, toolResultFilePath(req.Input, result.Metadata))
	if !ok {
		return result
	}

	result.Metadata = postprocess.EnsureMetadata(result.Metadata)
	result.Metadata["workflow_bundle_changed"] = "true"
	result.Metadata["workflow_id"] = workflowID
	result.Metadata["required_next_tool"] = WorkflowUpdateToolName

	result.Observation = postprocess.AppendObservationNote(result.Observation, PromptWorkflowBundleChanged(workflowID))
	return result
}

func WorkflowIDFromBundlePath(workspaceRoot, filePath string) (string, bool) {
	path := strings.TrimSpace(filePath)
	if path == "" {
		return "", false
	}
	root := strings.TrimSpace(workspaceRoot)
	if root != "" && !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	workflowsDir, err := workflowbundle.WorkflowsDir(root)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(workflowsDir, filepath.Clean(path))
	if err != nil || rel == "." {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "../") || rel == ".." || strings.HasPrefix(rel, "/") {
		return "", false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 || parts[1] == ".git" {
		return "", false
	}
	if workflowBundleRuntimePath(strings.Join(parts[1:], "/")) {
		return "", false
	}
	workflowID, err := workflowbundle.NormalizeWorkflowID(parts[0])
	if err != nil {
		return "", false
	}
	return workflowID, true
}

func workflowBundleRuntimePath(rel string) bool {
	rel = filepath.ToSlash(filepath.Clean(strings.TrimSpace(rel)))
	if rel == "." || rel == "" {
		return true
	}
	if rel == ".git" || strings.HasPrefix(rel, ".git/") {
		return true
	}
	if rel == ".DS_Store" || strings.HasSuffix(rel, ".log") {
		return true
	}
	for _, part := range strings.Split(rel, "/") {
		if part == ".DS_Store" || workflowBundleIgnoredDir(part) {
			return true
		}
	}
	return false
}

func workflowBundleIgnoredDir(name string) bool {
	switch name {
	case "data", "node_modules", "dist", "build", "target", "venv", ".venv", "__pycache__", ".pytest_cache", ".cache", "tmp", "temp", "output":
		return true
	default:
		return false
	}
}

func toolResultFilePath(input json.RawMessage, metadata map[string]string) string {
	if path := strings.TrimSpace(metadata["file_path"]); path != "" {
		return path
	}
	var payload struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(input, &payload); err != nil {
		return ""
	}
	return payload.FilePath
}
