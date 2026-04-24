package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

type semanticMemoryFact struct {
	fact       domain.MemoryFact
	similarity float64
}

func (s *Store) UpsertFactEmbedding(ctx context.Context, projectID, factID string, category domain.MemoryCategory, model string, embedding []float32) error {
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO memory_fact_embeddings (fact_id, project_id, category, model, dimensions, vector_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(fact_id) DO UPDATE SET
			project_id = excluded.project_id,
			category = excluded.category,
			model = excluded.model,
			dimensions = excluded.dimensions,
			vector_json = excluded.vector_json,
			updated_at = excluded.updated_at
	`, factID, projectID, category, model, len(embedding), mustJSON(embedding), now, now); err != nil {
		return err
	}

	return s.insert(ctx, `
		UPDATE memory_facts
		SET embedding_id = ?, updated_at = CASE WHEN updated_at > ? THEN updated_at ELSE ? END
		WHERE id = ? AND project_id = ?
	`, factID, now, now, factID, projectID)
}

func (s *Store) SearchByCategorySimilar(ctx context.Context, projectID string, category domain.MemoryCategory, queryEmbedding []float32, limit int) ([]domain.MemoryFact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT mf.id, mf.project_id, mf.category, mf.key_name, mf.value_text, mf.status, mf.embedding_id, mf.provenance_json, mf.metadata_json, mf.created_at, mf.updated_at, mfe.vector_json
		FROM memory_facts mf
		INNER JOIN memory_fact_embeddings mfe ON mfe.fact_id = mf.id
		WHERE mfe.project_id = ? AND mfe.category = ?
	`, projectID, category)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	scored := make([]semanticMemoryFact, 0, limit)
	for rows.Next() {
		var item domain.MemoryFact
		var provenance, metadata, vectorJSON []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Category, &item.Key, &item.Value, &item.Status, &item.EmbeddingID, &provenance, &metadata, &item.CreatedAt, &item.UpdatedAt, &vectorJSON); err != nil {
			return nil, err
		}

		var embedding []float32
		if err := json.Unmarshal(vectorJSON, &embedding); err != nil {
			return nil, err
		}
		score, ok := cosineSimilarity(queryEmbedding, embedding)
		if !ok {
			continue
		}

		_ = json.Unmarshal(provenance, &item.Provenance)
		_ = json.Unmarshal(metadata, &item.Metadata)
		scored = append(scored, semanticMemoryFact{
			fact:       item,
			similarity: score,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].similarity == scored[j].similarity {
			return scored[i].fact.UpdatedAt.After(scored[j].fact.UpdatedAt)
		}
		return scored[i].similarity > scored[j].similarity
	})

	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}

	facts := make([]domain.MemoryFact, 0, len(scored))
	for _, item := range scored {
		facts = append(facts, item.fact)
	}
	return facts, nil
}

func cosineSimilarity(left, right []float32) (float64, bool) {
	if len(left) == 0 || len(right) == 0 || len(left) != len(right) {
		return 0, false
	}

	var dot float64
	var leftNorm float64
	var rightNorm float64
	for idx := range left {
		l := float64(left[idx])
		r := float64(right[idx])
		dot += l * r
		leftNorm += l * l
		rightNorm += r * r
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0, false
	}

	return dot / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm)), true
}

func scanNullableTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := value.Time
	return &timestamp
}
