package memory

import (
	_ "embed"
	"encoding/json"
)

const (
	RememberToolName = "MemoryRemember"
	SearchToolName   = "MemorySearch"
	ForgetToolName   = "MemoryForget"

	RememberToolDescription = "Stores a durable memory for later OpenCTO tasks. Use for user preferences, project facts, standing instructions, decisions, and important context worth remembering beyond the current task."
	SearchToolDescription   = "Searches durable OpenCTO memory for project or global context. Use when the current task may depend on remembered preferences, decisions, or project facts beyond the automatically provided memory context."
	ForgetToolDescription   = "Deletes durable OpenCTO memories by exact memory_ids, or by tag and scope filters. Search memory first if exact memory ids are not already known."
)

//go:embed remember_schema.json
var rememberToolSchema json.RawMessage

//go:embed search_schema.json
var searchToolSchema json.RawMessage

//go:embed forget_schema.json
var forgetToolSchema json.RawMessage

func RememberToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), rememberToolSchema...)
}

func SearchToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), searchToolSchema...)
}

func ForgetToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), forgetToolSchema...)
}
