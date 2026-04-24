package llm

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

func TestRenderToolSelectionPromptUsesRenderedSystemPrompt(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{
				ID:          "project-1",
				Name:        "OpenCTO",
				Description: "Self-hosted AI technical co-founder",
			},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "deploy the app",
			},
			ProjectFacts: []domain.MemoryFact{{
				ID:        "fact-1",
				Key:       "deployment_target",
				Value:     "staging",
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			OpenContradictions: []domain.PendingContradiction{{
				ID:        "contr-1",
				Topic:     "deployment platform",
				Status:    domain.ContradictionStatusOpen,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			ActiveWorkItems: []domain.WorkItem{{
				ID:        "wi-1",
				Title:     "Ship deployment",
				Status:    domain.WorkItemStatusReady,
				RiskTier:  domain.RiskTierConsequential,
				CreatedAt: time.Now().UTC(),
				UpdatedAt: time.Now().UTC(),
			}},
			RecentDecisions: []domain.ADR{{
				ID:        "adr-1",
				Title:     "Use Temporal",
				Summary:   "Temporal coordinates long-running work.",
				CreatedAt: time.Now().UTC(),
			}},
		},
		Classification: agent.Classification{
			Intent:   agent.ClassificationIntentActionRequest,
			RoutedTo: agent.ClassificationRoutePlan,
			Tier:     domain.RiskTierConsequential,
			Summary:  "Deployment requested.",
		},
		Plan: domain.Plan{
			Summary: "Deploy the app safely.",
			Metadata: map[string]string{
				"assumptions_json":     `["Use the existing deployment workflow"]`,
				"risks_json":           `["Credentials may be missing"]`,
				"execution_order_json": `[["Verify deployment target"],["Deploy application"]]`,
				"test_strategy":        "Check the deployed app after rollout.",
			},
			Steps: []domain.PlanStep{{
				ID:          "step-1",
				Title:       "Verify deployment target",
				Description: "Confirm the platform before deployment.",
			}},
		},
		WorkItems: []domain.WorkItem{{
			ID:          "wi-1",
			Title:       "Deploy application",
			Description: "Run the deployment command",
			Status:      domain.WorkItemStatusReady,
			RiskTier:    domain.RiskTierConsequential,
			Metadata: map[string]string{
				"acceptance_criteria_json": `["Deployment status is Ready"]`,
				"rollback":                 "Rollback to the previous version.",
				"skills_json":              `["vercel"]`,
				"depends_on_json":          `["Verify deployment target"]`,
				"complexity":               "M",
			},
		}},
		Runtime: agent.RuntimeContext{
			OS:            "darwin",
			Arch:          "arm64",
			Shell:         "/bin/zsh",
			WorkspaceRoot: "/tmp/opencto",
		},
		ExecutionCycle: 2,
		LastObservation: &agent.ExecutionFeedback{
			Cycle:           1,
			WorkItemID:      "wi-1",
			Tool:            domain.ToolTypeShell,
			RequestedAction: "deploy app",
			Status:          "failed",
			Observation:     "staging target not found",
			Error:           "exit status 1",
		},
	}

	prompt, err := renderToolSelectionPrompt(input)
	if err != nil {
		t.Fatalf("render tool selection prompt: %v", err)
	}

	for _, want := range []string{
		"Project: OpenCTO (project-1)",
		"Project root: /tmp/opencto",
		"Relevant facts: deployment_target: staging",
		"Inbound request: deploy the app",
		"Execution cycle: 2",
		"Plan summary: Deploy the app safely.",
		"Execution order: [Verify deployment target] -> [Deploy application]",
		"Test strategy: Check the deployed app after rollout.",
		"Work items: Deploy application [id=wi-1,status=ready,tier=2]: Run the deployment command (depends_on=Verify deployment target)",
		"Work item: wi-1",
		"Tool: shell",
		"Observation: staging target not found",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "{{") {
		t.Fatalf("prompt still contains template markers:\n%s", prompt)
	}
	if strings.Contains(prompt, "Reply") {
		t.Fatalf("prompt should not mention Reply as a tool:\n%s", prompt)
	}
}

func TestToolChoiceFromShellToolCallWrapsCommand(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		Context: agent.Context{
			Event: domain.Event{Body: "run tests"},
		},
		Runtime: agent.RuntimeContext{
			OS:            "darwin",
			Shell:         "/bin/zsh",
			WorkspaceRoot: "/tmp/opencto",
		},
	}

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "call-1",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name: toolregistry.SelectorToolShellName,
			Arguments: `{
				"command":"go",
				"args":["test","./..."],
				"working_dir":"internal",
				"timeout_ms":45000,
				"description":"run the Go test suite",
				"destructive":false
			}`,
		},
	}, input, false)
	if err != nil {
		t.Fatalf("toolChoiceFromToolCall: %v", err)
	}

	if choice.Type != domain.ToolTypeShell {
		t.Fatalf("expected shell tool, got %q", choice.Type)
	}
	if choice.Command != "go" {
		t.Fatalf("expected model-selected command, got %q", choice.Command)
	}
	if len(choice.Args) != 2 || choice.Args[0] != "test" || choice.Args[1] != "./..." {
		t.Fatalf("unexpected args: %#v", choice.Args)
	}
	if choice.WorkingDir != "internal" {
		t.Fatalf("unexpected working dir: %q", choice.WorkingDir)
	}
	if choice.TimeoutMs != 45000 {
		t.Fatalf("unexpected timeout: %d", choice.TimeoutMs)
	}
	if choice.Intent != "run the Go test suite" {
		t.Fatalf("unexpected intent: %q", choice.Intent)
	}
	if choice.Metadata["model_tool"] != toolregistry.SelectorToolShellName {
		t.Fatalf("expected model_tool metadata, got %#v", choice.Metadata)
	}
}

func TestToolChoiceFromContentReturnsDirectReply(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		Context: agent.Context{
			Event: domain.Event{Body: "what failed?"},
		},
	}

	choice, err := toolChoiceFromContentResponse(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{
			Content: "The deployment is blocked on missing credentials.",
		}},
	}, input)
	if err != nil {
		t.Fatalf("toolChoiceFromContentResponse: %v", err)
	}

	if choice.ResponseMessage != "The deployment is blocked on missing credentials." {
		t.Fatalf("unexpected response message: %q", choice.ResponseMessage)
	}
	if choice.Type != "" {
		t.Fatalf("expected direct reply to have no tool type, got %q", choice.Type)
	}
	if choice.Metadata["model_tool"] != "content_fallback" {
		t.Fatalf("unexpected metadata: %#v", choice.Metadata)
	}
}

func TestToolChoiceFromContentRejectsEmptyMessage(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		Context: agent.Context{
			Event: domain.Event{Body: "what failed?"},
		},
	}

	_, err := toolChoiceFromContentResponse(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{{Content: "   "}},
	}, input)
	if err == nil {
		t.Fatalf("expected validation error for empty reply message")
	}
}

func TestOpenCTOToolDefinitionsUseStrictCompatibleRequiredLists(t *testing.T) {
	t.Parallel()

	type schema struct {
		Required   []string               `json:"required"`
		Properties map[string]interface{} `json:"properties"`
	}

	for _, definition := range toolregistry.SelectorDefinitions() {
		tools := toolregistry.SelectorLLMDefinitions()
		var matched *llms.Tool
		for idx := range tools {
			if tools[idx].Function != nil && tools[idx].Function.Name == definition.Name {
				matched = &tools[idx]
				break
			}
		}
		if matched == nil || matched.Function == nil {
			t.Fatalf("tool missing function definition for %s", definition.Name)
		}

		body, err := json.Marshal(matched.Function.Parameters)
		if err != nil {
			t.Fatalf("marshal schema for %s: %v", definition.Name, err)
		}

		var parsed schema
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("unmarshal schema for %s: %v", definition.Name, err)
		}

		if len(parsed.Required) != len(parsed.Properties) {
			t.Fatalf("tool %s required list does not cover all properties: required=%v properties=%v", definition.Name, parsed.Required, parsed.Properties)
		}

		required := make(map[string]struct{}, len(parsed.Required))
		for _, name := range parsed.Required {
			required[name] = struct{}{}
		}
		for name := range parsed.Properties {
			if _, ok := required[name]; !ok {
				t.Fatalf("tool %s missing property %q from required list", definition.Name, name)
			}
		}
	}
	if len(toolregistry.SelectorDefinitions()) != 1 {
		t.Fatalf("expected exactly one selector tool, got %d", len(toolregistry.SelectorDefinitions()))
	}
}
