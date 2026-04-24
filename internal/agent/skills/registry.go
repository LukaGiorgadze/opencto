package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

type Skill struct {
	Name    string
	Path    string
	Content string
}

type Registry struct {
	root string
}

func NewRegistry(root string) *Registry {
	return &Registry{root: root}
}

func (r *Registry) Load(ctx context.Context, names []string) ([]Skill, error) {
	var skills []Skill
	for _, name := range names {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		path := filepath.Join(r.root, name+".md")
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		skills = append(skills, Skill{
			Name:    name,
			Path:    path,
			Content: string(content),
		})
	}
	return skills, nil
}

func (r *Registry) Discover(ctx context.Context) ([]Skill, error) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}
	return r.Load(ctx, names)
}
