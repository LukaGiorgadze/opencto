package workflowschedule

import (
	_ "embed"
	"encoding/json"
)

const (
	WorkflowCreateToolName    = "WorkflowCreate"
	WorkflowUpdateToolName    = "WorkflowUpdate"
	WorkflowDeleteToolName    = "WorkflowDelete"
	WorkflowOperationToolName = "WorkflowOperation"

	WorkflowCreateToolDescription = `Create a new scheduled workflow by delegating authoring to a durable sub-agent.

Only for new workflows. Fails if workflow_id is already registered.
Provide a self-contained prompt with the desired schedule, behavior, state handling, credentials/env names, and validation expectations.
Do not provide workflow.yml, step definitions, or source files in this tool call. The child agent authors workflow.yml and source files on disk; OpenCTO validates, commits, and schedules the final bundle.`

	WorkflowUpdateToolDescription = `Update an existing scheduled workflow by delegating authoring to a durable sub-agent.

Provide a self-contained prompt describing what should change and what must stay stable.
Do not provide workflow.yml, step definitions, or source files in this tool call. The child agent edits workflow.yml and source files on disk; OpenCTO validates, commits, and repoints the schedule to the final bundle.`

	WorkflowDeleteToolDescription = `Hard-delete an existing scheduled workflow.

Removes the schedule, workflow record, {OPENCTO_WORKFLOWS_DIR}/{workflow_id}/, and all run snapshots under $OPENCTO_WORKSPACE/workflow-runs/{workflow_id}/, including the workflow's data/ directory. This is irreversible.`

	WorkflowOperationToolDescription = `Run control and read operations on scheduled workflows.

Operations:
- list              — list all registered workflows
- describe          — full status, source path, and schedule state for a workflow
- trigger           — manually run a workflow; requires a clean committed bundle (call WorkflowUpdate first if dirty)
- pause / resume    — suspend or re-enable the schedule

Use describe before any action that changes schedule state when current workflow status is not already known in this turn.
Use WorkflowUpdate for code or manifest changes — not this tool.`
)

//go:embed workflow_create_schema.json
var workflowCreateToolSchema json.RawMessage

//go:embed workflow_update_schema.json
var workflowUpdateToolSchema json.RawMessage

//go:embed workflow_delete_schema.json
var workflowDeleteToolSchema json.RawMessage

//go:embed workflow_operation_schema.json
var workflowOperationToolSchema json.RawMessage

func WorkflowCreateToolSchema() json.RawMessage {
	return cloneSchema(workflowCreateToolSchema)
}

func WorkflowUpdateToolSchema() json.RawMessage {
	return cloneSchema(workflowUpdateToolSchema)
}

func WorkflowDeleteToolSchema() json.RawMessage {
	return cloneSchema(workflowDeleteToolSchema)
}

func WorkflowOperationToolSchema() json.RawMessage {
	return cloneSchema(workflowOperationToolSchema)
}

func cloneSchema(schema json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), schema...)
}
