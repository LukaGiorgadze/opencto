package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type recordingToolModel struct {
	response *llms.ContentResponse
	options  llms.CallOptions
}

func (m *recordingToolModel) GenerateContent(_ context.Context, _ []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	for _, option := range options {
		option(&m.options)
	}
	return m.response, nil
}

func (m *recordingToolModel) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", nil
}

func TestBuildToolSelectionMessagesSeparatesConversationRoles(t *testing.T) {
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
		CurrentWorkItemID: "wi-1",
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
			Command:         "sh",
			Args:            []string{"-lc", "deploy --target staging"},
			Status:          "failed",
			Observation:     "staging target not found",
			Error:           "exit status 1",
			Metadata: map[string]string{
				"working_directory": "/tmp/opencto",
			},
		},
	}

	messages, err := buildToolSelectionMessages(input)
	if err != nil {
		t.Fatalf("build tool selection messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected system, user, assistant messages, got %d", len(messages))
	}
	if messages[0].Role != llms.ChatMessageTypeSystem {
		t.Fatalf("expected first message to be system, got %q", messages[0].Role)
	}
	if messages[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected second message to be human, got %q", messages[1].Role)
	}
	if messages[2].Role != llms.ChatMessageTypeAI {
		t.Fatalf("expected third message to be assistant, got %q", messages[2].Role)
	}

	prompt := messageText(messages[0])
	for _, want := range []string{
		"Project: OpenCTO (project-1)",
		"Project root: /tmp/opencto",
		"Relevant facts: deployment_target: staging",
		"Execution cycle: 2",
		"Plan summary: Deploy the app safely.",
		"Execution order: [Verify deployment target] -> [Deploy application]",
		"Test strategy: Check the deployed app after rollout.",
		"Work items: Deploy application [id=wi-1,status=ready,tier=2]: Run the deployment command (depends_on=Verify deployment target)",
		"Current work item: Deploy application [id=wi-1,status=ready,tier=2]: Run the deployment command (depends_on=Verify deployment target)",
		"Choose one executable action that advances the current work item's description and acceptance criteria.",
		"Do not select actions for later work items until the workflow makes them current.",
		"Choose the next action now.",
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
	for _, removedToolSchema := range []string{
		"`Shell` is the only execution tool",
		`"tool_choice"`,
		`"command": "executable"`,
		`"args": ["arg1"]`,
	} {
		if strings.Contains(prompt, removedToolSchema) {
			t.Fatalf("system prompt should not include tool schema %q\n%s", removedToolSchema, prompt)
		}
	}
	for _, removed := range []string{
		"Inbound request: deploy the app",
		"Work item: wi-1",
		"Observation: staging target not found",
		"Recent decisions:",
	} {
		if strings.Contains(prompt, removed) {
			t.Fatalf("system prompt should not include conversation message %q\n%s", removed, prompt)
		}
	}

	if got := messageText(messages[1]); got != "deploy the app" {
		t.Fatalf("unexpected human message: %q", got)
	}

	assistantMessage := messageText(messages[2])
	for _, want := range []string{
		"Work item: wi-1",
		"Tool: shell",
		"Input: deploy app",
		"Command: sh",
		`Args: ["-lc","deploy --target staging"]`,
		"Working directory: /tmp/opencto",
		"Status: failed",
		"Observation: staging target not found",
		"Error: exit status 1",
	} {
		if !strings.Contains(assistantMessage, want) {
			t.Fatalf("assistant message missing %q\n%s", want, assistantMessage)
		}
	}
}

func TestSelectToolUsesRegisteredTools(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: "",
				ToolCalls: []llms.ToolCall{{
					ID:   "call-1",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name: toolregistry.SelectorToolShellName,
						Arguments: `{
							"command":"pwd",
							"args":[],
							"working_dir":null,
							"timeout_ms":120000,
							"description":"inspect workspace",
							"destructive":false
						}`,
					},
				}},
			}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	selection, err := engine.SelectTool(context.Background(), agent.ToolSelectionInput{
		ProjectID:         "project-1",
		CurrentWorkItemID: "wi-1",
		Context: agent.Context{
			Event: domain.Event{Body: "inspect workspace"},
		},
		Runtime: agent.RuntimeContext{
			WorkspaceRoot: "/tmp/opencto",
		},
	})
	if err != nil {
		t.Fatalf("SelectTool: %v", err)
	}

	if len(model.options.Tools) != 1 {
		t.Fatalf("expected registered tools to be passed with WithTools, got %#v", model.options.Tools)
	}
	if model.options.Tools[0].Function == nil || model.options.Tools[0].Function.Name != toolregistry.SelectorToolShellName {
		t.Fatalf("unexpected tool definition: %#v", model.options.Tools[0])
	}
	if selection.ToolChoice == nil || selection.ToolChoice.Command != "pwd" {
		t.Fatalf("expected shell tool choice from tool call, got %#v", selection.ToolChoice)
	}
}

func TestSelectToolAcceptsToolCallOnly(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: "",
				ToolCalls: []llms.ToolCall{{
					ID:   "call_mHAF2Zp37v5ajz9YO4Lel1gn",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name: toolregistry.SelectorToolShellName,
						Arguments: `{
							"command":"mkdir",
							"args":["-p","helloworld"],
							"working_dir":null,
							"timeout_ms":120000,
							"description":"Create the helloworld project folder",
							"destructive":false
						}`,
					},
				}},
			}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	selection, err := engine.SelectTool(context.Background(), agent.ToolSelectionInput{
		ProjectID:         "project-1",
		CurrentWorkItemID: "wi-1",
		Context: agent.Context{
			Event: domain.Event{Body: "create helloworld"},
		},
		Runtime: agent.RuntimeContext{
			WorkspaceRoot: "/tmp/opencto",
		},
	})
	if err != nil {
		t.Fatalf("SelectTool: %v", err)
	}
	if selection.ToolChoice == nil {
		t.Fatalf("expected tool choice")
	}
	if selection.ToolChoice.Command != "mkdir" {
		t.Fatalf("unexpected command: %q", selection.ToolChoice.Command)
	}
	if len(selection.ToolChoice.Args) != 2 || selection.ToolChoice.Args[0] != "-p" || selection.ToolChoice.Args[1] != "helloworld" {
		t.Fatalf("unexpected args: %#v", selection.ToolChoice.Args)
	}
	if selection.ToolChoice.WorkingDir != "/tmp/opencto" {
		t.Fatalf("unexpected working dir: %q", selection.ToolChoice.WorkingDir)
	}
}

func TestSelectToolAcceptsMultipleToolCalls(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: "",
				ToolCalls: []llms.ToolCall{
					{
						ID:   "call-1",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name: toolregistry.SelectorToolShellName,
							Arguments: `{
								"command":"pwd",
								"args":[],
								"working_dir":null,
								"timeout_ms":120000,
								"description":"Inspect workspace",
								"destructive":false
							}`,
						},
					},
					{
						ID:   "call-2",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name: toolregistry.SelectorToolShellName,
							Arguments: `{
								"command":"ls",
								"args":["-la"],
								"working_dir":null,
								"timeout_ms":120000,
								"description":"List workspace files",
								"destructive":false
							}`,
						},
					},
				},
			}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	selection, err := engine.SelectTool(context.Background(), agent.ToolSelectionInput{
		ProjectID:         "project-1",
		CurrentWorkItemID: "wi-1",
		Context: agent.Context{
			Event: domain.Event{Body: "inspect workspace"},
		},
		Runtime: agent.RuntimeContext{
			WorkspaceRoot: "/tmp/opencto",
		},
	})
	if err != nil {
		t.Fatalf("SelectTool: %v", err)
	}

	if selection.ToolChoice == nil || selection.ToolChoice.Command != "pwd" {
		t.Fatalf("expected first call as active tool choice, got %#v", selection.ToolChoice)
	}
	if len(selection.ToolChoices) != 1 || selection.ToolChoices[0].Command != "ls" {
		t.Fatalf("expected queued second tool choice, got %#v", selection.ToolChoices)
	}
	if selection.ToolChoice.Metadata["work_item_id"] != "wi-1" || selection.ToolChoices[0].Metadata["work_item_id"] != "wi-1" {
		t.Fatalf("expected work item metadata on all choices: active=%#v queued=%#v", selection.ToolChoice.Metadata, selection.ToolChoices[0].Metadata)
	}
	if selection.ToolChoice.Metadata["tool_call_index"] != "0" || selection.ToolChoices[0].Metadata["tool_call_index"] != "1" {
		t.Fatalf("expected tool call indexes: active=%#v queued=%#v", selection.ToolChoice.Metadata, selection.ToolChoices[0].Metadata)
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
	}, input)
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

func TestToolChoiceFromShellToolCallWrapsCompoundCommand(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		Context: agent.Context{
			Event: domain.Event{Body: "inspect workspace"},
		},
		Runtime: agent.RuntimeContext{
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
				"command":"pwd; ls -la",
				"args":[],
				"working_dir":null,
				"timeout_ms":120000,
				"description":"inspect workspace",
				"destructive":false
			}`,
		},
	}, input)
	if err != nil {
		t.Fatalf("toolChoiceFromToolCall: %v", err)
	}

	if choice.Command != "/bin/zsh" {
		t.Fatalf("expected shell wrapper command, got %q", choice.Command)
	}
	if len(choice.Args) != 2 || choice.Args[0] != "-lc" || choice.Args[1] != "pwd; ls -la" {
		t.Fatalf("unexpected wrapped args: %#v", choice.Args)
	}
	if choice.Metadata["wrapped_shell_command"] != "true" {
		t.Fatalf("expected wrapped shell metadata, got %#v", choice.Metadata)
	}
}

func TestToolSelectionFromContentResponseRejectsContentOnly(t *testing.T) {
	t.Parallel()

	input := agent.ToolSelectionInput{
		CurrentWorkItemID: "wi-1",
	}
	_, err := toolSelectionFromContentResponse(&llms.ContentResponse{
		Choices: []*llms.ContentChoice{
			{Content: `{"response_message":"done"}`},
		},
	}, input)
	if err == nil {
		t.Fatalf("expected content-only response to fail")
	}
}

func TestDecodeJSONOutputRejectsRepeatedObject(t *testing.T) {
	t.Parallel()

	raw := `{"action":"continue","work_item_id":"wi-1","tool_choice":{"type":"shell"}}` + "\n" +
		`{"tool_choice":{"type":"shell"},"work_item_id":"wi-1","action":"continue"}`

	_, err := decodeJSONOutput[map[string]any](raw)
	if err == nil {
		t.Fatalf("expected repeated JSON values to fail")
	}
	if !strings.Contains(err.Error(), "multiple JSON values") {
		t.Fatalf("unexpected error: %v", err)
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

func messageText(message llms.MessageContent) string {
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		text, ok := part.(llms.TextContent)
		if ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}
