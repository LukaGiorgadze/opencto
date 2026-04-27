package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectorDefinitionsUsePlatformNeutralCommandTool(t *testing.T) {
	t.Parallel()

	definitions := SelectorDefinitions()
	if len(definitions) != 1 {
		t.Fatalf("expected one selector definition, got %d", len(definitions))
	}

	definition := definitions[0]
	if definition.Name != SelectorToolCommandName {
		t.Fatalf("expected tool name %q, got %q", SelectorToolCommandName, definition.Name)
	}
	if strings.Contains(definition.Name, "Bash") {
		t.Fatalf("selector tool name should not assume Bash: %q", definition.Name)
	}
	description := strings.ToLower(definition.Description)
	for _, disallowed := range []string{"bash", "unix", "linux"} {
		if strings.Contains(description, disallowed) {
			t.Fatalf("selector description should not assume %s: %q", disallowed, definition.Description)
		}
	}
	if !strings.Contains(description, "project workspace") || !strings.Contains(description, "current operating system") {
		t.Fatalf("selector description should state workspace and current OS execution: %q", definition.Description)
	}
}

func TestSelectorLLMDefinitionsUseCommandNameAndDescription(t *testing.T) {
	t.Parallel()

	definitions := SelectorLLMDefinitions()
	if len(definitions) != 1 || definitions[0].Function == nil {
		t.Fatalf("expected one function definition, got %#v", definitions)
	}

	function := definitions[0].Function
	if function.Name != SelectorToolCommandName {
		t.Fatalf("expected LLM tool name %q, got %q", SelectorToolCommandName, function.Name)
	}
	if !strings.Contains(function.Description, "current operating system") {
		t.Fatalf("LLM tool description should mention current OS: %q", function.Description)
	}

	var schema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	parameters, ok := function.Parameters.(json.RawMessage)
	if !ok {
		t.Fatalf("expected raw JSON parameters, got %T", function.Parameters)
	}
	if err := json.Unmarshal(parameters, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	commandDescription := schema.Properties["command"].Description
	if !strings.Contains(commandDescription, "Do not assume Bash, sh, cmd, or PowerShell") {
		t.Fatalf("command description should avoid shell assumptions: %q", commandDescription)
	}
}

func TestSelectorDefinitionsDeepCopyParameters(t *testing.T) {
	t.Parallel()

	first := SelectorDefinitions()
	second := SelectorDefinitions()
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("unexpected selector definitions: %#v %#v", first, second)
	}

	original := second[0].Parameters[0]
	first[0].Parameters[0] = 'x'
	if second[0].Parameters[0] != original {
		t.Fatalf("SelectorDefinitions should deep-copy Parameters")
	}

	third := SelectorDefinitions()
	if third[0].Parameters[0] != original {
		t.Fatalf("mutating cloned Parameters should not alter registry state")
	}
}

func TestSelectorDefinitionByNameRejectsBashName(t *testing.T) {
	t.Parallel()

	if _, ok := SelectorDefinitionByName("Bash"); ok {
		t.Fatalf("Bash should not resolve to a selector definition")
	}
}
