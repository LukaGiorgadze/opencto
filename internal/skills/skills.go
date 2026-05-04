package skills

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	DefaultDirName = "skills"
	SkillFileName  = "SKILL.md"
	MaxSkillBytes  = 64 * 1024
)

var (
	ErrInvalidID = errors.New("invalid skill id")
	ErrNotFound  = errors.New("skill not found")
)

type Summary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Path        string `json:"path"`
}

type Skill struct {
	Summary
	Content string `json:"content"`
}

func DefaultRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return DefaultDirName
	}
	return filepath.Join(wd, DefaultDirName)
}

func DefaultRoots() []string {
	return []string{DefaultRoot()}
}

func Discover(ctx context.Context, roots ...string) ([]Summary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(roots) == 0 {
		roots = DefaultRoots()
	}

	seen := map[string]bool{}
	var summaries []Summary
	for _, root := range roots {
		root = skillRoot(root)
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read skills directory: %w", err)
		}

		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() || !ValidID(entry.Name()) || seen[entry.Name()] {
				continue
			}
			skill, err := Load(ctx, root, entry.Name())
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(skill.Description) == "" {
				continue
			}
			seen[entry.Name()] = true
			summaries = append(summaries, skill.Summary)
		}
	}
	sort.Slice(summaries, func(i, j int) bool {
		return summaries[i].ID < summaries[j].ID
	})
	return summaries, nil
}

func LoadFromRoots(ctx context.Context, id string, roots ...string) (Skill, error) {
	if len(roots) == 0 {
		roots = DefaultRoots()
	}
	for _, root := range roots {
		skill, err := Load(ctx, root, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		return skill, err
	}
	return Skill{}, fmt.Errorf("%w: %s", ErrNotFound, id)
}

func Load(ctx context.Context, root, id string) (Skill, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Skill{}, err
	}
	id = strings.TrimSpace(id)
	if !ValidID(id) {
		return Skill{}, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}

	path := filepath.Join(skillRoot(root), id, SkillFileName)
	content, err := readSkillFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Skill{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Skill{}, err
	}
	summary := parseMarkdown(id, path, content)
	return Skill{
		Summary: summary,
		Content: content,
	}, nil
}

func ValidID(id string) bool {
	if id == "" || strings.HasPrefix(id, "-") || strings.HasSuffix(id, "-") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func Reminder(summaries []Summary) string {
	if len(summaries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<system-reminder>\n")
	builder.WriteString("The following skills are available for use with the Skill tool:\n\n")
	for _, summary := range summaries {
		description := strings.TrimSpace(summary.Description)
		if description == "" {
			description = strings.TrimSpace(summary.Name)
		}
		builder.WriteString("- ")
		builder.WriteString("`")
		builder.WriteString(summary.ID)
		builder.WriteString("`")
		if description != "" {
			builder.WriteString(": ")
			builder.WriteString(description)
		}
		builder.WriteString("\n")
	}
	builder.WriteString("</system-reminder>")
	return builder.String()
}

func skillRoot(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return DefaultRoot()
	}
	return filepath.Clean(root)
}

func readSkillFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, MaxSkillBytes+1))
	if err != nil {
		return "", fmt.Errorf("read skill file: %w", err)
	}
	if len(data) > MaxSkillBytes {
		return "", fmt.Errorf("skill file too large: %s", path)
	}
	return string(data), nil
}

func parseMarkdown(id, path, content string) Summary {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if summary, ok := parseFrontmatter(id, path, lines); ok {
		return summary
	}

	name := id
	start := 0
	for i, line := range lines {
		text := strings.TrimSpace(line)
		if strings.HasPrefix(text, "# ") {
			if parsed := strings.TrimSpace(strings.TrimPrefix(text, "# ")); parsed != "" {
				name = parsed
			}
			start = i + 1
			break
		}
	}

	description := firstParagraph(lines[start:])
	return Summary{
		ID:          id,
		Name:        name,
		Description: description,
		Path:        path,
	}
}

func parseFrontmatter(id, path string, lines []string) (Summary, bool) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return Summary{}, false
	}

	values := map[string]string{}
	for i := 1; i < len(lines); i++ {
		text := strings.TrimSpace(lines[i])
		if text == "---" {
			name := strings.TrimSpace(values["name"])
			if name == "" {
				name = id
			}
			return Summary{
				ID:          id,
				Name:        name,
				Description: strings.TrimSpace(values["description"]),
				Path:        path,
			}, true
		}
		key, value, ok := strings.Cut(text, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "name" || key == "description" {
			values[key] = value
		}
	}
	return Summary{}, false
}

func firstParagraph(lines []string) string {
	var parts []string
	for _, line := range lines {
		text := strings.TrimSpace(line)
		if text == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(text, "#") {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if len(parts) == 0 && !startsWithText(text) {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, " ")
}

func startsWithText(value string) bool {
	for _, r := range value {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	return false
}
