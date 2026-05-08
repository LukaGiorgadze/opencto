package skill

import (
	_ "embed"
	"encoding/json"
)

const (
	SkillToolName        = "LoadSkill"
	SkillToolDescription = "Loads an available top-level Markdown skill by exact skill ID. Only use exact skill IDs advertised in the skills reminder. Workspace skills can shadow built-in skills. If a skill points to reference files under references/, load those references with the Read tool by file path, not with LoadSkill."
)

//go:embed schema.json
var skillToolSchema json.RawMessage

func SkillToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), skillToolSchema...)
}
