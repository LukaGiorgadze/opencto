package skill

import (
	_ "embed"
	"encoding/json"
)

const (
	SkillToolName        = "load_skill"
	SkillToolDescription = "Loads a repository-local Markdown skill from skills/<skill_id>/SKILL.md. Use this before following a skill's full instructions."
)

//go:embed schema.json
var skillToolSchema json.RawMessage

func SkillToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), skillToolSchema...)
}
