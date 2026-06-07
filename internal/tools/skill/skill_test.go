package skill

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/skills"
)

func TestSafeExecutorLoadsSkillMarkdown(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "go-testing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	content := "# Go Testing\n\nUse when testing Go code.\n"
	if err := os.WriteFile(filepath.Join(dir, skills.SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}

	result, err := NewSafeExecutor(root).Run(context.Background(), Request{SkillID: "go-testing"})
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if result.SkillID != "go-testing" || result.Name != "Go Testing" {
		t.Fatalf("unexpected skill result: %#v", result)
	}
	if !strings.Contains(result.Content, "Use when testing Go code.") {
		t.Fatalf("skill content was not returned: %#v", result)
	}
}
