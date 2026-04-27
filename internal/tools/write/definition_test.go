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
	if schema["additionalProperties"] != false {
		t.Fatalf("expected additionalProperties false, got %#v", schema["additionalProperties"])
	}

	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected required array, got %#v", schema["required"])
	}
	assertRequired(t, required, "file_path")
	assertRequired(t, required, "content")
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

func assertRequired(t *testing.T, required []any, field string) {
	t.Helper()

	for _, item := range required {
		if item == field {
			return
		}
	}
	t.Fatalf("expected required field %q in %#v", field, required)
}
