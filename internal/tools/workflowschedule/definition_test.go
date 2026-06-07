package workflowschedule

import (
	"encoding/json"
	"testing"
)

func TestWorkflowUpdateSchemaIsAuthoringPromptOnly(t *testing.T) {
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
	if len(schema.Required) != 2 || schema.Required[0] != "workflow_id" || schema.Required[1] != "prompt" {
		t.Fatalf("expected workflow_id and prompt to be required, got %#v", schema.Required)
	}
	if _, ok := schema.Properties["operation"]; ok {
		t.Fatalf("WorkflowUpdate should not expose operation")
	}
	for _, field := range []string{"workflow_id", "prompt", "commit_message"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("WorkflowUpdate schema missing field %q", field)
		}
	}
	for _, field := range []string{"name", "description", "schedule", "notification_policy", "steps", "files"} {
		if _, ok := schema.Properties[field]; ok {
			t.Fatalf("WorkflowUpdate schema should not expose manifest field %q", field)
		}
	}
}

func TestWorkflowCreateSchemaIsAuthoringPromptOnly(t *testing.T) {
	t.Parallel()

	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(WorkflowCreateToolSchema(), &schema); err != nil {
		t.Fatalf("decode workflow create schema: %v", err)
	}
	if len(schema.Required) != 2 || schema.Required[0] != "workflow_id" || schema.Required[1] != "prompt" {
		t.Fatalf("expected workflow_id and prompt to be required, got %#v", schema.Required)
	}
	for _, field := range []string{"workflow_id", "prompt", "commit_message"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Fatalf("WorkflowCreate schema missing field %q", field)
		}
	}
	for _, field := range []string{"description", "notification_policy", "steps", "files", "paused", "note"} {
		if _, ok := schema.Properties[field]; ok {
			t.Fatalf("WorkflowCreate schema should not expose manifest field %q", field)
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
