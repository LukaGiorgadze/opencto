package write

import (
	"encoding/json"
	"testing"
)

func TestWriteToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(WriteToolSchema(), &schema); err != nil {
		t.Fatalf("decode write tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestWriteToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := WriteToolSchema()
	second := WriteToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty write tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("WriteToolSchema should return a copy")
	}
}
