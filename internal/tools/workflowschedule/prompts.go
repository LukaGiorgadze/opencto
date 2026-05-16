package workflowschedule

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl workflow_template.yml
var promptFS embed.FS

func PromptSummary(operation, workflowID, name, description string) string {
	return prompttemplate.MustRenderFS(promptFS, "prompt_summary.tmpl", map[string]any{
		"Operation":   strings.TrimSpace(operation),
		"WorkflowID":  strings.TrimSpace(workflowID),
		"Name":        strings.TrimSpace(name),
		"Description": strings.TrimSpace(description),
	})
}

func PromptAuthoringAgent(operation, workflowID, workflowPath, userPrompt, commitMessage string) string {
	return prompttemplate.MustRenderFSWithPatterns(promptFS, "prompt_authoring_agent.tmpl", map[string]any{
		"Operation":     strings.TrimSpace(operation),
		"WorkflowID":    strings.TrimSpace(workflowID),
		"WorkflowPath":  strings.TrimSpace(workflowPath),
		"UserPrompt":    strings.TrimSpace(userPrompt),
		"CommitMessage": strings.TrimSpace(commitMessage),
	}, "prompt_authoring_agent.tmpl", "workflow_template.yml")
}
