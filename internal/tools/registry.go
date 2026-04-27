package tools

import (
	"encoding/json"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/domain"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

const (
	SelectorToolCommandName = shelltool.SelectorToolName
)

type SelectorDefinition struct {
	Name        string
	Type        domain.ToolType
	Description string
	Parameters  json.RawMessage
}

var selectorDefinitions = []SelectorDefinition{
	{
		Name:        SelectorToolCommandName,
		Type:        domain.ToolTypeShell,
		Description: shelltool.SelectorToolDescription,
		Parameters:  shelltool.SelectorToolParameters(),
	},
}

func SelectorDefinitions() []SelectorDefinition {
	cloned := make([]SelectorDefinition, len(selectorDefinitions))
	for i, definition := range selectorDefinitions {
		cloned[i] = definition
		if definition.Parameters != nil {
			cloned[i].Parameters = append(json.RawMessage(nil), definition.Parameters...)
		}
	}
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

func FallbackCandidates(primary domain.ToolType) []domain.ToolType {
	return nil
}
