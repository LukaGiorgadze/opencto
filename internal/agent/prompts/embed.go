package prompts

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"
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
	source, err := Load(name)
	if err != nil {
		return "", err
	}

	tmpl, err := template.New(name).Option("missingkey=error").Parse(source)
	if err != nil {
		return "", fmt.Errorf("parse prompt template %q: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt template %q: %w", name, err)
	}
	return buf.String(), nil
}
