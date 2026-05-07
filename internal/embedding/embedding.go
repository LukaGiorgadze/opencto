package embedding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

const (
	ProviderOpenAI     = "openai"
	DefaultOpenAIModel = "text-embedding-3-small"
	DefaultDimensions  = 1536
)

type Result struct {
	Embeddings [][]float32
	Model      string
	Dimensions int
}

type Embedder interface {
	Embed(ctx context.Context, inputs []string) (Result, error)
	Provider() string
	Model() string
	Dimensions() int
}

func MemoryText(memory domain.Memory) string {
	var builder strings.Builder
	if kind := strings.TrimSpace(memory.Kind); kind != "" {
		builder.WriteString("kind: ")
		builder.WriteString(kind)
		builder.WriteString("\n")
	}
	tags := cleanTags(memory.Tags)
	if len(tags) > 0 {
		builder.WriteString("tags: ")
		builder.WriteString(strings.Join(tags, ", "))
		builder.WriteString("\n")
	}
	builder.WriteString("content: ")
	builder.WriteString(strings.TrimSpace(memory.Content))
	return strings.TrimSpace(builder.String())
}

func ContentHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

func cleanTags(tags []string) []string {
	cleaned := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		cleaned = append(cleaned, tag)
	}
	sort.Strings(cleaned)
	return cleaned
}
