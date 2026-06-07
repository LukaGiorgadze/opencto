package skills

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMarkdownSkills(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSkill(t, root, "go-testing", "# Go Testing\n\nUse when adding or fixing Go tests.\n\n## Workflow\nRun go test.\n")
	writeSkill(t, root, "code-review", "# Code Review\n\nUse when reviewing code changes.\n")
	if err := os.Mkdir(filepath.Join(root, "Bad_ID"), 0o755); err != nil {
		t.Fatalf("mkdir invalid skill: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "missing-file"), 0o755); err != nil {
		t.Fatalf("mkdir missing skill: %v", err)
	}

	summaries, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("discover skills: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("expected two skills, got %#v", summaries)
	}
	if summaries[0].ID != "code-review" || summaries[1].ID != "go-testing" {
		t.Fatalf("expected sorted skill ids, got %#v", summaries)
	}
	if summaries[1].Name != "Go Testing" {
		t.Fatalf("unexpected skill name: %q", summaries[1].Name)
	}
	if summaries[1].Description != "Use when adding or fixing Go tests." {
		t.Fatalf("unexpected skill description: %q", summaries[1].Description)
	}
}

func TestDiscoverUsesFrontmatterWhenPresent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSkill(t, root, "go-testing", "---\nname: go-testing\ndescription: Use when adding, fixing, or debugging Go tests.\n---\n\n# Go Testing\n\nDetailed workflow.\n")

	summaries, err := Discover(context.Background(), root)
	if err != nil {
		t.Fatalf("discover skills: %v", err)
	}
	if len(summaries) != 1 {
		t.Fatalf("expected one skill, got %#v", summaries)
	}
	if summaries[0].Name != "go-testing" {
		t.Fatalf("expected frontmatter name, got %q", summaries[0].Name)
	}
	if summaries[0].Description != "Use when adding, fixing, or debugging Go tests." {
		t.Fatalf("expected frontmatter description, got %q", summaries[0].Description)
	}
}

func TestDefaultRootUsesSkills(t *testing.T) {
	t.Parallel()

	root := DefaultRoot()
	if !strings.HasSuffix(filepath.ToSlash(root), "/skills") {
		t.Fatalf("expected default root to use skills, got %q", root)
	}
}

func TestRuntimeRootsUsesWorkspaceSkills(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()

	roots := RuntimeRoots(workspaceRoot)
	want := []string{
		filepath.Join(workspaceRoot, "skills"),
		filepath.Join(workspaceRoot, ".agents", "skills"),
	}
	if strings.Join(roots, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unexpected roots:\nwant: %#v\n got: %#v", want, roots)
	}
}

func TestDiscoverRuntimeRootsIncludesWorkspaceSkills(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "workspace-root-skill", "# Workspace Root Skill\n\nUse when handling workspace root work.\n")
	writeSkill(t, filepath.Join(workspaceRoot, ".agents", "skills"), "workspace-skill", "# Workspace Skill\n\nUse when handling workspace-specific work.\n")

	summaries, err := Discover(context.Background(), RuntimeRoots(workspaceRoot)...)
	if err != nil {
		t.Fatalf("discover runtime skills: %v", err)
	}
	got := map[string]string{}
	for _, summary := range summaries {
		got[summary.ID] = summary.Path
	}
	if len(got) != 2 {
		t.Fatalf("expected two skills, got %#v", summaries)
	}
	if !strings.Contains(filepath.ToSlash(got["workspace-root-skill"]), "/skills/workspace-root-skill/SKILL.md") {
		t.Fatalf("expected workspace root skill path, got %#v", got)
	}
	if !strings.Contains(filepath.ToSlash(got["workspace-skill"]), "/.agents/skills/workspace-skill/SKILL.md") {
		t.Fatalf("expected workspace .agents skill path, got %#v", got)
	}
}

func TestLoadFromRuntimeRootsUsesWorkspaceSkill(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	writeSkill(t, filepath.Join(workspaceRoot, "skills"), "go-testing", "# Workspace Go Testing\n\nUse the workspace-defined workflow.\n")

	skill, err := LoadFromRoots(context.Background(), "go-testing", RuntimeRoots(workspaceRoot)...)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if skill.Name != "Workspace Go Testing" || !strings.Contains(skill.Content, "workspace-defined") {
		t.Fatalf("expected workspace skill to shadow built-in skill, got %#v", skill)
	}
	if !strings.Contains(filepath.ToSlash(skill.Path), "/skills/go-testing/SKILL.md") {
		t.Fatalf("unexpected skill path: %q", skill.Path)
	}
}

func TestLoadSkillRejectsInvalidID(t *testing.T) {
	t.Parallel()

	_, err := Load(context.Background(), t.TempDir(), "../bad")
	if err == nil || !strings.Contains(err.Error(), ErrInvalidID.Error()) {
		t.Fatalf("expected invalid id error, got %v", err)
	}
}

func TestReminderFormatsSkillList(t *testing.T) {
	t.Parallel()

	reminder := Reminder([]Summary{{
		ID:          "go-testing",
		Name:        "Go Testing",
		Description: "Use when adding or fixing Go tests.",
	}})
	if !strings.Contains(reminder, "<system-reminder>") ||
		!strings.Contains(reminder, "loadable with the `LoadSkill` tool") ||
		!strings.Contains(reminder, "Only call `LoadSkill` with one of these exact top-level skill IDs") ||
		!strings.Contains(reminder, "read them with the Read tool by file path") ||
		!strings.Contains(reminder, "- `go-testing`: Use when adding or fixing Go tests.") ||
		strings.Contains(reminder, "SKILL.md") {
		t.Fatalf("unexpected reminder:\n%s", reminder)
	}
}

func writeSkill(t *testing.T, root, id, content string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
}
