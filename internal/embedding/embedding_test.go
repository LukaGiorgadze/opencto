package embedding

import (
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestMemoryTextUsesCanonicalFields(t *testing.T) {
	t.Parallel()

	text := MemoryText(domain.Memory{
		ID:      "memory-1",
		Kind:    "preference",
		Tags:    []string{"SQLite", "storage", "storage"},
		Content: "Use SQLite for local development.",
	})
	if !strings.Contains(text, "kind: preference") ||
		!strings.Contains(text, "tags: sqlite, storage") ||
		!strings.Contains(text, "content: Use SQLite for local development.") {
		t.Fatalf("unexpected memory text:\n%s", text)
	}
	if strings.Contains(text, "memory-1") {
		t.Fatalf("memory text should not include ids:\n%s", text)
	}
}
