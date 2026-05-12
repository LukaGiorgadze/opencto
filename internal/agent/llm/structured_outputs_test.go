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
