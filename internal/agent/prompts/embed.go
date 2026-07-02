package prompts

import (
	"embed"
	"fmt"

	"github.com/opencto/opencto/internal/prompttemplate"
)

//go:embed *.tmpl
var fs embed.FS

func Load(name string) (string, error) {
	data, err := fs.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("load prompt %q: %w", name, err)
	}
	return string(data), nil
}

func Render(name string, data any) (string, error) {
	return prompttemplate.RenderFS(fs, name, data)
}

func RenderWithIncludes(name string, data any, includes ...string) (string, error) {
	patterns := append([]string{name}, includes...)
	return prompttemplate.RenderFSWithPatterns(fs, name, data, patterns...)
}

func MustRender(name string, data any) string {
	return prompttemplate.MustRenderFS(fs, name, data)
}
