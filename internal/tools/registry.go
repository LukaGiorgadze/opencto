package tools

import (
	"encoding/json"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/domain"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
	planningtool "github.com/opencto/opencto/internal/tools/planning"
	readtool "github.com/opencto/opencto/internal/tools/read"
	scheduletool "github.com/opencto/opencto/internal/tools/schedule"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
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
		Name:        planningtool.AskUserQuestionToolName,
		Type:        domain.ToolTypeAskUserQuestion,
		Description: planningtool.AskUserQuestionToolDescription,
		Schema:      planningtool.AskUserQuestionToolSchema(),
	},
	{
		Name:        planningtool.ProposePlanToolName,
		Type:        domain.ToolTypeProposePlan,
		Description: planningtool.ProposePlanToolDescription,
		Schema:      planningtool.ProposePlanToolSchema(),
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
		Name:        scheduletool.ToolName,
		Type:        domain.ToolTypeSchedule,
		Description: scheduletool.ToolDescription,
		Schema:      scheduletool.ToolSchema(),
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
