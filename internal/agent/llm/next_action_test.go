package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type recordingToolModel struct {
	response *llms.ContentResponse
	options  llms.CallOptions
	messages []llms.MessageContent
}

func (m *recordingToolModel) GenerateContent(_ context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error) {
	m.messages = append([]llms.MessageContent(nil), messages...)
	for _, option := range options {
		option(&m.options)
	}
	return m.response, nil
}

func (m *recordingToolModel) Call(_ context.Context, _ string, _ ...llms.CallOption) (string, error) {
	return "", nil
}

func messageText(message llms.MessageContent) string {
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if text, ok := part.(llms.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "")
}

func TestBuildNextActionMessagesUsesOpenAIToolTranscript(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "deploy the app",
			},
		},
		Runtime: agent.RuntimeContext{
			OS:            "darwin",
			Arch:          "arm64",
			Shell:         "/bin/zsh",
			Path:          "/usr/bin:/bin",
			WorkspaceRoot: "/tmp/opencto",
		},
		ExecutionCycle: 2,
		ObservationHistory: []agent.ExecutionFeedback{{
			Cycle:           1,
			WorkItemID:      "wi-1",
			ToolCallID:      "toolu_abc123",
			Tool:            domain.ToolTypeShell,
			RequestedAction: "deploy app",
			Command:         "/bin/zsh",
			Args:            []string{"-lc", "deploy --target staging"},
			Status:          string(domain.ExecutionStatusFailed),
			Observation:     "staging target not found",
			Error:           "exit status 1",
			Metadata: map[string]string{
				"assistant_text":    "I'll deploy the app to staging.",
				"working_directory": "/tmp/opencto",
				"result_code":       "1",
				"original_command":  "deploy --target staging",
				"timeout_ms":        "120000",
			},
		}},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected system, user, assistant tool call, and tool result messages, got %d", len(messages))
	}
	if messages[0].Role != llms.ChatMessageTypeSystem || messages[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("unexpected leading roles: %q %q", messages[0].Role, messages[1].Role)
	}
	if got := messageText(messages[1]); got != "deploy the app" {
		t.Fatalf("unexpected user message: %q", got)
	}

	prompt := messageText(messages[0])
	for _, want := range []string{
		"OS: darwin",
		"Architecture: arm64",
		"Shell: /bin/zsh",
		"Project root: /tmp/opencto",
		"PATH: /usr/bin:/bin",
		"User request: deploy the app",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	for _, removed := range []string{"Work items:", "Current work item:"} {
		if strings.Contains(prompt, removed) {
			t.Fatalf("prompt still contains removed work item field %q\n%s", removed, prompt)
		}
	}

	assistant := messages[2]
	if assistant.Role != llms.ChatMessageTypeAI || len(assistant.Parts) != 2 {
		t.Fatalf("expected assistant text plus tool call, got %#v", assistant)
	}
	toolCall, ok := assistant.Parts[1].(llms.ToolCall)
	if !ok {
		t.Fatalf("expected assistant second part to be tool call, got %T", assistant.Parts[1])
	}
	if toolCall.ID != "toolu_abc123" || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != toolregistry.SelectorToolShellName {
		t.Fatalf("unexpected tool call: %#v", toolCall)
	}
	if !strings.Contains(toolCall.FunctionCall.Arguments, `"work_item_id":"wi-1"`) {
		t.Fatalf("tool call arguments missing work item id: %s", toolCall.FunctionCall.Arguments)
	}
	if !strings.Contains(toolCall.FunctionCall.Arguments, `"command":"deploy --target staging"`) {
		t.Fatalf("tool call arguments should use original command: %s", toolCall.FunctionCall.Arguments)
	}

	result := messages[3]
	if result.Role != llms.ChatMessageTypeTool || len(result.Parts) != 1 {
		t.Fatalf("expected tool result message, got %#v", result)
	}
	toolResult, ok := result.Parts[0].(llms.ToolCallResponse)
	if !ok {
		t.Fatalf("expected tool result part, got %T", result.Parts[0])
	}
	if toolResult.ToolCallID != "toolu_abc123" {
		t.Fatalf("tool result id %q did not match tool call", toolResult.ToolCallID)
	}
	if !strings.Contains(toolResult.Content, "exit_code: 1") ||
		!strings.Contains(toolResult.Content, "output:\nstaging target not found") ||
		!strings.Contains(toolResult.Content, "error:\nexit status 1") {
		t.Fatalf("tool result should include failure status and exit code: %q", toolResult.Content)
	}
}

func TestNextActionReturnsSingleToolChoice(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: "I'll inspect the workspace.",
				ToolCalls: []llms.ToolCall{{
					ID:   "toolu_next",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name: toolregistry.SelectorToolShellName,
						Arguments: `{
							"command":"pwd",
							"args":[],
							"working_dir":null,
							"timeout_ms":120000,
							"description":"inspect workspace",
							"destructive":false,
							"work_item_id":"wi-1"
						}`,
					},
				}},
			}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1"},
			Event:   domain.Event{Body: "inspect workspace"},
		},
	})
	if err != nil {
		t.Fatalf("NextAction: %v", err)
	}
	if output.Status != "tool" || output.ToolChoice == nil {
		t.Fatalf("expected tool output, got %#v", output)
	}
	if output.ToolChoice.ToolCallID != "toolu_next" || output.ToolChoice.Metadata["tool_call_id"] != "toolu_next" {
		t.Fatalf("tool call id was not preserved: %#v", output.ToolChoice)
	}
	if output.WorkItemID != "wi-1" {
		t.Fatalf("unexpected work item id: %q", output.WorkItemID)
	}
	if len(model.options.Tools) != 1 {
		t.Fatalf("expected Bash tool schema, got %#v", model.options.Tools)
	}
}

func TestNextActionReturnsTerminalAnswer(t *testing.T) {
	t.Parallel()

	engine := &OpenAIEngine{
		reasoningModel: &recordingToolModel{
			response: &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{
					Content: `{"status":"completed","final_answer":"Report created."}`,
				}},
			},
		},
	}

	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		Context: agent.Context{Event: domain.Event{Body: "create report"}},
	})
	if err != nil {
		t.Fatalf("NextAction final: %v", err)
	}
	if output.Status != "completed" || output.FinalAnswer != "Report created." {
		t.Fatalf("unexpected final output: %#v", output)
	}
}
