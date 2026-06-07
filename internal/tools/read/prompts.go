package read

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

func PromptCompressionFile(value string) string {
	return renderValuePrompt("prompt_compression_file.tmpl", value)
}

func PromptCompressionLines(value string) string {
	return renderValuePrompt("prompt_compression_lines.tmpl", value)
}

func PromptCompressionBytes(value string) string {
	return renderValuePrompt("prompt_compression_bytes.tmpl", value)
}

func PromptCompressionTruncated(value string) string {
	return renderValuePrompt("prompt_compression_truncated.tmpl", value)
}

func PromptCompressionContentOmitted() string {
	return renderPrompt("prompt_compression_content_omitted.tmpl", nil)
}

func renderValuePrompt(name string, value string) string {
	return renderPrompt(name, map[string]any{"Value": strings.TrimSpace(value)})
}

func renderPrompt(name string, data any) string {
	return prompttemplate.MustRenderFS(promptFS, name, data)
}
