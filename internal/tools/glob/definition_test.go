package edit

import (
	"encoding/json"
	"testing"
)

func TestEditToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(EditToolSchema(), &schema); err != nil {
		t.Fatalf("decode edit tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestEditToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := EditToolSchema()
	second := EditToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty edit tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("EditToolSchema should return a copy")
	}
}
