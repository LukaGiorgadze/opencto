package activities

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/memory/sqlite"
)

func TestSummarizeObservationTruncatesLongStdout(t *testing.T) {
	stdout := strings.Repeat("file.go\n", 900)

	summary := summarizeObservation(stdout, "", nil)
	if summary == strings.TrimSpace(stdout) {
		t.Fatalf("expected long stdout to be summarized, got original output")
	}
	if !strings.Contains(summary, "output truncated") {
		t.Fatalf("expected truncation notice, got %q", summary)
	}
}

func TestSummarizeObservationUsesStderrWhenStdoutEmpty(t *testing.T) {
	summary := summarizeObservation("", "command failed\nwith stderr", nil)
	if summary != "command failed\nwith stderr" {
		t.Fatalf("unexpected stderr summary: %q", summary)
	}
}

func TestLoadContextResolvesRecentContradictionsAndCarriesConversationForward(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "memory.db"), "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 4, 23, 17, 31, 0, 0, time.UTC)
	events := []domain.Event{
		{
			ID:          "event-source",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "you should remove git and create it in hello-world",
			CreatedAt:   base,
		},
		{
			ID:          "event-path",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "`/Users/luka/projects/opencto/hello-world`",
			CreatedAt:   base.Add(10 * time.Second),
		},
		{
			ID:          "event-yes",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "yes",
			CreatedAt:   base.Add(20 * time.Second),
		},
		{
			ID:          "event-current",
			ProjectID:   "default",
			Kind:        domain.EventKindMessage,
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "do it",
			CreatedAt:   base.Add(30 * time.Second),
		},
	}
	for _, event := range events {
		if err := store.Append(context.Background(), event); err != nil {
			t.Fatalf("append event %s: %v", event.ID, err)
		}
	}

	question := domain.MemoryFact{
		ID:        "memory-question",
		ProjectID: "default",
		Category:  domain.MemoryCategoryConversation,
		Key:       "memory-question",
		Value:     "I have the hello-world path. Should I initialize git there and remove the root .git in /Users/luka/projects/opencto?",
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeDiscord),
			SourceID:   "channel-1",
			Actor:      "opencto",
			ObservedAt: base.Add(15 * time.Second),
		},
		Metadata: map[string]string{
			"speaker": "assistant",
		},
		CreatedAt: base.Add(15 * time.Second),
		UpdatedAt: base.Add(15 * time.Second),
	}
	if err := store.UpsertFact(context.Background(), question); err != nil {
		t.Fatalf("upsert conversation fact: %v", err)
	}

	contradiction := domain.PendingContradiction{
		ID:        "contradiction-1",
		ProjectID: "default",
		Status:    domain.ContradictionStatusOpen,
		Topic:     "git repo location",
		Metadata: map[string]string{
			"event_id":   "event-source",
			"channel_id": "channel-1",
		},
		CreatedAt: base.Add(5 * time.Second),
		UpdatedAt: base.Add(5 * time.Second),
	}
	if err := store.UpsertContradiction(context.Background(), contradiction); err != nil {
		t.Fatalf("upsert contradiction: %v", err)
	}

	activities := Activities{
		Store:   store,
		Project: domain.Project{ID: "default", Name: "OpenCTO"},
	}

	loaded, err := activities.LoadContext(context.Background(), events[len(events)-1])
	if err != nil {
		t.Fatalf("load context: %v", err)
	}

	if len(loaded.OpenContradictions) != 0 {
		t.Fatalf("expected contradiction to be auto-resolved, got %d open contradictions", len(loaded.OpenContradictions))
	}

	values := make([]string, 0, len(loaded.ConversationMemory))
	for _, item := range loaded.ConversationMemory {
		values = append(values, item.Value)
	}
	joined := strings.Join(values, "\n")
	if !strings.Contains(joined, "Should I initialize git there") {
		t.Fatalf("expected recent assistant clarification in conversation memory, got %q", joined)
	}
	if !strings.Contains(joined, "/Users/luka/projects/opencto/hello-world") {
		t.Fatalf("expected recent path answer in conversation memory, got %q", joined)
	}

	openContradictions, err := store.ListOpen(context.Background(), "default")
	if err != nil {
		t.Fatalf("list open contradictions: %v", err)
	}
	if len(openContradictions) != 0 {
		t.Fatalf("expected store contradiction to be resolved, got %d open contradictions", len(openContradictions))
	}
}

func TestSearchMemoryFallsBackToTextSearchWhenEmbeddingQueryFails(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "memory.db"), "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	fact := domain.MemoryFact{
		ID:        "fact-1",
		ProjectID: "default",
		Category:  domain.MemoryCategoryConversation,
		Key:       "event-1",
		Value:     "remember the release train uses Temporal",
		Status:    "active",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.UpsertFact(context.Background(), fact); err != nil {
		t.Fatalf("upsert fact: %v", err)
	}

	activities := Activities{
		Store:          store,
		MemoryEmbedder: failingEmbedder{err: errors.New("embedding model unavailable")},
	}

	facts, err := activities.searchMemory(context.Background(), "default", domain.MemoryCategoryConversation, "Temporal", 10)
	if err != nil {
		t.Fatalf("search memory: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected text search fallback result, got %d", len(facts))
	}
	if facts[0].ID != fact.ID {
		t.Fatalf("unexpected fallback fact: %s", facts[0].ID)
	}
}

func TestPersistConversationMemoryStoresTextWhenEmbeddingFails(t *testing.T) {
	t.Parallel()

	store, err := sqlite.Open(filepath.Join(t.TempDir(), "memory.db"), "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		Kind:        domain.EventKindMessage,
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		ActorName:   "luka",
		Body:        "remember Temporal setup",
		CreatedAt:   time.Now().UTC(),
	}
	activities := Activities{
		Store:          store,
		MemoryEmbedder: failingEmbedder{err: errors.New("embedding model unavailable")},
		EmbeddingModel: "text-embedding-3-small",
	}

	if err := activities.PersistConversationMemory(context.Background(), event, event.Body); err != nil {
		t.Fatalf("persist conversation memory: %v", err)
	}

	facts, err := store.SearchByCategory(context.Background(), "default", domain.MemoryCategoryConversation, "Temporal", 10)
	if err != nil {
		t.Fatalf("search stored text fact: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("expected stored text fact, got %d", len(facts))
	}
	if facts[0].EmbeddingID != "" {
		t.Fatalf("expected text-only memory fact, got embedding id %q", facts[0].EmbeddingID)
	}
}

type failingEmbedder struct {
	err error
}

func (f failingEmbedder) EmbedDocuments(context.Context, []string) ([][]float32, error) {
	return nil, f.err
}

func (f failingEmbedder) EmbedQuery(context.Context, string) ([]float32, error) {
	return nil, f.err
}
