package tools

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptDefaultSummary(name string) string {
	return renderPrompt("prompt_default_summary.tmpl", map[string]any{
		"Name": strings.TrimSpace(name),
	})
}

func PromptResultExitCode(value string) string {
	return renderPrompt("prompt_result_exit_code.tmpl", map[string]any{
		"Value": strings.TrimSpace(value),
	})
}

func PromptResultOutput(value string) string {
	return renderPrompt("prompt_result_output.tmpl", map[string]any{
		"Value": strings.TrimSpace(value),
	})
}

func PromptResultError(value string) string {
	return renderPrompt("prompt_result_error.tmpl", map[string]any{
		"Value": strings.TrimSpace(value),
	})
}

func PromptCompressionRequestedAction(value string) string {
	return renderPrompt("prompt_compression_requested_action.tmpl", map[string]any{
		"Value": strings.TrimSpace(value),
	})
}

func PromptCompressionObservation(value string) string {
	return renderPrompt("prompt_compression_observation.tmpl", map[string]any{
		"Value": strings.TrimSpace(value),
	})
}

func renderPrompt(name string, data any) string {
	return prompttemplate.MustRenderFS(promptFS, name, data)
}
