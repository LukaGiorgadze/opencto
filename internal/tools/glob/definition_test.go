package glob

import (
	"encoding/json"
	"testing"
)

func TestGlobToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(GlobToolSchema(), &schema); err != nil {
		t.Fatalf("decode glob tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestGlobToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := GlobToolSchema()
	second := GlobToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty glob tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("GlobToolSchema should return a copy")
	}
}
