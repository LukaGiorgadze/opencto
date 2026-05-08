package memory

import (
	_ "embed"
	"encoding/json"
)

const (
	RememberToolName = "MemoryRemember"
	SearchToolName   = "MemorySearch"
	UpdateToolName   = "MemoryUpdate"
	ForgetToolName   = "MemoryForget"

	RememberToolDescription = "Stores a new durable memory for later OpenCTO tasks. Use only for durable preferences, project/user/global facts, standing instructions, decisions, constraints, identity context, or reusable workflow context; search/update existing memory first when a related memory may already exist."
	SearchToolDescription   = "Searches durable OpenCTO memory for project, user, or global context. Use before writing memory when the current task may depend on or change remembered preferences, decisions, project facts, identity context, constraints, or workflows."
	UpdateToolDescription   = "Updates an existing durable OpenCTO memory by memory_id. Use after searching when a remembered fact, preference, tags, pinning, or confidence should change without deleting and recreating it."
	ForgetToolDescription   = "Deletes durable OpenCTO memories by exact memory_ids, or by tag and scope filters. Use when the user asks to forget/delete memory, or when a memory is clearly obsolete; search first if exact ids are not already known."
)

//go:embed remember_schema.json
var rememberToolSchema json.RawMessage

//go:embed search_schema.json
var searchToolSchema json.RawMessage

//go:embed update_schema.json
var updateToolSchema json.RawMessage

//go:embed forget_schema.json
var forgetToolSchema json.RawMessage

func RememberToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), rememberToolSchema...)
}

func SearchToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), searchToolSchema...)
}

func UpdateToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), updateToolSchema...)
}

func ForgetToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), forgetToolSchema...)
}
