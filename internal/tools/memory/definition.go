package memory

import (
	_ "embed"
	"encoding/json"
)

const (
	ProposeAddToolName    = "MemoryProposeAdd"
	SearchToolName        = "MemorySearch"
	ListToolName          = "MemoryList"
	ProposeUpdateToolName = "MemoryProposeUpdate"
	ProposeForgetToolName = "MemoryProposeForget"

	ProposeAddToolDescription    = "Proposes adding a new durable memory for later OpenCTO tasks. The backend validates, dedupes, saves, and embeds accepted proposals. Use only for durable thread/channel/project/user/global facts, preferences, standing instructions, decisions, constraints, identity context, or reusable workflow context; search/update existing memory first when a related memory may already exist."
	SearchToolDescription        = "Searches durable OpenCTO memory for thread, channel, project, user, or global context. Use before proposing memory changes when the current task may depend on or change remembered preferences, decisions, project facts, identity context, constraints, or workflows."
	ListToolDescription          = "Lists recent durable memories for inspection and cleanup. Use for memory admin/debug requests such as showing thread, channel, project, current-user, or global memories; this is read-only."
	ProposeUpdateToolDescription = "Proposes updating an existing durable OpenCTO memory by memory_id. Use after searching when a remembered fact, preference, tags, pinning, or confidence should change without deleting and recreating it. The backend validates, saves, and re-embeds accepted proposals."
	ProposeForgetToolDescription = "Proposes deleting durable OpenCTO memories by exact memory_ids, or by tag and scope filters. Use when the user asks to forget/delete memory, or when a memory is clearly obsolete; search first if exact ids are not already known. The backend validates and performs accepted deletes."
)

//go:embed remember_schema.json
var rememberToolSchema json.RawMessage

//go:embed search_schema.json
var searchToolSchema json.RawMessage

//go:embed list_schema.json
var listToolSchema json.RawMessage

//go:embed update_schema.json
var updateToolSchema json.RawMessage

//go:embed forget_schema.json
var forgetToolSchema json.RawMessage

func ProposeAddToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), rememberToolSchema...)
}

func SearchToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), searchToolSchema...)
}

func ListToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), listToolSchema...)
}

func ProposeUpdateToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), updateToolSchema...)
}

func ProposeForgetToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), forgetToolSchema...)
}
