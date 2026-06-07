package prompttemplate

import (
	"bytes"
	"fmt"
	"io/fs"
	"text/template"
)

func RenderFS(fsys fs.FS, name string, data any) (string, error) {
	source, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", fmt.Errorf("load prompt template %q: %w", name, err)
	}

	tmpl, err := template.New(name).Option("missingkey=error").Parse(string(source))
	if err != nil {
		return "", fmt.Errorf("parse prompt template %q: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render prompt template %q: %w", name, err)
	}
	return buf.String(), nil
}

func RenderFSWithPatterns(fsys fs.FS, name string, data any, patterns ...string) (string, error) {
	tmpl, err := template.New(name).Option("missingkey=error").ParseFS(fsys, patterns...)
	if err != nil {
		return "", fmt.Errorf("parse prompt template %q: %w", name, err)
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render prompt template %q: %w", name, err)
	}
	return buf.String(), nil
}

func MustRenderFS(fsys fs.FS, name string, data any) string {
	rendered, err := RenderFS(fsys, name, data)
	if err != nil {
		panic(err)
	}
	return rendered
}

func MustRenderFSWithPatterns(fsys fs.FS, name string, data any, patterns ...string) string {
	rendered, err := RenderFSWithPatterns(fsys, name, data, patterns...)
	if err != nil {
		panic(err)
	}
	return rendered
}
