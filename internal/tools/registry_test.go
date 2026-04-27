package tools

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestDefinitionsIncludeDedicatedTools(t *testing.T) {
	t.Parallel()

	definitions := Definitions()
	if len(definitions) != 6 {
		t.Fatalf("expected six tool definitions, got %d", len(definitions))
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

	seen := map[domain.ToolType]bool{}
	for _, definition := range definitions {
		seen[definition.Type] = true
	}
	for _, toolType := range []domain.ToolType{
		domain.ToolTypeShell,
		domain.ToolTypeRead,
		domain.ToolTypeEdit,
		domain.ToolTypeWrite,
		domain.ToolTypeGlob,
		domain.ToolTypeGrep,
	} {
		if !seen[toolType] {
			t.Fatalf("missing tool type %q in registry", toolType)
		}
	}
}

func TestLLMDefinitionsUseCommandNameAndDescription(t *testing.T) {
	t.Parallel()

	definitions := LLMDefinitions()
	if len(definitions) != 6 || definitions[0].Function == nil {
		t.Fatalf("expected six function definitions, got %#v", definitions)
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

func TestLLMDefinitionSchemasAreStrictToolCompatible(t *testing.T) {
	t.Parallel()

	for _, definition := range LLMDefinitions() {
		if definition.Function == nil {
			t.Fatalf("tool %q is missing function definition", definition.Type)
		}
		rawSchema, ok := definition.Function.Parameters.(json.RawMessage)
		if !ok {
			t.Fatalf("tool %q parameters should be raw JSON schema, got %T", definition.Function.Name, definition.Function.Parameters)
		}

		var schema struct {
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties bool                       `json:"additionalProperties"`
		}
		if err := json.Unmarshal(rawSchema, &schema); err != nil {
			t.Fatalf("decode %s schema: %v", definition.Function.Name, err)
		}
		if len(schema.Properties) == 0 {
			t.Fatalf("%s schema should declare properties", definition.Function.Name)
		}
		if schema.AdditionalProperties {
			t.Fatalf("%s schema should set additionalProperties to false", definition.Function.Name)
		}

		required := map[string]bool{}
		for _, field := range schema.Required {
			required[field] = true
		}
		var missing []string
		for field := range schema.Properties {
			if !required[field] {
				missing = append(missing, field)
			}
		}
		sort.Strings(missing)
		if len(missing) > 0 {
			t.Fatalf("%s schema required array is missing properties: %s", definition.Function.Name, strings.Join(missing, ", "))
		}
		var extra []string
		for field := range required {
			if _, ok := schema.Properties[field]; !ok {
				extra = append(extra, field)
			}
		}
		sort.Strings(extra)
		if len(extra) > 0 {
			t.Fatalf("%s schema required array includes unknown properties: %s", definition.Function.Name, strings.Join(extra, ", "))
		}
	}
}

func TestDefinitionByNameRejectsBashName(t *testing.T) {
	t.Parallel()

	if _, ok := DefinitionByName("Bash"); ok {
		t.Fatalf("Bash should not resolve to a tool definition")
	}
}
