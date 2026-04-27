package shell

import (
	"encoding/json"
	"testing"
)

func TestSelectorToolParametersAreValidJSON(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(SelectorToolParameters(), &schema); err != nil {
		t.Fatalf("decode selector parameters: %v", err)
	}
	if schema["type"] != "object" {
		t.Fatalf("expected object schema, got %#v", schema["type"])
	}
}

func TestSelectorToolParametersReturnsCopy(t *testing.T) {
	t.Parallel()

	first := SelectorToolParameters()
	second := SelectorToolParameters()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty selector parameters")
	}

	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("SelectorToolParameters should return a copy")
	}
}
