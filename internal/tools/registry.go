package tools

import (
	"encoding/json"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/domain"
)

const (
	SelectorToolShellName = "Shell"
)

type SelectorDefinition struct {
	Name          string
	Type          domain.ToolType
	Description   string
	PromptSummary string
	Parameters    json.RawMessage
}

var selectorDefinitions = []SelectorDefinition{
	{
		Name:          SelectorToolShellName,
		Type:          domain.ToolTypeShell,
		Description:   "Execute any shell command in the project workspace. Use this for any concrete terminal action OpenCTO can perform safely.",
		PromptSummary: "- `" + SelectorToolShellName + "` is the only execution tool wired to the host today.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"required": ["command", "args", "working_dir", "timeout_ms", "description", "destructive"],
			"additionalProperties": false,
			"properties": {
				"command": {
					"type": "string",
					"description": "The executable to run for the next execution step."
				},
				"args": {
					"type": "array",
					"items": {"type": "string"},
					"description": "Arguments for the executable. Use an empty array when there are no arguments."
				},
				"working_dir": {
					"type": ["string", "null"],
					"description": "Working directory for the command. Use null to use the project root."
				},
				"timeout_ms": {
					"type": ["number", "null"],
					"description": "Maximum runtime for the command in milliseconds. Use null to accept the runtime default.",
					"maximum": 600000
				},
				"description": {
					"type": ["string", "null"],
					"description": "One sentence describing what this command achieves. Use null when the command itself is self-explanatory."
				},
				"destructive": {
					"type": ["boolean", "null"],
					"description": "Set true when the command mutates state in a risky or hard-to-reverse way. Use null when unknown."
				}
			}
		}`),
	},
}

func SelectorDefinitions() []SelectorDefinition {
	cloned := make([]SelectorDefinition, len(selectorDefinitions))
	copy(cloned, selectorDefinitions)
	return cloned
}

func SelectorLLMDefinitions() []llms.Tool {
	definitions := SelectorDefinitions()
	tools := make([]llms.Tool, 0, len(definitions))
	for _, definition := range definitions {
		tools = append(tools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Parameters,
				Strict:      true,
			},
		})
	}
	return tools
}

func SelectorDefinitionByName(name string) (SelectorDefinition, bool) {
	for _, definition := range selectorDefinitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return SelectorDefinition{}, false
}

func SelectorPromptSummaries() []string {
	lines := make([]string, 0, len(selectorDefinitions))
	for _, definition := range selectorDefinitions {
		lines = append(lines, definition.PromptSummary)
	}
	return lines
}

func ParseToolType(value string) (domain.ToolType, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "shell":
		return domain.ToolTypeShell, true
	default:
		return "", false
	}
}

func FallbackCandidates(primary domain.ToolType) []domain.ToolType {
	return nil
}
