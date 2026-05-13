package workflowschedule

import (
	_ "embed"
	"encoding/json"
)

const (
	ToolName        = "WorkflowSchedule"
	ToolDescription = `Create and manage Git-backed Temporal workflow schedules.

Use this tool when the user asks OpenCTO to run a workflow later, once, or repeatedly.

Workflow schedules are deterministic by default.
The tool creates or updates a workflow bundle under the OpenCTO data directory at workflows/{workflow_id}, commits it to the bundle's Git repo, and creates a Temporal schedule that runs the approved commit_hash.
Do not schedule free-form AI instructions.

The model must convert natural-language time into structured input before calling this tool:
- For one-shot schedules, set schedule.one_shot_at to an RFC3339 timestamp with offset, for example "2026-05-07T09:00:00+04:00".
- For recurring schedules, set schedule.cron to a Temporal cron expression, for example "0 9 * * *" or "@every 24h".
- Use host timezone only. Do not set a user-provided timezone; the backend resolves the host IANA timezone.

Workflow logic must live in files under src/. The manifest only defines durable step boundaries. Use command and args arrays for each step; do not combine executable and arguments in command.
The backend owns workflow.yml and .gitignore. Provide implementation files only under src/.
Use Temporal-style snake_case fields such as start_to_close_timeout, schedule_to_close_timeout, retry_policy.maximum_attempts, and schedule.overlap_policy.

For new workflows, provide files for the implementation under src/. For existing workflow bundles, files may be empty and the current bundle contents will be committed/scheduled.`
)

//go:embed schema.json
var toolSchema json.RawMessage

func ToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), toolSchema...)
}
