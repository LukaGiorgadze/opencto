package shell

import (
	"encoding/json"
	"strings"
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
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("expected required array, got %#v", schema["required"])
	}
	for _, field := range []string{"run_mode", "idempotency", "process_scope"} {
		if !containsRequiredField(required, field) {
			t.Fatalf("expected %q to be required, got %#v", field, required)
		}
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties object, got %#v", schema["properties"])
	}
	for _, field := range []string{"cwd", "run_mode", "idempotency", "process_scope"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("expected %q property in schema", field)
		}
	}
}

func TestShellToolSchemaUsesStopOnFinishProcessScope(t *testing.T) {
	t.Parallel()

	var schema map[string]any
	if err := json.Unmarshal(ShellToolSchema(), &schema); err != nil {
		t.Fatalf("decode shell tool schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	processScope := properties["process_scope"].(map[string]any)
	values := processScope["enum"].([]any)
	if !containsRequiredField(values, "stop_on_finish") {
		t.Fatalf("expected stop_on_finish process scope, got %#v", values)
	}
	if containsRequiredField(values, "task") {
		t.Fatalf("process scope should not expose ambiguous task value: %#v", values)
	}
	description := processScope["description"].(string)
	if !containsText(description, "stops it when the task finishes") || !containsText(description, "Do not use it") || !containsText(description, "after your response") {
		t.Fatalf("expected explicit lifetime guidance, got %q", description)
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

func containsRequiredField(required []any, field string) bool {
	for _, value := range required {
		if value == field {
			return true
		}
	}
	return false
}

func containsText(value, needle string) bool {
	return strings.Contains(value, needle)
}
