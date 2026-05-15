package grep

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptSummary(pattern, path string) string {
	return prompttemplate.MustRenderFS(promptFS, "prompt_summary.tmpl", map[string]any{
		"Pattern": strings.TrimSpace(pattern),
		"Path":    strings.TrimSpace(path),
	})
}
