package llm

import "testing"

func TestConversationCompressionResponseFormatIsStrictJSONSchema(t *testing.T) {
	t.Parallel()

	format := conversationCompressionResponseFormat()
	if format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict {
		t.Fatalf("expected strict json_schema response format, got %#v", format)
	}
	schema := format.JSONSchema.Schema
	if schema == nil || schema.Type != "object" {
		t.Fatalf("expected object schema, got %#v", schema)
	}
	if schema.AdditionalProperties {
		t.Fatalf("expected additional properties to be disabled")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "summary" {
		t.Fatalf("expected summary to be required, got %#v", schema.Required)
	}
	if schema.Properties["summary"].Type != "string" {
		t.Fatalf("expected string summary property, got %#v", schema.Properties["summary"])
	}
}

func TestMemoryExtractionResponseFormatIsStrictJSONSchema(t *testing.T) {
	t.Parallel()

	format := memoryExtractionResponseFormat()
	if format.Type != "json_schema" || format.JSONSchema == nil || !format.JSONSchema.Strict {
		t.Fatalf("expected strict json_schema response format, got %#v", format)
	}
	schema := format.JSONSchema.Schema
	if schema == nil || schema.Type != "object" {
		t.Fatalf("expected object schema, got %#v", schema)
	}
	candidates := schema.Properties["candidates"]
	if candidates == nil || candidates.Type != "array" || candidates.Items == nil {
		t.Fatalf("expected candidates array schema, got %#v", candidates)
	}
	item := candidates.Items
	if item.AdditionalProperties {
		t.Fatalf("expected candidate additional properties to be disabled")
	}
	if len(item.Required) != 7 {
		t.Fatalf("expected all candidate properties to be required, got %#v", item.Required)
	}
	if item.Properties["scope"].Type != "string" || len(item.Properties["scope"].Enum) != 4 {
		t.Fatalf("expected scoped enum, got %#v", item.Properties["scope"])
	}
	if enumContains(item.Properties["scope"].Enum, "channel") {
		t.Fatalf("memory extraction schema should not include unsupported channel scope")
	}
	if enumContains(item.Properties["kind"].Enum, "project") || enumContains(item.Properties["kind"].Enum, "user") {
		t.Fatalf("memory kind enum should not duplicate scope values, got %#v", item.Properties["kind"].Enum)
	}
	if item.Properties["confidence"].Type != "number" {
		t.Fatalf("expected numeric confidence, got %#v", item.Properties["confidence"])
	}
}

func enumContains(values []any, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
