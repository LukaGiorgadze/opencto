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

	WorkflowCreateToolDescription = `Create a new Git-backed scheduled workflow.

Use this tool only for new workflows. Provide enough fields to write a valid workflow.yml, and include implementation files under src/ when the workflow needs workflow-owned code. It fails if the workflow is already registered.

The manifest defines durable step boundaries only. Implementation files are ordinary runnable programs, scripts, or binaries invoked by step command and args. Keep each step focused on one responsibility, split distinct operations into separate steps and entrypoints, and do not write schedulers, workers, queues, daemons, or orchestration framework code inside src/.

Use command for the executable only and args for its arguments. Do not repeat the executable in args.
Global env entries apply to every step. Use NAME to require and pass through a host environment variable, or NAME=value to set a workflow-owned value.`

	WorkflowUpdateToolDescription = `Update an existing Git-backed scheduled workflow.

Use this tool after Edit or Write modifies files under workflows/{workflow_id}/src. It loads the existing workflow.yml, applies any provided manifest fields, validates the bundle, commits dirty src changes, and repoints the schedule to the new commit_hash.

The manifest defines durable step boundaries only. Implementation files are ordinary runnable programs, scripts, or binaries invoked by step command and args. Keep each step focused on one responsibility, split distinct operations into separate steps and entrypoints, and do not write schedulers, workers, queues, daemons, or orchestration framework code inside src/.

Use command for the executable only and args for its arguments. Do not repeat the executable in args.
Global env entries apply to every step. Use NAME to require and pass through a host environment variable, or NAME=value to set a workflow-owned value.

Fields are optional unless the schema marks them required. Omitted fields preserve the existing manifest value. If a field is present, it is applied and validated. Do not run git commit manually for workflow bundles.`

	WorkflowDeleteToolDescription = `Hard-delete an existing scheduled workflow.

Deletes the schedule, workflow record, workflow bundle directory, and workflow run snapshots for the workflow_id.`

	WorkflowOperationToolDescription = `Run control and read operations for scheduled workflows.

Supported operations: list, describe, trigger, pause, resume. Use WorkflowUpdate for code or manifest changes, not this tool.`
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
