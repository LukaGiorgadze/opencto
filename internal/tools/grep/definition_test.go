package grep

import (
	"encoding/json"
	"testing"
)

func TestGrepToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(GrepToolSchema(), &schema); err != nil {
		t.Fatalf("decode grep tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestGrepToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := GrepToolSchema()
	second := GrepToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty grep tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("GrepToolSchema should return a copy")
	}
}
