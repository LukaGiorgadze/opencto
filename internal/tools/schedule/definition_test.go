package schedule

import (
	"encoding/json"
	"testing"
)

func TestScheduleToolSchemaIsStrictJSON(t *testing.T) {
	t.Parallel()

	var schema struct {
		Type                 string                     `json:"type"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(ToolSchema(), &schema); err != nil {
		t.Fatalf("decode schedule schema: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("expected object schema, got %q", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatalf("expected additionalProperties false")
	}
	required := map[string]bool{}
	for _, field := range schema.Required {
		required[field] = true
	}
	for field := range schema.Properties {
		if !required[field] {
			t.Fatalf("property %q must be required for strict tool compatibility", field)
		}
	}
}

func TestScheduleToolSchemaReturnsCopy(t *testing.T) {
	t.Parallel()

	first := ToolSchema()
	second := ToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty schema")
	}
	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("ToolSchema should return a copy")
	}
}
