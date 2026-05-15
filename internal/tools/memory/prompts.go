package memory

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptAddSummary(content string) string {
	return renderPrompt("prompt_add_summary.tmpl", map[string]any{
		"Content": strings.TrimSpace(content),
	})
}

func PromptSearchSummary(query string) string {
	return renderPrompt("prompt_search_summary.tmpl", map[string]any{
		"Query": strings.TrimSpace(query),
	})
}

func PromptUpdateSummary(memoryID string) string {
	return renderPrompt("prompt_update_summary.tmpl", map[string]any{
		"MemoryID": strings.TrimSpace(memoryID),
	})
}

func PromptForgetSummary(memoryIDs, tags, scope string) string {
	return renderPrompt("prompt_forget_summary.tmpl", map[string]any{
		"MemoryIDs": strings.TrimSpace(memoryIDs),
		"Tags":      strings.TrimSpace(tags),
		"Scope":     strings.TrimSpace(scope),
	})
}

func PromptListSummary(scope, kind string) string {
	return renderPrompt("prompt_list_summary.tmpl", map[string]any{
		"Scope": strings.TrimSpace(scope),
		"Kind":  strings.TrimSpace(kind),
	})
}

func renderPrompt(name string, data any) string {
	return prompttemplate.MustRenderFS(promptFS, name, data)
}
