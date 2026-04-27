package read

import (
	"encoding/json"
	"testing"
)

func TestReadToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(ReadToolSchema(), &schema); err != nil {
		t.Fatalf("decode read tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestReadToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := ReadToolSchema()
	second := ReadToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty read tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("ReadToolSchema should return a copy")
	}
}
