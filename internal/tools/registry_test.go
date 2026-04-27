package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefinitionsUsePlatformNeutralCommandTool(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	if len(definitions) != 1 {
		t.Fatalf("expected one tool definition, got %d", len(definitions))
	}

	definition := definitions[0]
	if definition.Name != CommandToolName {
		t.Fatalf("expected tool name %q, got %q", CommandToolName, definition.Name)
	}
	if strings.Contains(definition.Name, "Bash") {
		t.Fatalf("tool name should not assume Bash: %q", definition.Name)
	}
	description := strings.ToLower(definition.Description)
	for _, disallowed := range []string{"bash", "unix", "linux"} {
		if strings.Contains(description, disallowed) {
			t.Fatalf("tool description should not assume %s: %q", disallowed, definition.Description)
		}
	}
	if !strings.Contains(description, "project workspace") || !strings.Contains(description, "current operating system") {
		t.Fatalf("tool description should state workspace and current OS execution: %q", definition.Description)
	}
}

func TestLLMDefinitionsUseCommandNameAndDescription(t *testing.T) {
	t.Parallel()

	definitions := LLMDefinitions()
	if len(definitions) != 1 || definitions[0].Function == nil {
		t.Fatalf("expected one function definition, got %#v", definitions)
	}

	function := definitions[0].Function
	if function.Name != CommandToolName {
		t.Fatalf("expected LLM tool name %q, got %q", CommandToolName, function.Name)
	}
	if !strings.Contains(function.Description, "current operating system") {
		t.Fatalf("LLM tool description should mention current OS: %q", function.Description)
	}

	var decodedSchema struct {
		Properties map[string]struct {
			Description string `json:"description"`
		} `json:"properties"`
	}
	rawSchema, ok := function.Parameters.(json.RawMessage)
	if !ok {
		t.Fatalf("expected raw JSON schema, got %T", function.Parameters)
	}
	if err := json.Unmarshal(rawSchema, &decodedSchema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	commandDescription := decodedSchema.Properties["command"].Description
	if !strings.Contains(commandDescription, "Do not assume Bash, sh, cmd, or PowerShell") {
		t.Fatalf("command description should avoid shell assumptions: %q", commandDescription)
	}
}

func TestDefinitionsDeepCopySchema(t *testing.T) {
	t.Parallel()

	first := Definitions()
	second := Definitions()
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("unexpected tool definitions: %#v %#v", first, second)
	}

	original := second[0].Schema[0]
	first[0].Schema[0] = 'x'
	if second[0].Schema[0] != original {
		t.Fatalf("Definitions should deep-copy Schema")
	}

	third := Definitions()
	if third[0].Schema[0] != original {
		t.Fatalf("mutating cloned Schema should not alter registry state")
	}
}

func TestDefinitionByNameRejectsBashName(t *testing.T) {
	t.Parallel()

	if _, ok := DefinitionByName("Bash"); ok {
		t.Fatalf("Bash should not resolve to a tool definition")
	}
}
