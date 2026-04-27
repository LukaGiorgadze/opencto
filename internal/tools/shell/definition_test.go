package shell

import (
	"encoding/json"
	"testing"
)

func TestShellToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(ShellToolSchema(), &schema); err != nil {
		t.Fatalf("decode shell tool schema: %v", err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("expected draft 2020-12 schema declaration, got %#v", schema["$schema"])
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestShellToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := ShellToolSchema()
	second := ShellToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty shell tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("ShellToolSchema should return a copy")
	}
}
