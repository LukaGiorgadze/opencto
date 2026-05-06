package schedule

import (
	_ "embed"
	"encoding/json"
)

const (
	ToolName        = "Schedule"
	ToolDescription = `Create and manage Temporal-backed OpenCTO schedules.

Use this tool when the user asks OpenCTO to do something later, once, or repeatedly.

The model must convert natural-language time into structured input before calling this tool:
- For one-shot schedules, set one_shot_at to an RFC3339 timestamp with offset, for example "2026-05-07T09:00:00+04:00".
- For recurring schedules, set cron to a Temporal cron expression, for example "0 9 * * *" or "@every 24h".
- Interpret phrases like "tomorrow", "today", and "morning" using the current local time and host timezone from the system prompt.

OpenCTO uses the host IANA timezone when creating schedules. If the host timezone cannot be resolved, the tool returns an error instead of guessing.
Scheduled runs are enqueued as normal OpenCTO tasks, so they can use the same tools as immediate user requests.

OpenCTO stores schedule names and descriptions in Temporal schedule metadata. Show users those human-readable labels instead of raw Temporal schedule IDs unless they explicitly ask for internal IDs.
For list requests, set include_completed=false unless the user explicitly asks for completed, exhausted, past, no-upcoming-run, or all schedules.
If completed schedules are included, show only their name, optional description, and completed marker; do not show a next run for completed schedules.`
)

//go:embed schema.json
var toolSchema json.RawMessage

func ToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), toolSchema...)
}
