package workflowschedule

import (
	_ "embed"
	"encoding/json"
)

const (
	ToolName        = "WorkflowSchedule"
	ToolDescription = `Create and manage Git-backed workflow schedules.

Use this tool when the user asks OpenCTO to run a workflow later, once, or repeatedly.

Workflow schedules are deterministic by default.
The tool creates or updates a workflow bundle under the OpenCTO data directory at workflows/{workflow_id}, commits it to the bundle's Git repo, and registers a schedule that runs the approved commit_hash.
Do not schedule free-form AI instructions.

The model must convert natural-language time into structured input before calling this tool:
- For one-shot schedules, set schedule.one_shot_at to an RFC3339 timestamp with offset, for example "2026-05-07T09:00:00+04:00".
- For recurring schedules, set schedule.cron to a cron expression, for example "0 9 * * *" or "@every 24h".
- Use host timezone only. Do not set a user-provided timezone; the backend resolves the host IANA timezone.

Workflow logic must live in files under src/. The manifest only defines durable step boundaries. Use command and args arrays for each step; do not combine executable and arguments in command.
Each step must run plain, single-responsibility code for that step only. When the requested workflow has multiple distinct operations, create separate steps and separate entrypoints instead of one large command.
Do not write scheduler, worker, workflow, activity, queue, daemon, or framework orchestration code inside src/. The code in src/ should be normal runnable programs or scripts invoked by the step command.
Edit and Write may be used to author or modify files under workflows/{workflow_id}/src. After any Edit or Write to a workflow bundle, call WorkflowSchedule update for that workflow_id so the backend validates workflow.yml, commits dirty source changes, and repoints the schedule to the new commit_hash.
Do not run git commit manually for workflow bundles.
The backend owns workflow.yml and .gitignore when this tool writes a bundle. Provide implementation files only under src/.
Use snake_case fields such as start_to_close_timeout, schedule_to_close_timeout, retry_policy.maximum_attempts, and schedule.overlap_policy.

For new workflows, provide a complete manifest and implementation files under src/.
For existing workflow bundles, files may be empty and the current workflow.yml plus dirty src changes will be validated, committed, and scheduled.
For a code-only finalization after Edit/Write, call update with the workflow_id, files: [], steps: [], env: [], schedule: {"cron":"","one_shot_at":"","overlap_policy":"","catchup_window":"","pause_on_failure":false}, and notification_policy: {"on_failure": false}; the backend will preserve the existing manifest fields.`
)

//go:embed schema.json
var toolSchema json.RawMessage

func ToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), toolSchema...)
}
