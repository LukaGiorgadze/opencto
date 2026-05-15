package skill

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptSummary(skillID string) string {
	return prompttemplate.MustRenderFS(promptFS, "prompt_summary.tmpl", map[string]any{
		"SkillID": strings.TrimSpace(skillID),
	})
}
