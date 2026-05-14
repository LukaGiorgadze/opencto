package exec

import (
	"embed"
	"strings"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed prompt_*.tmpl
var promptFS embed.FS

func PromptCompressionResultCode(value string) string {
	return renderValuePrompt("prompt_compression_result_code.tmpl", value)
}

func PromptCompressionStdoutLogPath(value string) string {
	return renderValuePrompt("prompt_compression_stdout_log_path.tmpl", value)
}

func PromptCompressionStderrLogPath(value string) string {
	return renderValuePrompt("prompt_compression_stderr_log_path.tmpl", value)
}

func PromptCompressionOutputTruncated(value string) string {
	return renderValuePrompt("prompt_compression_output_truncated.tmpl", value)
}

func renderValuePrompt(name string, value string) string {
	return prompttemplate.MustRenderFS(promptFS, name, map[string]any{
		"Value": strings.TrimSpace(value),
	})
}
