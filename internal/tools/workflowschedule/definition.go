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

	WorkflowCreateToolDescription = `Create a new scheduled workflow by delegating authoring to a durable agent.

Only for new workflows. Fails if workflow_id is already registered.
Provide a self-contained prompt with the desired schedule, logical steps, state handling, credential requirements, and validation expectations. The authoring agent can share state between manifest steps with OpenCTO runtime artifact/data env vars.
Do not provide workflow.yml, step definitions, or source files in this tool call. The sub agent authors workflow.yml and source files on disk; OpenCTO validates, commits, and schedules the final bundle.`

	WorkflowUpdateToolDescription = `Update an existing scheduled workflow by delegating authoring to a durable agent.

Provide a self-contained prompt describing what should change, what must stay stable, logical step boundaries, and state handling. The authoring agent can share state between manifest steps with OpenCTO runtime artifact/data env vars.
Do not provide workflow.yml, step definitions, or source files in this tool call. The sub agent edits workflow.yml and source files on disk; OpenCTO validates, commits, and repoints the schedule to the final bundle.`

	WorkflowDeleteToolDescription = `Hard-delete an existing scheduled workflow.

Removes the schedule, workflow record, $OPENCTO_WORKSPACE/workflows/{workflow_id}/, all run snapshots under $OPENCTO_WORKSPACE/workflow-runs/{workflow_id}/, the workflow's data/ directory, and workflow logs under the runtime state dir's workflow-logs/{workflow_id}/. This is irreversible.`

	WorkflowOperationToolDescription = `Run control and read operations on workflows.

Operations:
- list              — list Temporal workflow executions plus registered scheduled workflows that have no current execution
- describe          — full status, source path, and schedule state for a workflow
- trigger           — publish current local source if needed, then manually run the workflow
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
