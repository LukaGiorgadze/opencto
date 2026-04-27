package tools

import (
	"encoding/json"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/domain"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

const (
	CommandToolName = shelltool.ShellToolName
)

type Definition struct {
	Name        string
	Type        domain.ToolType
	Description string
	Schema      json.RawMessage
}

var definitions = []Definition{
	{
		Name:        CommandToolName,
		Type:        domain.ToolTypeShell,
		Description: shelltool.ShellToolDescription,
		Schema:      shelltool.ShellToolSchema(),
	},
}

func Definitions() []Definition {
	cloned := make([]Definition, len(definitions))
	for i, definition := range definitions {
		cloned[i] = definition
		if definition.Schema != nil {
			cloned[i].Schema = append(json.RawMessage(nil), definition.Schema...)
		}
	}
	return cloned
}

func LLMDefinitions() []llms.Tool {
	registered := Definitions()
	tools := make([]llms.Tool, 0, len(registered))
	for _, definition := range registered {
		tools = append(tools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Schema,
				Strict:      true,
			},
		})
	}
	return tools
}

func DefinitionByName(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}

func FallbackCandidates(primary domain.ToolType) []domain.ToolType {
	return nil
}
