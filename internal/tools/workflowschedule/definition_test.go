package workflowschedule

import (
	"encoding/json"
	"testing"
)

func TestWorkflowUpdateSchemaHasOptionalManifestFields(t *testing.T) {
	t.Parallel()

	var schema struct {
		Type                 string                     `json:"type"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(WorkflowUpdateToolSchema(), &schema); err != nil {
		t.Fatalf("decode workflow update schema: %v", err)
	}
	if schema.Type != "object" {
		t.Fatalf("expected object schema, got %q", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatalf("expected additionalProperties false")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "workflow_id" {
		t.Fatalf("expected only workflow_id to be required, got %#v", schema.Required)
	}
	if _, ok := schema.Properties["operation"]; ok {
		t.Fatalf("WorkflowUpdate should not expose operation")
	}
	for _, field := range []string{"name", "description", "schedule", "notification_policy", "env", "steps", "files", "commit_message"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("WorkflowUpdate schema missing optional field %q", field)
		}
	}
}

func TestWorkflowCreateSchemaDoesNotRequireOptionalManifestFields(t *testing.T) {
	t.Parallel()

	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(WorkflowCreateToolSchema(), &schema); err != nil {
		t.Fatalf("decode workflow create schema: %v", err)
	}
	required := map[string]bool{}
	for _, field := range schema.Required {
		required[field] = true
	}
	for _, field := range []string{"description", "notification_policy", "env", "files", "commit_message", "paused", "note"} {
		if required[field] {
			t.Fatalf("WorkflowCreate should not require optional field %q", field)
		}
	}
}

func TestWorkflowToolSchemasReturnCopies(t *testing.T) {
	t.Parallel()

	first := WorkflowCreateToolSchema()
	second := WorkflowCreateToolSchema()
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected non-empty schema")
	}
	original := second[0]
	first[0] = 'x'
	if second[0] != original {
		t.Fatalf("schema helpers should return a copy")
	}
}
