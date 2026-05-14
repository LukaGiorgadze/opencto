package edit

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptSummary(filePath string) string {
	return renderPrompt("prompt_summary.tmpl", map[string]any{
		"Path": strings.TrimSpace(filePath),
	})
}

func PromptCompactHistoryBody(filePath, replacements, bytesWritten string) string {
	return renderPrompt("prompt_compact_history_body.tmpl", map[string]any{
		"FilePath":     strings.TrimSpace(filePath),
		"Replacements": strings.TrimSpace(replacements),
		"BytesWritten": strings.TrimSpace(bytesWritten),
	})
}

func renderPrompt(name string, data any) string {
	return prompttemplate.MustRenderFS(promptFS, name, data)
}
