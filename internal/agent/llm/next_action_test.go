package llm

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/skills"
	toolregistry "github.com/opencto/opencto/internal/tools"
	readtool "github.com/opencto/opencto/internal/tools/read"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
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

type fakeAudioTranscriber struct {
	transcripts map[string]string
}

func (t fakeAudioTranscriber) TranscribeAudio(_ context.Context, attachment domain.EventAttachment) (string, error) {
	return t.transcripts[attachment.LocalPath], nil
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
			OpenCTORoot:   "/home/luka/projects/opencto",
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
		"`$OPENCTO_ROOT`: /home/luka/projects/opencto",
		"Meaning: READ ONLY! The OpenCTO source repository",
		"`$OPENCTO_WORKSPACE`: /tmp/opencto",
		"Meaning: Stores projects, artifacts, data, screenshots, logs, and related files.",
		"PATH: /usr/bin:/bin",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q\n%s", want, prompt)
		}
	}
	for _, removed := range []string{"Work items:", "Current work item:", "## Current Task", "User request:", "Execution cycle:", "deploy the app"} {
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
	if toolCall.ID != "toolu_abc123" || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != toolregistry.CommandToolName {
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

func TestBuildNextActionMessagesAppendsAdditionalEventsAsUserMessages(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "create a folder",
			},
			AdditionalEvents: []domain.Event{{
				ID:        "event-2",
				ProjectID: "project-1",
				ActorName: "lukagiorgazde",
				Body:      "tell me my public ip",
			}},
		},
		ObservationHistory: []agent.ExecutionFeedback{{
			Cycle:           1,
			WorkItemID:      "wi-1",
			ToolCallID:      "toolu_abc123",
			Tool:            domain.ToolTypeShell,
			RequestedAction: "create a folder",
			Command:         "mkdir",
			Args:            []string{"example2"},
			Status:          string(domain.ExecutionStatusSucceeded),
			Observation:     "created",
			Metadata: map[string]string{
				"assistant_text": "I'll create the folder.",
				"result_code":    "0",
			},
		}},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 5 {
		t.Fatalf("expected system, initial user, assistant, tool, and additional user messages, got %d", len(messages))
	}
	if messages[4].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected additional context as human message, got %q", messages[4].Role)
	}
	if got := messageText(messages[4]); got != "tell me my public ip" {
		t.Fatalf("unexpected additional user message: %q", got)
	}

	systemPrompt := messageText(messages[0])
	for _, removed := range []string{"create a folder", "tell me my public ip", "Additional user context"} {
		if strings.Contains(systemPrompt, removed) {
			t.Fatalf("system prompt should not contain task-specific text %q\n%s", removed, systemPrompt)
		}
	}
}

func TestBuildNextActionMessagesAddsSkillReminderAsUserMessage(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "add test coverage",
			},
			Skills: []skills.Summary{{
				ID:          "go-testing",
				Name:        "Go Testing",
				Description: "Use when adding or fixing Go tests.",
				Path:        "/repo/skills/go-testing/SKILL.md",
			}},
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected system, skill reminder, and user messages, got %d", len(messages))
	}
	if messages[1].Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected skill reminder as human message, got %q", messages[1].Role)
	}
	systemPrompt := messageText(messages[0])
	if !strings.Contains(systemPrompt, "skills/<skill_id>/SKILL.md") {
		t.Fatalf("system prompt should include concise skill path rule:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "go-testing") || strings.Contains(systemPrompt, "Use when adding or fixing Go tests.") {
		t.Fatalf("system prompt should not include skill catalog entries:\n%s", systemPrompt)
	}
	reminder := messageText(messages[1])
	if !strings.Contains(reminder, "<system-reminder>") ||
		!strings.Contains(reminder, "- go-testing: Use when adding or fixing Go tests.") {
		t.Fatalf("unexpected skill reminder:\n%s", reminder)
	}
	if got := messageText(messages[2]); got != "add test coverage" {
		t.Fatalf("unexpected user message: %q", got)
	}
}

func TestBuildNextActionMessagesIncludesEventAttachments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imagePath := dir + "/photo.png"
	audioPath := dir + "/voice.wav"
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o600); err != nil {
		t.Fatalf("write image attachment: %v", err)
	}
	if err := os.WriteFile(audioPath, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatalf("write audio attachment: %v", err)
	}

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "read these files",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{
						{
							ID:          "attachment-1",
							ProjectID:   "project-1",
							EventID:     "event-1",
							Filename:    "photo.png",
							ContentType: "image/png",
							SizeBytes:   8,
							LocalPath:   imagePath,
						},
						{
							ID:          "attachment-2",
							ProjectID:   "project-1",
							EventID:     "event-1",
							Filename:    "voice.wav",
							ContentType: "audio/wav",
							SizeBytes:   4,
							LocalPath:   audioPath,
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %d", len(messages))
	}
	user := messages[1]
	if user.Role != llms.ChatMessageTypeHuman {
		t.Fatalf("expected human message, got %q", user.Role)
	}

	var hasImage, hasAudioReference bool
	for _, part := range user.Parts {
		switch value := part.(type) {
		case llms.TextContent:
			hasAudioReference = strings.Contains(value.Text, "voice.wav") &&
				strings.Contains(value.Text, "audio/wav") &&
				strings.Contains(value.Text, audioPath)
		case llms.ImageURLContent:
			hasImage = strings.HasPrefix(value.URL, "data:image/png;base64,")
		case llms.BinaryContent:
			t.Fatalf("OpenAI chat messages must not include raw binary parts: %#v", value)
		}
	}
	if !hasImage {
		t.Fatalf("expected image attachment to be included as image content: %#v", user.Parts)
	}
	if !hasAudioReference {
		t.Fatalf("expected audio attachment to be included as a local file reference: %#v", user.Parts)
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
						Name: toolregistry.CommandToolName,
						Arguments: `{
							"command":"pwd",
							"args":[],
							"timeout_ms":120000,
							"run_mode":"wait_for_exit",
							"idempotency":"read_only",
							"process_scope":"task",
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
	if output.ToolChoice.RunMode != domain.ToolRunModeWaitForExit || output.ToolChoice.Idempotency != domain.ToolIdempotencyReadOnly || output.ToolChoice.ProcessScope != domain.ProcessScopeTask {
		t.Fatalf("tool execution metadata was not preserved: %#v", output.ToolChoice)
	}
	if len(model.options.Tools) != 7 {
		t.Fatalf("expected all tool schemas, got %#v", model.options.Tools)
	}
}

func TestNextActionCombinesMultipleShellToolCalls(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				ToolCalls: []llms.ToolCall{
					{
						ID:   "toolu_pwd",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name: toolregistry.CommandToolName,
							Arguments: `{
								"command":"pwd",
								"args":[],
								"timeout_ms":10000,
								"run_mode":"wait_for_exit",
								"idempotency":"read_only",
								"process_scope":"task",
								"description":"confirm workspace",
								"destructive":false,
								"work_item_id":"wi-1"
							}`,
						},
					},
					{
						ID:   "toolu_uname",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name: toolregistry.CommandToolName,
							Arguments: `{
								"command":"uname",
								"args":["-a"],
								"timeout_ms":10000,
								"run_mode":"wait_for_exit",
								"idempotency":"read_only",
								"process_scope":"task",
								"description":"capture platform",
								"destructive":false,
								"work_item_id":"wi-1"
							}`,
						},
					},
				},
			}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1"},
			Event:   domain.Event{Body: "inspect system"},
		},
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("NextAction: %v", err)
	}
	if output.ToolChoice == nil || output.ToolChoice.Metadata["multi_action"] != "true" {
		t.Fatalf("expected multi-action shell choice, got %#v", output.ToolChoice)
	}
	var batch shelltool.BatchInput
	if err := json.Unmarshal(output.ToolChoice.Input, &batch); err != nil {
		t.Fatalf("decode batch input: %v", err)
	}
	if len(batch.Actions) != 2 || batch.Actions[0].Command != "pwd" || batch.Actions[1].Command != "uname" {
		t.Fatalf("unexpected batch actions: %#v", batch.Actions)
	}
	if output.WorkItemID != "wi-1" {
		t.Fatalf("unexpected work item id: %q", output.WorkItemID)
	}
}

func TestToolChoicePreservesCommandAndArgsForDirectExecution(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_direct",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name: toolregistry.CommandToolName,
			Arguments: `{
				"command":"go",
				"args":["test","./..."],
				"timeout_ms":120000,
				"run_mode":"wait_for_exit",
				"idempotency":"idempotent",
				"process_scope":"task",
				"description":"run tests",
				"destructive":false,
				"work_item_id":"wi-1"
			}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "linux", Shell: "/bin/bash", WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Command != "go" {
		t.Fatalf("expected direct command go, got %q", choice.Command)
	}
	if got := strings.Join(choice.Args, "\x00"); got != "test\x00./..." {
		t.Fatalf("expected direct args to be preserved, got %#v", choice.Args)
	}
	if choice.Metadata["wrapped_shell_command"] == "true" {
		t.Fatalf("direct command should not be shell wrapped: %#v", choice.Metadata)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyIdempotent || choice.ProcessScope != domain.ProcessScopeTask {
		t.Fatalf("expected execution metadata to be preserved, got %#v", choice)
	}
}

func TestToolChoiceCapturesStructuredReadInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_read",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      readtool.ReadToolName,
			Arguments: `{"file_path":"/workspace/main.go","offset":10,"limit":20}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeRead {
		t.Fatalf("expected read tool type, got %q", choice.Type)
	}
	if !strings.Contains(string(choice.Input), `"/workspace/main.go"`) {
		t.Fatalf("expected raw read input to be preserved, got %s", choice.Input)
	}
	if choice.Metadata["model_tool"] != readtool.ReadToolName {
		t.Fatalf("expected model tool metadata, got %#v", choice.Metadata)
	}
}

func TestToolChoiceCapturesSkillInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_skill",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      skilltool.SkillToolName,
			Arguments: `{"skill_id":"go-testing"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeSkill {
		t.Fatalf("expected skill tool type, got %q", choice.Type)
	}
	if !strings.Contains(string(choice.Input), `"go-testing"`) {
		t.Fatalf("expected raw skill input to be preserved, got %s", choice.Input)
	}
	if choice.Metadata["model_tool"] != skilltool.SkillToolName {
		t.Fatalf("expected model tool metadata, got %#v", choice.Metadata)
	}
}

func TestToolChoiceSplitsPlainCommandStringWithoutShell(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_plain",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"go test ./...","args":[],"description":"run tests"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "darwin", Shell: "/bin/zsh", WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Command != "go" {
		t.Fatalf("expected plain command string to split to direct executable, got %q", choice.Command)
	}
	if got := strings.Join(choice.Args, "\x00"); got != "test\x00./..." {
		t.Fatalf("expected split args, got %#v", choice.Args)
	}
	if choice.Metadata["model_tool"] != toolregistry.CommandToolName {
		t.Fatalf("expected canonical model tool, got %#v", choice.Metadata)
	}
	if choice.WorkingDir != "/workspace" {
		t.Fatalf("expected shell choice to default to workspace, got %q", choice.WorkingDir)
	}
}

func TestToolChoiceUsesOSAwareShellOnlyForShellSyntax(t *testing.T) {
	t.Parallel()

	linuxChoice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_shell",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"printf hi | wc -c","args":[],"description":"count bytes"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "linux", Shell: "/bin/bash", WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("linux tool choice: %v", err)
	}
	if linuxChoice.Command != "/bin/bash" || strings.Join(linuxChoice.Args, "\x00") != "-c\x00printf hi | wc -c" {
		t.Fatalf("expected OS shell for shell syntax, got %q %#v", linuxChoice.Command, linuxChoice.Args)
	}

	windowsChoice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_windows_shell",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"dir | findstr go.mod","args":[],"description":"find go module"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "windows", WorkspaceRoot: `C:\workspace`},
	})
	if err != nil {
		t.Fatalf("windows tool choice: %v", err)
	}
	if windowsChoice.Command != "cmd" || strings.Join(windowsChoice.Args, "\x00") != "/C\x00dir | findstr go.mod" {
		t.Fatalf("expected Windows shell for shell syntax, got %q %#v", windowsChoice.Command, windowsChoice.Args)
	}
}

func TestNextActionTranscribesAudioAttachmentsBeforePlanning(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	audioPath := dir + "/voice-message.ogg"
	if err := os.WriteFile(audioPath, []byte("ogg data"), 0o600); err != nil {
		t.Fatalf("write audio attachment: %v", err)
	}
	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: `{"status":"completed","final_answer":"I heard: run tests."}`,
			}},
		},
	}
	engine := &OpenAIEngine{
		reasoningModel: model,
		audioTranscriber: fakeAudioTranscriber{transcripts: map[string]string{
			audioPath: "run tests",
		}},
	}

	_, err := engine.NextAction(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "Uploaded attachment(s): voice-message.ogg (audio/ogg)",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{{
						ID:          "attachment-1",
						ProjectID:   "project-1",
						EventID:     "event-1",
						Filename:    "voice-message.ogg",
						ContentType: "audio/ogg",
						SizeBytes:   8,
						LocalPath:   audioPath,
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NextAction: %v", err)
	}
	if len(model.messages) != 2 {
		t.Fatalf("expected system and user messages, got %d", len(model.messages))
	}
	systemText := messageText(model.messages[0])
	if strings.Contains(systemText, "Voice message transcript") {
		t.Fatalf("system prompt should not contain task transcript: %s", systemText)
	}
	userText := messageText(model.messages[1])
	if !strings.Contains(userText, "Voice message transcript (voice-message.ogg): run tests") {
		t.Fatalf("user message missing transcript: %s", userText)
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
