package git

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

type ADRWriter struct {
	root string
}

func NewADRWriter(root string) *ADRWriter {
	return &ADRWriter{root: root}
}

func (w *ADRWriter) WriteSummary(_ context.Context, projectID, title, summary string, details []string) (domain.ADR, error) {
	if err := os.MkdirAll(w.root, 0o755); err != nil {
		return domain.ADR{}, err
	}

	nextNumber, err := w.nextNumber()
	if err != nil {
		return domain.ADR{}, err
	}

	id, err := domain.NewID()
	if err != nil {
		return domain.ADR{}, err
	}

	filename := fmt.Sprintf("%04d-%s.md", nextNumber, slug(title))
	path := filepath.Join(w.root, filename)
	createdAt := time.Now().UTC()

	var body strings.Builder
	body.WriteString("# ")
	body.WriteString(title)
	body.WriteString("\n\n")
	body.WriteString("Date: ")
	body.WriteString(createdAt.Format(time.RFC3339))
	body.WriteString("\n\n")
	body.WriteString("## Summary\n")
	body.WriteString(summary)
	body.WriteString("\n")
	if len(details) > 0 {
		body.WriteString("\n## Details\n")
		for _, detail := range details {
			body.WriteString("- ")
			body.WriteString(detail)
			body.WriteString("\n")
		}
	}

	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		return domain.ADR{}, err
	}

	return domain.ADR{
		ID:        id,
		ProjectID: projectID,
		Title:     title,
		Summary:   summary,
		Path:      path,
		CreatedAt: createdAt,
	}, nil
}

func (w *ADRWriter) nextNumber() (int, error) {
	entries, err := os.ReadDir(w.root)
	if err != nil {
		if os.IsNotExist(err) {
			return 1, nil
		}
		return 0, err
	}

	var numbers []int
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < 4 {
			continue
		}
		n, err := strconv.Atoi(name[:4])
		if err == nil {
			numbers = append(numbers, n)
		}
	}
	sort.Ints(numbers)
	if len(numbers) == 0 {
		return 1, nil
	}
	return numbers[len(numbers)-1] + 1, nil
}

func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, " ", "-")
	value = strings.ReplaceAll(value, "/", "-")
	return value
}
