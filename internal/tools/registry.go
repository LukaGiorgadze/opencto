package tools

import (
	"encoding/json"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/domain"
	agenttool "github.com/opencto/opencto/internal/tools/agenttool"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
	"github.com/opencto/opencto/internal/tools/postprocess"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	workflowscheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

const (
	CommandToolName = exectool.ExecToolName
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
		Type:        domain.ToolTypeExec,
		Description: exectool.ExecToolDescription,
		Schema:      exectool.ExecToolSchema(),
	},
	{
		Name:        agenttool.AgentToolName,
		Type:        domain.ToolTypeAgent,
		Description: agenttool.AgentToolDescription,
		Schema:      agenttool.AgentToolSchema(),
	},
	{
		Name:        readtool.ReadToolName,
		Type:        domain.ToolTypeRead,
		Description: readtool.ReadToolDescription,
		Schema:      readtool.ReadToolSchema(),
	},
	{
		Name:        edittool.EditToolName,
		Type:        domain.ToolTypeEdit,
		Description: edittool.EditToolDescription,
		Schema:      edittool.EditToolSchema(),
	},
	{
		Name:        writetool.WriteToolName,
		Type:        domain.ToolTypeWrite,
		Description: writetool.WriteToolDescription,
		Schema:      writetool.WriteToolSchema(),
	},
	{
		Name:        globtool.GlobToolName,
		Type:        domain.ToolTypeGlob,
		Description: globtool.GlobToolDescription,
		Schema:      globtool.GlobToolSchema(),
	},
	{
		Name:        greptool.GrepToolName,
		Type:        domain.ToolTypeGrep,
		Description: greptool.GrepToolDescription,
		Schema:      greptool.GrepToolSchema(),
	},
	{
		Name:        memorytool.ProposeAddToolName,
		Type:        domain.ToolTypeMemoryProposeAdd,
		Description: memorytool.ProposeAddToolDescription,
		Schema:      memorytool.ProposeAddToolSchema(),
	},
	{
		Name:        memorytool.SearchToolName,
		Type:        domain.ToolTypeMemorySearch,
		Description: memorytool.SearchToolDescription,
		Schema:      memorytool.SearchToolSchema(),
	},
	{
		Name:        memorytool.ListToolName,
		Type:        domain.ToolTypeMemoryList,
		Description: memorytool.ListToolDescription,
		Schema:      memorytool.ListToolSchema(),
	},
	{
		Name:        memorytool.ProposeUpdateToolName,
		Type:        domain.ToolTypeMemoryProposeUpdate,
		Description: memorytool.ProposeUpdateToolDescription,
		Schema:      memorytool.ProposeUpdateToolSchema(),
	},
	{
		Name:        memorytool.ProposeForgetToolName,
		Type:        domain.ToolTypeMemoryProposeForget,
		Description: memorytool.ProposeForgetToolDescription,
		Schema:      memorytool.ProposeForgetToolSchema(),
	},
	{
		Name:        workflowscheduletool.WorkflowCreateToolName,
		Type:        domain.ToolTypeWorkflowCreate,
		Description: workflowscheduletool.WorkflowCreateToolDescription,
		Schema:      workflowscheduletool.WorkflowCreateToolSchema(),
	},
	{
		Name:        workflowscheduletool.WorkflowUpdateToolName,
		Type:        domain.ToolTypeWorkflowUpdate,
		Description: workflowscheduletool.WorkflowUpdateToolDescription,
		Schema:      workflowscheduletool.WorkflowUpdateToolSchema(),
	},
	{
		Name:        workflowscheduletool.WorkflowDeleteToolName,
		Type:        domain.ToolTypeWorkflowDelete,
		Description: workflowscheduletool.WorkflowDeleteToolDescription,
		Schema:      workflowscheduletool.WorkflowDeleteToolSchema(),
	},
	{
		Name:        workflowscheduletool.WorkflowOperationToolName,
		Type:        domain.ToolTypeWorkflowOperation,
		Description: workflowscheduletool.WorkflowOperationToolDescription,
		Schema:      workflowscheduletool.WorkflowOperationToolSchema(),
	},
	{
		Name:        skilltool.SkillToolName,
		Type:        domain.ToolTypeSkill,
		Description: skilltool.SkillToolDescription,
		Schema:      skilltool.SkillToolSchema(),
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

func ToolResultProcessors() []postprocess.Processor {
	return nil
}

func LLMDefinitions() []llms.Tool {
	return llmDefinitionsFromDefinitions(Definitions())
}

func LLMDefinitionsForTypes(allowed []domain.ToolType) []llms.Tool {
	if len(allowed) == 0 {
		return nil
	}
	allowedSet := make(map[domain.ToolType]bool, len(allowed))
	for _, toolType := range allowed {
		allowedSet[toolType] = true
	}
	registered := Definitions()
	filtered := make([]Definition, 0, len(registered))
	for _, definition := range registered {
		if allowedSet[definition.Type] {
			filtered = append(filtered, definition)
		}
	}
	return llmDefinitionsFromDefinitions(filtered)
}

func ModelToolTypes(includeAgent bool) []domain.ToolType {
	registered := Definitions()
	types := make([]domain.ToolType, 0, len(registered))
	for _, definition := range registered {
		if !includeAgent && definition.Type == domain.ToolTypeAgent {
			continue
		}
		types = append(types, definition.Type)
	}
	return types
}

func SupportsToolType(toolType domain.ToolType) bool {
	_, ok := DefinitionByType(toolType)
	return ok
}

func llmDefinitionsFromDefinitions(registered []Definition) []llms.Tool {
	tools := make([]llms.Tool, 0, len(registered))
	for _, definition := range registered {
		tools = append(tools, llms.Tool{
			Type: "function",
			Function: &llms.FunctionDefinition{
				Name:        definition.Name,
				Description: definition.Description,
				Parameters:  definition.Schema,
				Strict:      strictForDefinition(definition),
			},
		})
	}
	return tools
}

func strictForDefinition(definition Definition) bool {
	switch definition.Type {
	case domain.ToolTypeAgent, domain.ToolTypeWorkflowCreate, domain.ToolTypeWorkflowUpdate, domain.ToolTypeWorkflowDelete, domain.ToolTypeWorkflowOperation:
		return false
	default:
		return true
	}
}

func DefinitionByName(name string) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Name == name {
			return definition, true
		}
	}
	return Definition{}, false
}

func DefinitionByType(toolType domain.ToolType) (Definition, bool) {
	for _, definition := range definitions {
		if definition.Type == toolType {
			return definition, true
		}
	}
	return Definition{}, false
}

func FallbackCandidates(primary domain.ToolType) []domain.ToolType {
	return nil
}
