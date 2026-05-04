package browser

import (
	"encoding/json"
	"testing"
)

func TestBrowserToolSchemaIsValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(BrowserToolSchema(), &schema); err != nil {
		t.Fatalf("decode browser tool schema: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestBrowserToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := BrowserToolSchema()
	second := BrowserToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty browser tool schema")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("BrowserToolSchema should return a copy")
	}
}
