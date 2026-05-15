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

	WorkflowCreateToolDescription = `Create a new scheduled workflow.

Only for new workflows. Fails if workflow_id is already registered.
Creates $OPENCTO_WORKSPACE/workflows/{workflow_id}/ as a workflow-local Git repository with workflow.yml, .gitignore, src/, and data/.

Provide enough fields for a valid workflow.yml. Include files[] when the workflow needs implementation code.

Constraints:
- command: executable only. args is optional, but external commands such as python3 usually need args. Never repeat the executable in args.
- env: literal NAME=value entries only. No template syntax. OPENCTO_* names are reserved.
- files[].path: relative to workflows/{workflow_id}/. Must not target reserved or ignored paths.
- Do not write schedulers, workers, queues, daemons, or orchestration code in workflow source.

Runtime environment variables set by OpenCTO for each step:
- OPENCTO_WORKFLOWS_DIR: root directory containing workflow source repositories. A workflow source repo is at OPENCTO_WORKFLOWS_DIR/{workflow_id}. Do not edit workflow source during a run.
- OPENCTO_WORKFLOW_RUN_DIR: current run snapshot directory and step working directory. Use OPENCTO_WORKFLOW_RUN_DIR/artifacts for files shared between steps in the same run. These files are not available to future runs.
- OPENCTO_WORKFLOW_DATA_DIR: persistent data directory for this workflow at OPENCTO_WORKFLOWS_DIR/{workflow_id}/data. It is ignored by Git and writable at step runtime. Use it only for state or outputs future runs must read.

Do not use removed runtime names such as OPENCTO_RUN_DIR, OPENCTO_STEP_ID, OPENCTO_STEP_DIR, OPENCTO_WORKFLOW_ID, or OPENCTO_RUN_ID.`

	WorkflowUpdateToolDescription = `Update an existing scheduled workflow.

Loads the current workflow.yml, applies provided fields, validates the bundle, commits dirty changes under $OPENCTO_WORKSPACE/workflows/{workflow_id}/, and repoints the schedule to the new commit hash.

Call this after any Edit or Write to files under $OPENCTO_WORKSPACE/workflows/{workflow_id}/, or to update manifest fields directly. Do not run git commit manually for workflow bundles.

Constraints:
- All fields are optional. Omitted fields preserve the existing manifest value.
- command: executable only. args is optional, but external commands such as python3 usually need args. Never repeat the executable in args.
- env: literal NAME=value entries only. No template syntax. OPENCTO_* names are reserved.
- files[].path: relative to workflows/{workflow_id}/. Must not target reserved or ignored paths.
- Do not write schedulers, workers, queues, daemons, or orchestration code in workflow source.

Runtime environment variables set by OpenCTO for each step:
- OPENCTO_WORKFLOWS_DIR: root directory containing workflow source repositories. A workflow source repo is at OPENCTO_WORKFLOWS_DIR/{workflow_id}. Do not edit workflow source during a run.
- OPENCTO_WORKFLOW_RUN_DIR: current run snapshot directory and step working directory. Use OPENCTO_WORKFLOW_RUN_DIR/artifacts for files shared between steps in the same run. These files are not available to future runs.
- OPENCTO_WORKFLOW_DATA_DIR: persistent data directory for this workflow at OPENCTO_WORKFLOWS_DIR/{workflow_id}/data. It is ignored by Git and writable at step runtime. Use it only for state or outputs future runs must read.

Do not use removed runtime names such as OPENCTO_RUN_DIR, OPENCTO_STEP_ID, OPENCTO_STEP_DIR, OPENCTO_WORKFLOW_ID, or OPENCTO_RUN_ID.`

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
