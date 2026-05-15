package workflowschedule

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptSummary(operation, workflowID, name, description string) string {
	return prompttemplate.MustRenderFS(promptFS, "prompt_summary.tmpl", map[string]any{
		"Operation":   strings.TrimSpace(operation),
		"WorkflowID":  strings.TrimSpace(workflowID),
		"Name":        strings.TrimSpace(name),
		"Description": strings.TrimSpace(description),
	})
}

func PromptWorkflowBundleChanged(workflowID string) string {
	return prompttemplate.MustRenderFS(promptFS, "prompt_workflow_bundle_changed.tmpl", map[string]any{
		"WorkflowID":       strings.TrimSpace(workflowID),
		"RequiredNextTool": WorkflowUpdateToolName,
	})
}
