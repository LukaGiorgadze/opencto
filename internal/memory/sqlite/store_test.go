package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func TestStoreSearchByCategoryScopesToProject(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := Open(path, "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	factA := domain.MemoryFact{
		ID:        "fact-a",
		ProjectID: "project-a",
		Category:  domain.MemoryCategoryConversation,
		Key:       "stack",
		Value:     "Go and Temporal",
		Provenance: domain.Provenance{
			Source:     "test",
			ObservedAt: now,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	factB := factA
	factB.ID = "fact-b"
	factB.ProjectID = "project-b"

	if err := store.UpsertFact(context.Background(), factA); err != nil {
		t.Fatalf("insert factA: %v", err)
	}
	if err := store.UpsertFact(context.Background(), factB); err != nil {
		t.Fatalf("insert factB: %v", err)
	}

	found, err := store.SearchByCategory(context.Background(), "project-a", domain.MemoryCategoryConversation, "go", 10)
	if err != nil {
		t.Fatalf("search facts: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(found))
	}
	if found[0].ProjectID != "project-a" {
		t.Fatalf("unexpected project scope: %s", found[0].ProjectID)
	}
}

func TestStoreSearchByCategorySimilarScopesToProject(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "memory.db")
	store, err := Open(path, "", time.Second)
	if err != nil {
		t.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	now := time.Now().UTC()
	factA := domain.MemoryFact{
		ID:        "fact-a",
		ProjectID: "project-a",
		Category:  domain.MemoryCategoryConversation,
		Key:       "event-a",
		Value:     "OpenCTO uses Go and Temporal",
		Provenance: domain.Provenance{
			Source:     "test",
			ObservedAt: now,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	factB := factA
	factB.ID = "fact-b"
	factB.ProjectID = "project-b"
	factB.Value = "Completely unrelated memory"

	if err := store.UpsertFact(context.Background(), factA); err != nil {
		t.Fatalf("insert factA: %v", err)
	}
	if err := store.UpsertFact(context.Background(), factB); err != nil {
		t.Fatalf("insert factB: %v", err)
	}

	if err := store.UpsertFactEmbedding(context.Background(), factA.ProjectID, factA.ID, factA.Category, "test-model", []float32{1, 0, 0}); err != nil {
		t.Fatalf("insert factA embedding: %v", err)
	}
	if err := store.UpsertFactEmbedding(context.Background(), factB.ProjectID, factB.ID, factB.Category, "test-model", []float32{0, 1, 0}); err != nil {
		t.Fatalf("insert factB embedding: %v", err)
	}

	found, err := store.SearchByCategorySimilar(context.Background(), "project-a", domain.MemoryCategoryConversation, []float32{1, 0, 0}, 10)
	if err != nil {
		t.Fatalf("semantic search facts: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(found))
	}
	if found[0].ID != factA.ID {
		t.Fatalf("unexpected fact id: %s", found[0].ID)
	}
}
