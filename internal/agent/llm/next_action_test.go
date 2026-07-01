package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/media"
	"github.com/opencto/opencto/internal/skills"
	toolregistry "github.com/opencto/opencto/internal/tools"
	agenttool "github.com/opencto/opencto/internal/tools/agenttool"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
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

func messagesContainText(messages []llms.MessageContent, text string) bool {
	for _, message := range messages {
		if strings.Contains(messageText(message), text) {
			return true
		}
	}
	return false
}

func countImageParts(message llms.MessageContent) int {
	count := 0
	for _, part := range message.Parts {
		if _, ok := part.(llms.ImageURLContent); ok {
			count++
		}
	}
	return count
}

func validTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buffer.Bytes()
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
			Exec:          "/bin/zsh",
			Path:          "/usr/bin:/bin",
			WorkspaceRoot: "/tmp/opencto",
		},
		ExecutionCycle: 2,
		ObservationHistory: []agent.ExecutionFeedback{{
			Cycle:           1,
			WorkItemID:      "wi-1",
			ToolCallID:      "toolu_abc123",
			Tool:            domain.ToolTypeExec,
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
		"Exec: /bin/zsh",
		"`$OPENCTO_WORKSPACE`: /tmp/opencto",
		"Projects, scheduled workflow source and run snapshots, artifacts, data/db, screenshots, logs, and related files.",
		"Default working directory for all user project work",
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
	if assistant.Role != llms.ChatMessageTypeAI || len(assistant.Parts) != 1 {
		t.Fatalf("expected assistant tool call message, got %#v", assistant)
	}
	toolCall, ok := assistant.Parts[0].(llms.ToolCall)
	if !ok {
		t.Fatalf("expected assistant part to be tool call, got %T", assistant.Parts[0])
	}
	if toolCall.ID != "toolu_abc123" || toolCall.FunctionCall == nil || toolCall.FunctionCall.Name != toolregistry.CommandToolName {
		t.Fatalf("unexpected tool call: %#v", toolCall)
	}
	if strings.Contains(toolCall.FunctionCall.Arguments, "work_item_id") {
		t.Fatalf("tool call arguments should not expose work item id: %s", toolCall.FunctionCall.Arguments)
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
	var envelope toolResultEnvelope
	if err := json.Unmarshal([]byte(toolResult.Content), &envelope); err != nil {
		t.Fatalf("tool result should be a JSON envelope: %v\n%s", err, toolResult.Content)
	}
	if !envelope.IsError {
		t.Fatalf("unexpected tool result envelope metadata: %#v", envelope)
	}
	if strings.Contains(toolResult.Content, "tool_use_id") || strings.Contains(toolResult.Content, "toolu_abc123") {
		t.Fatalf("tool result content should not duplicate tool call id: %s", toolResult.Content)
	}
	if !strings.Contains(envelope.Content, "exit_code: 1") ||
		!strings.Contains(envelope.Content, "output:\nstaging target not found") ||
		!strings.Contains(envelope.Content, "error:\nexit status 1") {
		t.Fatalf("tool result envelope content should include failure details: %q", envelope.Content)
	}
}

func TestBuildNextActionMessagesReplaysMultipleToolResults(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-1", ProjectID: "project-1", Body: "make title red"},
		},
		ObservationHistory: []agent.ExecutionFeedback{
			{
				Cycle:           1,
				WorkItemID:      "wi-1",
				ToolCallID:      "call_skill",
				Tool:            domain.ToolTypeSkill,
				RequestedAction: "load skill example-skill",
				Status:          string(domain.ExecutionStatusSucceeded),
				Input:           json.RawMessage(`{"skill_id":"example-skill"}`),
				Observation:     "loaded",
				Metadata: map[string]string{
					"tool_call_ids": "call_skill,call_grep",
					"result_code":   "0",
				},
			},
			{
				Cycle:           1,
				WorkItemID:      "wi-1",
				ToolCallID:      "call_grep",
				Tool:            domain.ToolTypeGrep,
				RequestedAction: "grep example-app",
				Status:          string(domain.ExecutionStatusSucceeded),
				Input:           json.RawMessage(`{"pattern":"example-app","path":"/Users/luka/.opencto","glob":"*","type":"","output_mode":"files_with_matches","-A":0,"-B":0,"-C":0,"context":0,"-i":false,"-n":true,"multiline":false,"head_limit":20,"offset":0}`),
				Observation:     "matched",
				Metadata: map[string]string{
					"tool_call_ids": "call_skill,call_grep",
					"result_code":   "0",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}

	assistant := messages[2]
	if assistant.Role != llms.ChatMessageTypeAI || len(assistant.Parts) != 2 {
		t.Fatalf("expected assistant message with two tool calls, got %#v", assistant)
	}
	firstCall := assistant.Parts[0].(llms.ToolCall)
	secondCall := assistant.Parts[1].(llms.ToolCall)
	if firstCall.ID != "call_skill" || firstCall.FunctionCall == nil || firstCall.FunctionCall.Name != skilltool.SkillToolName {
		t.Fatalf("unexpected first tool call: %#v", firstCall)
	}
	if secondCall.ID != "call_grep" || secondCall.FunctionCall == nil || secondCall.FunctionCall.Name != greptool.GrepToolName {
		t.Fatalf("unexpected second tool call: %#v", secondCall)
	}
	firstResult := messages[3].Parts[0].(llms.ToolCallResponse)
	secondResult := messages[4].Parts[0].(llms.ToolCallResponse)
	if firstResult.ToolCallID != "call_skill" || !strings.Contains(firstResult.Content, "loaded") {
		t.Fatalf("unexpected first tool result: %#v", firstResult)
	}
	if secondResult.ToolCallID != "call_grep" || !strings.Contains(secondResult.Content, "matched") {
		t.Fatalf("unexpected second tool result: %#v", secondResult)
	}
}

func TestValidateToolTranscriptMessagesRejectsOrphanToolResult(t *testing.T) {
	t.Parallel()

	err := validateToolTranscriptMessages([]llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, "system"),
		{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{llms.ToolCallResponse{
				ToolCallID: "call_missing",
				Name:       toolregistry.CommandToolName,
				Content:    "ok",
			}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "no preceding assistant tool call") {
		t.Fatalf("expected orphan tool result error, got %v", err)
	}
}

func TestValidateToolTranscriptMessagesRejectsInterruptedToolResults(t *testing.T) {
	t.Parallel()

	err := validateToolTranscriptMessages([]llms.MessageContent{
		{
			Role: llms.ChatMessageTypeAI,
			Parts: []llms.ContentPart{
				llms.ToolCall{
					ID:   "call_one",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      toolregistry.CommandToolName,
						Arguments: `{"command":"pwd"}`,
					},
				},
				llms.ToolCall{
					ID:   "call_two",
					Type: "function",
					FunctionCall: &llms.FunctionCall{
						Name:      toolregistry.CommandToolName,
						Arguments: `{"command":"ls"}`,
					},
				},
			},
		},
		{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{llms.ToolCallResponse{
				ToolCallID: "call_one",
				Name:       toolregistry.CommandToolName,
				Content:    "ok",
			}},
		},
		llms.TextParts(llms.ChatMessageTypeHuman, "continue"),
	})
	if err == nil || !strings.Contains(err.Error(), "assistant tool calls missing results") || !strings.Contains(err.Error(), "call_two") {
		t.Fatalf("expected interrupted tool result error, got %v", err)
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
			Tool:            domain.ToolTypeExec,
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
	toolResult, ok := messages[3].Parts[0].(llms.ToolCallResponse)
	if !ok {
		t.Fatalf("expected tool result part, got %T", messages[3].Parts[0])
	}
	var envelope toolResultEnvelope
	if err := json.Unmarshal([]byte(toolResult.Content), &envelope); err != nil {
		t.Fatalf("tool result should be a JSON envelope: %v\n%s", err, toolResult.Content)
	}
	if envelope.IsError {
		t.Fatalf("unexpected successful tool result envelope: %#v", envelope)
	}
	if strings.Contains(toolResult.Content, "tool_use_id") || strings.Contains(toolResult.Content, "toolu_abc123") {
		t.Fatalf("tool result content should not duplicate tool call id: %s", toolResult.Content)
	}
	if !strings.Contains(envelope.Content, "exit_code: 0") || !strings.Contains(envelope.Content, "output:\ncreated") {
		t.Fatalf("tool result envelope content should include success details: %q", envelope.Content)
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
	if !strings.Contains(systemPrompt, "Run `LoadSkill` by exact ID before applying an advertised skill") {
		t.Fatalf("system prompt should include concise skill loading rule:\n%s", systemPrompt)
	}
	if strings.Contains(systemPrompt, "go-testing") || strings.Contains(systemPrompt, "Use when adding or fixing Go tests.") {
		t.Fatalf("system prompt should not include skill catalog entries:\n%s", systemPrompt)
	}
	reminder := messageText(messages[1])
	if !strings.Contains(reminder, "<system-reminder>") ||
		!strings.Contains(reminder, "- `go-testing`: Use when adding or fixing Go tests.") {
		t.Fatalf("unexpected skill reminder:\n%s", reminder)
	}
	if got := messageText(messages[2]); got != "add test coverage" {
		t.Fatalf("unexpected user message: %q", got)
	}
}

func TestBuildNextActionMessagesIncludesMemoryCrudPolicy(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "remember that I prefer SQLite",
			},
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %d", len(messages))
	}
	systemPrompt := messageText(messages[0])
	for _, expected := range []string{
		"Run `MemorySearch` before proposing changes",
		"`MemoryProposeUpdate` — when you know the `memory_id`",
		"`MemoryProposeAdd` — new durable facts",
		"`MemoryProposeForget` — only when the user asks to forget",
		"`MemoryList` — read-only inspection",
		"Store reusable preferences even without an explicit \"remember\"",
		"Scope — narrowest first",
		"project for repo/stack/architecture",
		"user for cross-project identity",
		"global only for rules that span users",
		"Do not save: task-scoped choices",
		"Pin only high-value long-term memory",
		"\"use pnpm\"",
		"\"use raw SQL for this migration\"",
		"\"never deploy without asking me first\"",
	} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("system prompt should include memory CRUD policy %q:\n%s", expected, systemPrompt)
		}
	}
	if got := messageText(messages[1]); got != "remember that I prefer SQLite" {
		t.Fatalf("unexpected user message: %q", got)
	}
}

func TestBuildNextActionMessagesIncludesCollaborationGuidance(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-1", ProjectID: "project-1", Body: "add authentication"},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	prompt := messageText(messages[0])
	for _, expected := range []string{
		"For non-trivial work: use read-only tools first",
		"ask one concise question",
		"Otherwise proceed, then verify",
		"Use dedicated, specialized tools",
		"Use `WorkflowCreate` or `WorkflowUpdate` with a self-contained prompt",
		"Include logical step boundaries and state handoff requirements",
		"# Reporting",
		"Use `opencto report` only when a command, script, scheduled workflow, monitor, or background process must send a separate channel notification",
		"opencto report \"Message.\" -channel_type <discord|telegram|cli> -channel_id \"<id>\"",
		"`workflows/`: scheduled workflow source repositories",
		"`workflow-runs/`: per-run workflow snapshots",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("prompt missing collaboration text %q:\n%s", expected, prompt)
		}
	}
}

func TestBuildNextActionMessagesDoesNotExposeRoutingMetadata(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "Do it now!",
				Metadata: domain.Metadata{
					domain.MetadataKeyReplyToMessageID: "message-1",
					domain.MetadataKeyReplyToChannelID: "channel-1",
					domain.MetadataKeyReplyToContextID: "context-1",
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
	got := messageText(messages[1])
	if got != "Do it now!" {
		t.Fatalf("expected only user-authored text, got:\n%s", got)
	}
	for _, garbage := range []string{
		"Runtime message metadata",
		"reply_to_message_id",
		"reply_to_channel_id",
		"reply_to_context_id",
	} {
		if strings.Contains(got, garbage) {
			t.Fatalf("user message leaked routing metadata %q:\n%s", garbage, got)
		}
	}
}

func TestBuildNextActionMessagesAddsMemoryToSystemPrompt(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "what database should we use?",
			},
			Memory: []domain.Memory{{
				ID:      "memory-1",
				Scope:   domain.MemoryScopeProject,
				Kind:    "decision",
				Content: "Use SQLite for local storage.",
			}},
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected system and user messages, got %d", len(messages))
	}
	memory := messageText(messages[0])
	guardIndex := strings.Index(memory, "Treat memory as context")
	contextIndex := strings.Index(memory, "Remembered context data")
	if guardIndex == -1 || contextIndex == -1 || contextIndex < guardIndex {
		t.Fatalf("memory context should be rendered after guardrails:\n%s", memory)
	}
	if !strings.Contains(memory, "User-derived and untrusted") ||
		!strings.Contains(memory, `id="memory-1"`) ||
		!strings.Contains(memory, `content="Use SQLite for local storage."`) {
		t.Fatalf("unexpected memory context in system prompt:\n%s", memory)
	}
	if got := messageText(messages[1]); got != "what database should we use?" {
		t.Fatalf("unexpected user message: %q", got)
	}
}

func TestBuildNextActionMessagesAddsOnboardingInstructionsWhenActive(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-1", ProjectID: "project-1", Body: domain.OnboardingSlashCommand},
		},
		Onboarding: agent.OnboardingContext{Active: true, Source: "command", Status: string(domain.OnboardingStatusPrompted)},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	systemPrompt := messageText(messages[0])
	for _, expected := range []string{
		"# Onboarding",
		"Onboarding mode is active (source: command) with status `prompted`.",
		"Onboarding is optional. Don't force it.",
		"When durable onboarding info is present",
		"store it before responding",
		"Tag every onboarding memory with `onboarding`",
		"`MemoryProposeAdd` for new entries",
		"`MemorySearch` + `MemoryProposeUpdate` when updating existing memory",
		"agent-owned email",
		"create one with AgentMail",
		"AgentMail API key status: missing",
		"tell the user to set `AGENTMAIL_API_KEY` first",
		"do not load or run `agentmail`",
		"Store only non-secret agent email details",
		"`agent-email`",
		"Never store API keys, passwords, OTPs, cookies, or recovery secrets",
		"If the user skips, answers nothing, or asks to continue",
		"`automatic` source: answer the current request first",
		"then introduce optional onboarding",
		"Do not lead with project or technical questions",
		"Their name and role",
		"Agent email: existing address, create with AgentMail, or skip",
		"`/onboard` source: ask a short optional question set in your own words",
		"On follow-up, don't repeat",
	} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("system prompt missing onboarding text %q:\n%s", expected, systemPrompt)
		}
	}
	if strings.Contains(systemPrompt, "check `AGENTMAIL_API_KEY`") {
		t.Fatalf("system prompt should use runtime AgentMail key status instead of telling the model to check env:\n%s", systemPrompt)
	}
}

func TestBuildNextActionMessagesUsesAvailableAgentMailKeyStatus(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-1", ProjectID: "project-1", Body: domain.OnboardingSlashCommand},
		},
		Onboarding: agent.OnboardingContext{
			Active:                   true,
			Source:                   "command",
			Status:                   string(domain.OnboardingStatusPrompted),
			AgentMailAPIKeyAvailable: true,
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	systemPrompt := messageText(messages[0])
	for _, expected := range []string{
		"AgentMail API key status: available",
		"load skill `agentmail` and create the inbox",
	} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("system prompt missing available AgentMail text %q:\n%s", expected, systemPrompt)
		}
	}
	for _, blocked := range []string{
		"do not load or run `agentmail`",
		"check `AGENTMAIL_API_KEY`",
	} {
		if strings.Contains(systemPrompt, blocked) {
			t.Fatalf("system prompt included unavailable AgentMail text %q:\n%s", blocked, systemPrompt)
		}
	}
}

func TestBuildNextActionMessagesOmitsConversationContextDuringOnboarding(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-current", ProjectID: "project-1", Body: domain.OnboardingSlashCommand},
			ConversationSummaries: []domain.ConversationSummary{{
				ID:      "summary-1",
				Scope:   domain.ConversationSummaryScopeProject,
				Summary: "Previous task was changing DNS/NS records for ge.domains.",
			}},
			Conversation: []domain.ConversationMessage{
				{ID: "previous-user", Role: domain.ConversationRoleUser, Body: "Proceed with logging in and changing NS."},
				{ID: "previous-assistant", Role: domain.ConversationRoleAssistant, Body: "Do you want me to make DNS changes?"},
			},
			Skills: []skills.Summary{{
				ID:          "go-testing",
				Name:        "Go Testing",
				Description: "Use when adding or fixing Go tests.",
				Path:        "/repo/skills/go-testing/SKILL.md",
			}},
			ConversationMaxContextChars: 4000,
		},
		Onboarding: agent.OnboardingContext{Active: true, Source: "command", Status: string(domain.OnboardingStatusPrompted)},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("expected only system and onboarding command messages, got %d", len(messages))
	}
	combined := messageText(messages[0]) + "\n" + messageText(messages[1])
	for _, leaked := range []string{"Conversation summary", "Recent conversation history", "DNS/NS", "ge.domains", "logging in and changing NS", "<system-reminder>", "go-testing", "Use when adding or fixing Go tests."} {
		if strings.Contains(combined, leaked) {
			t.Fatalf("onboarding messages leaked prior context %q:\n%s", leaked, combined)
		}
	}
	if got := messageText(messages[1]); got != domain.OnboardingSlashCommand {
		t.Fatalf("unexpected onboarding user message: %q", got)
	}
}

func TestBuildNextActionMessagesOmitsContextDuringNonCommandOnboarding(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"automatic", "answer"} {
		source := source
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			messages, err := buildNextActionMessages(agent.NextActionInput{
				ProjectID: "project-1",
				Context: agent.Context{
					Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
					Event:   domain.Event{ID: "event-current", ProjectID: "project-1", Body: "deploy the app"},
					ConversationSummaries: []domain.ConversationSummary{{
						ID:      "summary-1",
						Scope:   domain.ConversationSummaryScopeProject,
						Summary: "Previous task was updating deployment settings.",
					}},
					Conversation: []domain.ConversationMessage{
						{ID: "previous-user", Role: domain.ConversationRoleUser, Body: "Use the staging cluster first."},
						{ID: "previous-assistant", Role: domain.ConversationRoleAssistant, Body: "I will verify staging before production."},
					},
					Memory: []domain.Memory{
						{ID: "thread-memory", Scope: domain.MemoryScopeThread, Kind: "fact", Content: "The active task is deployment prep."},
						{ID: "user-preference", Scope: domain.MemoryScopeUser, Kind: "preference", Content: "User wants concise release notes."},
					},
					Skills: []skills.Summary{{
						ID:          "go-testing",
						Name:        "Go Testing",
						Description: "Use when adding or fixing Go tests.",
						Path:        "/repo/skills/go-testing/SKILL.md",
					}},
					ConversationMaxContextChars: 4000,
				},
				Onboarding: agent.OnboardingContext{Active: true, Source: source, Status: string(domain.OnboardingStatusPrompted)},
			})
			if err != nil {
				t.Fatalf("build next action messages: %v", err)
			}
			if len(messages) != 2 {
				t.Fatalf("expected only system and current user messages, got %d", len(messages))
			}
			combined := messageText(messages[0])
			for _, message := range messages[1:] {
				combined += "\n" + messageText(message)
			}
			for _, leaked := range []string{
				"thread-memory",
				"The active task is deployment prep.",
				"user-preference",
				"User wants concise release notes.",
				"<system-reminder>",
				"go-testing",
				"Conversation summary",
				"Previous task was updating deployment settings.",
				"Recent conversation history",
				"Use the staging cluster first.",
			} {
				if strings.Contains(combined, leaked) {
					t.Fatalf("%s onboarding leaked context %q:\n%s", source, leaked, combined)
				}
			}
			if got := messageText(messages[len(messages)-1]); got != "deploy the app" {
				t.Fatalf("unexpected user message: %q", got)
			}
		})
	}
}

func TestBuildNextActionMessagesFiltersMemoryContextDuringOnboarding(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-current", ProjectID: "project-1", Body: domain.OnboardingSlashCommand},
			Memory: []domain.Memory{
				{ID: "project-memory", Scope: domain.MemoryScopeProject, Kind: "decision", Content: "Project uses SQLite."},
				{ID: "user-identity", Scope: domain.MemoryScopeUser, Kind: "identity", Content: "User is Luka."},
				{ID: "user-preference", Scope: domain.MemoryScopeUser, Kind: "preference", Content: "User prefers concise replies."},
				{ID: "global-memory", Scope: domain.MemoryScopeGlobal, Kind: "instruction", Content: "Global deployment rule."},
				{ID: "thread-memory", Scope: domain.MemoryScopeThread, Kind: "fact", Content: "Thread task was DNS changes."},
			},
		},
		Onboarding: agent.OnboardingContext{Active: true, Source: "command", Status: string(domain.OnboardingStatusPrompted)},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	systemPrompt := messageText(messages[0])
	for _, expected := range []string{"project-memory", "Project uses SQLite.", "user-identity", "User is Luka."} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("onboarding system prompt missing memory %q:\n%s", expected, systemPrompt)
		}
	}
	for _, blocked := range []string{"user-preference", "concise replies", "global-memory", "Global deployment rule", "thread-memory", "DNS changes"} {
		if strings.Contains(systemPrompt, blocked) {
			t.Fatalf("onboarding system prompt included blocked memory %q:\n%s", blocked, systemPrompt)
		}
	}
}

func TestBuildNextActionMessagesHidesOnboardingInstructionsWhenInactive(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event:   domain.Event{ID: "event-1", ProjectID: "project-1", Body: "deploy"},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if strings.Contains(messageText(messages[0]), "# Onboarding") {
		t.Fatalf("system prompt should not contain onboarding instructions:\n%s", messageText(messages[0]))
	}
}

func TestBuildNextActionMessagesAddsBoundedConversationHistory(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-current",
				ProjectID: "project-1",
				Body:      "continue",
			},
			Conversation: []domain.ConversationMessage{
				{ID: "old", Role: domain.ConversationRoleUser, Body: "older message that should be trimmed first"},
				{ID: "assistant", Role: domain.ConversationRoleAssistant, Body: "use Open-Meteo"},
				{ID: "tool", Role: domain.ConversationRoleTool, Body: strings.Repeat("weather-json ", 5), Metadata: domain.Metadata{"tool": "exec", "status": "succeeded"}},
			},
			ConversationMaxContextChars: 240,
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 3 {
		t.Fatalf("expected system, conversation history, and user messages, got %d", len(messages))
	}
	history := messageText(messages[1])
	if !strings.Contains(history, "Recent conversation history") ||
		!strings.Contains(history, "assistant: use Open-Meteo") ||
		!strings.Contains(history, "tool[exec succeeded]") {
		t.Fatalf("unexpected conversation history:\n%s", history)
	}
	if strings.Contains(history, "older message that should be trimmed first") {
		t.Fatalf("expected oldest history to be dropped under char cap:\n%s", history)
	}
	if len(history) > input.Context.ConversationMaxContextChars {
		t.Fatalf("history exceeded cap: %d > %d\n%s", len(history), input.Context.ConversationMaxContextChars, history)
	}
	if got := messageText(messages[2]); got != "continue" {
		t.Fatalf("unexpected user message: %q", got)
	}
}

func TestBuildNextActionMessagesAddsConversationSummaries(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-current",
				ProjectID: "project-1",
				Body:      "continue",
			},
			ConversationSummaries: []domain.ConversationSummary{{
				ID:      "summary-1",
				Scope:   domain.ConversationSummaryScopeProject,
				Summary: "Earlier discussion selected SQLite and memory tools.",
			}},
			Conversation: []domain.ConversationMessage{{
				ID:   "recent",
				Role: domain.ConversationRoleAssistant,
				Body: "I can implement the next step.",
			}},
			ConversationMaxContextChars: 4000,
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected system, summary, history, and user messages, got %d", len(messages))
	}
	summary := messageText(messages[1])
	if !strings.Contains(summary, "Conversation summary") || !strings.Contains(summary, "summary[project]") {
		t.Fatalf("unexpected summary context:\n%s", summary)
	}
	history := messageText(messages[2])
	if !strings.Contains(history, "Recent conversation history") || !strings.Contains(history, "assistant: I can implement") {
		t.Fatalf("unexpected history context:\n%s", history)
	}
}

func TestConversationContextBudgetsUseSummaryHeavySplitWhenSummariesExist(t *testing.T) {
	t.Parallel()

	summaryBudget, historyBudget := conversationContextBudgets(20000, true)
	if summaryBudget != 14000 || historyBudget != 6000 {
		t.Fatalf("expected 70%% summary and 30%% raw history budgets, got summary=%d history=%d", summaryBudget, historyBudget)
	}
}

func TestBuildNextActionMessagesPrioritizesThreadSummaryBeforeChannelSummary(t *testing.T) {
	t.Parallel()

	raw := make([]domain.ConversationMessage, 0, 20)
	for i := 0; i < 20; i++ {
		raw = append(raw, domain.ConversationMessage{
			ID:   "recent-" + strconv.Itoa(i),
			Role: domain.ConversationRoleUser,
			Body: "recent-" + strconv.Itoa(i) + " " + strings.Repeat("raw-thread-detail ", 80),
		})
	}
	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-current",
				ProjectID: "project-1",
				Body:      "continue",
			},
			ConversationSummaries: []domain.ConversationSummary{
				{
					ID:      "thread-summary",
					Scope:   domain.ConversationSummaryScopeThread,
					Summary: "THREAD-SUMMARY " + strings.Repeat("thread-summary-detail ", 350) + "THREAD-END",
				},
				{
					ID:      "channel-summary",
					Scope:   domain.ConversationSummaryScopeChannel,
					Summary: "CHANNEL-SUMMARY " + strings.Repeat("channel-summary-detail ", 700) + "CHANNEL-END",
				},
			},
			Conversation:                raw,
			ConversationMaxContextChars: 20000,
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected system, summary, history, and user messages, got %d", len(messages))
	}
	summary := messageText(messages[1])
	if !strings.Contains(summary, "summary[thread]") || !strings.Contains(summary, "THREAD-SUMMARY") {
		t.Fatalf("expected thread summary in bounded summary context:\n%s", summary)
	}
	if !strings.Contains(summary, "THREAD-END") {
		t.Fatalf("expected full thread summary to be kept before truncating channel summary:\n%s", summary)
	}
	if !strings.Contains(summary, "summary[channel]") || !strings.Contains(summary, "CHANNEL-SUMMARY") {
		t.Fatalf("expected remaining budget to include the channel summary:\n%s", summary)
	}
	if strings.Contains(summary, "CHANNEL-END") {
		t.Fatalf("expected channel summary to be truncated before thread summary:\n%s", summary)
	}
	history := messageText(messages[2])
	if len(history) > 6000 {
		t.Fatalf("expected raw thread history to stay within 30%% budget, got %d chars", len(history))
	}
	if !strings.Contains(history, "recent-19") {
		t.Fatalf("expected most recent raw thread history to be retained:\n%s", history)
	}
}

func TestConversationSummaryContextPrioritizesNewestSummaryWithinScope(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	summary := conversationSummaryContextMessage([]domain.ConversationSummary{
		{
			ID:          "old-thread-summary",
			Scope:       domain.ConversationSummaryScopeThread,
			Summary:     "OLD-THREAD " + strings.Repeat("old thread detail ", 80),
			ToCreatedAt: base,
		},
		{
			ID:          "new-thread-summary",
			Scope:       domain.ConversationSummaryScopeThread,
			Summary:     "NEWEST-THREAD-SUMMARY must survive.",
			ToCreatedAt: base.Add(time.Minute),
		},
		{
			ID:          "channel-summary",
			Scope:       domain.ConversationSummaryScopeChannel,
			Summary:     "CHANNEL-SUMMARY can use remaining space.",
			ToCreatedAt: base.Add(2 * time.Minute),
		},
	}, 180)

	if !strings.Contains(summary, "summary[thread]: NEWE") {
		t.Fatalf("expected newest thread summary to be prioritized:\n%s", summary)
	}
	if strings.Contains(summary, "OLD-THREAD") {
		t.Fatalf("expected older thread summary to be trimmed before newest one:\n%s", summary)
	}
}

func TestConversationSummaryContextRendersSelectedSummariesChronologically(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	summary := conversationSummaryContextMessage([]domain.ConversationSummary{
		{
			ID:          "thread-summary",
			Scope:       domain.ConversationSummaryScopeThread,
			Summary:     "THREAD-NEW selected first by scope priority.",
			ToCreatedAt: base.Add(2 * time.Minute),
			ToMessageID: "thread-new",
		},
		{
			ID:          "channel-summary",
			Scope:       domain.ConversationSummaryScopeChannel,
			Summary:     "CHANNEL-OLD should render first.",
			ToCreatedAt: base,
			ToMessageID: "channel-old",
		},
		{
			ID:          "project-summary",
			Scope:       domain.ConversationSummaryScopeProject,
			Summary:     "PROJECT-MIDDLE should render second.",
			ToCreatedAt: base.Add(time.Minute),
			ToMessageID: "project-middle",
		},
	}, 2000)

	channelIndex := strings.Index(summary, "CHANNEL-OLD")
	projectIndex := strings.Index(summary, "PROJECT-MIDDLE")
	threadIndex := strings.Index(summary, "THREAD-NEW")
	if channelIndex < 0 || projectIndex < 0 || threadIndex < 0 {
		t.Fatalf("expected all selected summaries to render:\n%s", summary)
	}
	if !(channelIndex < projectIndex && projectIndex < threadIndex) {
		t.Fatalf("expected selected summaries to render oldest-to-newest:\n%s", summary)
	}
}

func TestConversationHistoryKeepsThreadRootBeforeExtraChannelRawHistory(t *testing.T) {
	t.Parallel()

	messages := []domain.ConversationMessage{
		{ID: "channel-old", Role: domain.ConversationRoleUser, Body: "DROP-OLD-CHANNEL " + strings.Repeat("old channel detail ", 20)},
		{ID: "root", Role: domain.ConversationRoleUser, Body: "KEEP-THREAD-ROOT original parent request"},
	}
	for i := 0; i < 8; i++ {
		messages = append(messages, domain.ConversationMessage{
			ID:       "thread-" + strconv.Itoa(i),
			Role:     domain.ConversationRoleUser,
			ThreadID: "thread-1",
			Body:     "thread-" + strconv.Itoa(i) + " " + strings.Repeat("thread detail ", 20),
		})
	}

	history := conversationContextMessage(messages, 700)
	if !strings.Contains(history, "KEEP-THREAD-ROOT") {
		t.Fatalf("expected thread root to be kept before extra channel raw history:\n%s", history)
	}
	if !strings.Contains(history, "thread-7") {
		t.Fatalf("expected newest thread history to be kept:\n%s", history)
	}
	if strings.Contains(history, "DROP-OLD-CHANNEL") {
		t.Fatalf("expected older channel raw history to be trimmed first:\n%s", history)
	}
}

func TestBuildNextActionMessagesExcludesCurrentAndAdditionalEventsFromHistory(t *testing.T) {
	t.Parallel()

	input := agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-current",
				ProjectID: "project-1",
				Body:      "Do it now!",
			},
			AdditionalEvents: []domain.Event{{
				ID:        "event-additional",
				ProjectID: "project-1",
				Body:      "Use the existing plan.",
			}},
			Conversation: []domain.ConversationMessage{
				{ID: "prior", EventID: "event-prior", Role: domain.ConversationRoleUser, Body: "Earlier request"},
				{ID: "assistant-prompt", EventID: "event-current", Role: domain.ConversationRoleAssistant, Body: "Where should I create it?"},
				{ID: "current", EventID: "event-current", Role: domain.ConversationRoleUser, Body: "Do it now!"},
				{ID: "additional", EventID: "event-additional", Role: domain.ConversationRoleUser, Body: "Use the existing plan."},
			},
		},
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("expected system, history, current user, and additional user messages, got %d", len(messages))
	}
	history := messageText(messages[1])
	if !strings.Contains(history, "Earlier request") {
		t.Fatalf("expected prior conversation history, got:\n%s", history)
	}
	if !strings.Contains(history, "Where should I create it?") {
		t.Fatalf("assistant prompt tied to the task event should remain in history:\n%s", history)
	}
	if strings.Contains(history, "Do it now!") || strings.Contains(history, "Use the existing plan.") {
		t.Fatalf("current/additional events should not be duplicated in history:\n%s", history)
	}
	if got := messageText(messages[2]); got != "Do it now!" {
		t.Fatalf("unexpected current user message: %q", got)
	}
	if got := messageText(messages[3]); got != "Use the existing plan." {
		t.Fatalf("unexpected additional user message: %q", got)
	}
}

func TestConversationHistoryCompactsRepeatedEditToolResults(t *testing.T) {
	t.Parallel()

	const filePath = "/Users/luka/.opencto/react-vite-example/src/index.css"
	var conversation []domain.ConversationMessage
	for index, bytesWritten := range []string{"2167", "2167", "2167", "2168", "2169", "2170"} {
		conversation = append(conversation, domain.ConversationMessage{
			ID:      "edit-" + string(rune('0'+index)),
			EventID: "event-" + bytesWritten,
			Role:    domain.ConversationRoleTool,
			Body: "requested_action: edit " + filePath + "\n\nobservation:\nedited: " + filePath +
				"\nreplacements: 1\nbytes_written: " + bytesWritten,
			Metadata: domain.Metadata{
				"tool":        string(domain.ToolTypeEdit),
				"status":      string(domain.ExecutionStatusSucceeded),
				"result_code": "0",
			},
		})
	}

	history := conversationContextMessage(conversation, 8000)
	if strings.Count(history, "tool[Edit succeeded]") != 1 {
		t.Fatalf("expected one compacted edit entry, got:\n%s", history)
	}
	if !strings.Contains(history, "tool[Edit succeeded] x6") ||
		!strings.Contains(history, "edited: "+filePath) ||
		!strings.Contains(history, "replacements: 1") ||
		!strings.Contains(history, "bytes_written: 2167-2170") {
		t.Fatalf("unexpected compacted edit history:\n%s", history)
	}
	if strings.Contains(history, "requested_action:") || strings.Contains(history, "observation:") {
		t.Fatalf("compacted edit history should omit repeated raw sections:\n%s", history)
	}
}

func TestConversationHistoryDedupeAdjacentAssistantDuplicates(t *testing.T) {
	t.Parallel()

	history := conversationContextMessage([]domain.ConversationMessage{
		{ID: "assistant-1", Role: domain.ConversationRoleAssistant, Body: "Plan:\n- create app\n- run build"},
		{ID: "assistant-2", Role: domain.ConversationRoleAssistant, Body: "Plan:\n- create app\n- run build"},
		{ID: "assistant-3", Role: domain.ConversationRoleAssistant, Body: "Plan:\n- create app\n- run build"},
		{ID: "user-1", Role: domain.ConversationRoleUser, Body: "approved"},
	}, 8000)

	if got := strings.Count(history, "Plan:"); got != 1 {
		t.Fatalf("expected adjacent duplicate assistant history to collapse, got %d in:\n%s", got, history)
	}
	if !strings.Contains(history, "user: approved") {
		t.Fatalf("expected user message to remain, got:\n%s", history)
	}
}

func TestConversationHistoryCleansTerminalControls(t *testing.T) {
	t.Parallel()

	history := conversationContextMessage([]domain.ConversationMessage{{
		ID:      "tool-1",
		EventID: "event-1",
		Role:    domain.ConversationRoleTool,
		Body:    "requested_action: build\n\nobservation:\nstdout:\nvite build\x1b[2K\rtransforming...\x1b]0;ignored title\a done\x00",
		Metadata: domain.Metadata{
			"tool":        string(domain.ToolTypeExec),
			"status":      string(domain.ExecutionStatusSucceeded),
			"result_code": "0",
		},
	}}, 8000)

	if strings.Contains(history, "\x1b") ||
		strings.Contains(history, "\r") ||
		strings.Contains(history, "[2K") ||
		strings.Contains(history, "ignored title") ||
		strings.Contains(history, "\x00") {
		t.Fatalf("expected terminal controls to be removed, got:\n%s", history)
	}
	if !strings.Contains(history, "vite build\ntransforming... done") {
		t.Fatalf("expected readable output to remain, got:\n%s", history)
	}
}

func TestBuildNextActionMessagesIncludesEventAttachments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imagePath := dir + "/photo.png"
	audioPath := dir + "/voice.wav"
	if err := os.WriteFile(imagePath, validTestPNG(t), 0o600); err != nil {
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

func TestBuildNextActionMessagesReportsImageContentTypeMismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imagePath := dir + "/photo.bin"
	if err := os.WriteFile(imagePath, validTestPNG(t), 0o600); err != nil {
		t.Fatalf("write image attachment: %v", err)
	}

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "what is in this image?",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{{
						ID:          "attachment-1",
						Filename:    "photo.bin",
						ContentType: "image/jpeg",
						LocalPath:   imagePath,
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	text := messageText(messages[1])
	if !strings.Contains(text, "Attachment content type corrected for photo.bin: declared image/jpeg, detected image/png") {
		t.Fatalf("expected content type correction note, got:\n%s", text)
	}
	if countImageParts(messages[1]) != 1 {
		t.Fatalf("expected image part, got %#v", messages[1].Parts)
	}
}

func TestBuildNextActionMessagesSkipsUnsupportedImageAttachment(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imagePath := dir + "/diagram.svg"
	if err := os.WriteFile(imagePath, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o600); err != nil {
		t.Fatalf("write image attachment: %v", err)
	}

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "review this diagram",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{{
						ID:          "attachment-1",
						Filename:    "diagram.svg",
						ContentType: "image/svg+xml",
						LocalPath:   imagePath,
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	text := messageText(messages[1])
	if !strings.Contains(text, "Image attachment skipped: diagram.svg: unsupported or invalid image content") {
		t.Fatalf("expected unsupported image skip note, got:\n%s", text)
	}
	if countImageParts(messages[1]) != 0 {
		t.Fatalf("unsupported image should not be sent: %#v", messages[1].Parts)
	}
}

func TestBuildNextActionMessagesLimitsImageAttachments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	attachments := make([]domain.EventAttachment, 0, media.DefaultMaxImagesPerEvent+1)
	for index := 0; index < media.DefaultMaxImagesPerEvent+1; index++ {
		path := dir + "/photo-" + strconv.Itoa(index) + ".png"
		if err := os.WriteFile(path, validTestPNG(t), 0o600); err != nil {
			t.Fatalf("write image attachment: %v", err)
		}
		attachments = append(attachments, domain.EventAttachment{
			ID:          "attachment-" + strconv.Itoa(index),
			Filename:    "photo-" + strconv.Itoa(index) + ".png",
			ContentType: "image/png",
			LocalPath:   path,
		})
	}

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "compare these screenshots",
				Payload:   map[string]any{eventPayloadAttachmentsKey: attachments},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if got := countImageParts(messages[1]); got != media.DefaultMaxImagesPerEvent {
		t.Fatalf("expected %d image parts, got %d", media.DefaultMaxImagesPerEvent, got)
	}
	if !strings.Contains(messageText(messages[1]), "image limit reached") {
		t.Fatalf("expected image limit note, got:\n%s", messageText(messages[1]))
	}
}

func TestConversationHistoryDoesNotReplayImageAttachments(t *testing.T) {
	t.Parallel()

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-2",
				ProjectID: "project-1",
				Body:      "what about the previous image?",
			},
			Conversation: []domain.ConversationMessage{{
				ID:      "message-1",
				EventID: "event-1",
				Role:    domain.ConversationRoleUser,
				Body:    "Uploaded attachment(s): photo.png (image/png)",
			}},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	for _, message := range messages {
		if countImageParts(message) != 0 {
			t.Fatalf("conversation history should not replay image parts: %#v", message.Parts)
		}
	}
}

func TestBuildNextActionMessagesFetchesInlineImageURL(t *testing.T) {
	imageData := validTestPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	t.Cleanup(server.Close)
	resolver := media.NewImageResolver(media.ImageResolverConfig{
		MaxBytes:            1024,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})

	messages, err := buildNextActionMessagesWithContext(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      server.URL + "/avatar.png what's this?",
			},
		},
	}, resolver)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if countImageParts(messages[1]) != 1 {
		t.Fatalf("expected inline URL image part, got %#v", messages[1].Parts)
	}
	if !strings.Contains(messageText(messages[1]), "Inline image URL candidate: "+server.URL+"/avatar.png") {
		t.Fatalf("expected inline URL note, got:\n%s", messageText(messages[1]))
	}
}

func TestBuildNextActionMessagesLimitsInlineURLFetchAttempts(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	}))
	t.Cleanup(server.Close)
	resolver := media.NewImageResolver(media.ImageResolverConfig{
		MaxBytes:            1024,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})

	var body strings.Builder
	for index := 0; index < media.DefaultMaxImageURLFetchesPerEvent+1; index++ {
		if index > 0 {
			body.WriteByte(' ')
		}
		body.WriteString(server.URL)
		body.WriteString("/bad-")
		body.WriteString(strconv.Itoa(index))
		body.WriteString(".png")
	}

	messages, err := buildNextActionMessagesWithContext(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      body.String(),
			},
		},
	}, resolver)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if requests != media.DefaultMaxImageURLFetchesPerEvent {
		t.Fatalf("expected %d fetches, got %d", media.DefaultMaxImageURLFetchesPerEvent, requests)
	}
	if !strings.Contains(messageText(messages[1]), "image URL fetch limit reached") {
		t.Fatalf("expected fetch limit note, got:\n%s", messageText(messages[1]))
	}
}

func TestBuildNextActionMessagesLimitsTotalImageBytes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imageData := append(validTestPNG(t), bytes.Repeat([]byte{0}, int(media.DefaultMaxTotalImageBytes/2)+1)...)
	var attachments []domain.EventAttachment
	for index := 0; index < 2; index++ {
		path := dir + "/large-" + strconv.Itoa(index) + ".png"
		if err := os.WriteFile(path, imageData, 0o600); err != nil {
			t.Fatalf("write image attachment: %v", err)
		}
		attachments = append(attachments, domain.EventAttachment{
			ID:          "attachment-" + strconv.Itoa(index),
			Filename:    "large-" + strconv.Itoa(index) + ".png",
			ContentType: "image/png",
			LocalPath:   path,
		})
	}

	messages, err := buildNextActionMessages(agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "compare these large images",
				Payload:   map[string]any{eventPayloadAttachmentsKey: attachments},
			},
		},
	})
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if got := countImageParts(messages[1]); got != 1 {
		t.Fatalf("expected one image part due to total byte limit, got %d", got)
	}
	if !strings.Contains(messageText(messages[1]), "total image byte limit reached") {
		t.Fatalf("expected total byte limit note, got:\n%s", messageText(messages[1]))
	}
}

func TestBuildNextActionMessagesDoesNotRefetchFailedAttachmentDownload(t *testing.T) {
	var requests int
	imageData := validTestPNG(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(imageData)
	}))
	t.Cleanup(server.Close)
	resolver := media.NewImageResolver(media.ImageResolverConfig{
		MaxBytes:            1024,
		HTTPClient:          server.Client(),
		AllowPrivateNetwork: true,
	})

	messages, err := buildNextActionMessagesWithContext(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
			Event: domain.Event{
				ID:        "event-1",
				ProjectID: "project-1",
				Body:      "Uploaded attachment(s): avatar.png (image/png)",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{{
						ID:          "attachment-1",
						Filename:    "avatar.png",
						ContentType: "image/png",
						URL:         server.URL + "/avatar.png",
						Metadata:    domain.Metadata{"download_error": "network failed"},
					}},
				},
			},
		},
	}, resolver)
	if err != nil {
		t.Fatalf("build next action messages: %v", err)
	}
	if requests != 0 {
		t.Fatalf("failed attachment download should not be refetched, got %d request(s)", requests)
	}
	if countImageParts(messages[1]) != 0 {
		t.Fatalf("failed attachment download should not produce image parts: %#v", messages[1].Parts)
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
						Name:      toolregistry.CommandToolName,
						Arguments: `{"command":"pwd","args":[],"timeout_ms":120000,"run_mode":"wait_for_exit","idempotency":"read_only","process_scope":"stop_on_finish","description":"inspect workspace","destructive":false}`,
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
	if output.Status != "tool" || len(output.ToolChoices) != 1 {
		t.Fatalf("expected tool output, got %#v", output)
	}
	choice := output.ToolChoices[0]
	if choice.ToolCallID != "toolu_next" || choice.Metadata["tool_call_id"] != "toolu_next" {
		t.Fatalf("tool call id was not preserved: %#v", choice)
	}
	if output.WorkItemID != "" {
		t.Fatalf("unexpected work item id: %q", output.WorkItemID)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyReadOnly || choice.ProcessScope != domain.ProcessScopeStopOnFinish {
		t.Fatalf("tool execution metadata was not preserved: %#v", choice)
	}
	if len(model.options.Tools) != len(toolregistry.Definitions()) {
		t.Fatalf("expected all tool schemas, got %#v", model.options.Tools)
	}
}

func TestNextActionSubAgentUsesRestrictedToolsAndPrompt(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{Content: "done"}},
		},
	}

	engine := &OpenAIEngine{reasoningModel: model}
	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		ProjectID: "project-1",
		Context: agent.Context{
			Project: domain.Project{ID: "project-1"},
			Event:   domain.Event{Body: "# Goal\nAudit\n\n# Task Context And Instructions\nCheck files."},
		},
		SubAgent:      &agent.SubAgentContext{Goal: "Audit", RunSummary: "Read README.md already."},
		RestrictTools: true,
		ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
	})
	if err != nil {
		t.Fatalf("NextAction: %v", err)
	}
	if output.Status != "completed" {
		t.Fatalf("expected completed output, got %#v", output)
	}
	if len(model.options.Tools) != 1 || model.options.Tools[0].Function.Name != "Read" {
		t.Fatalf("expected only Read tool, got %#v", model.options.Tools)
	}
	if len(model.messages) == 0 || !strings.Contains(messageText(model.messages[0]), "OpenCTO agent") || !strings.Contains(messageText(model.messages[0]), "Audit") {
		t.Fatalf("agent system prompt missing expected text: %#v", model.messages)
	}
	if !messagesContainText(model.messages, "Agent run summary") || !messagesContainText(model.messages, "Read README.md already.") {
		t.Fatalf("agent run summary missing from context: %#v", model.messages)
	}
}

func TestNextActionCombinesMultipleExecToolCalls(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				ToolCalls: []llms.ToolCall{
					{
						ID:   "toolu_pwd",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      toolregistry.CommandToolName,
							Arguments: `{"command":"pwd","args":[],"timeout_ms":10000,"run_mode":"wait_for_exit","idempotency":"read_only","process_scope":"stop_on_finish","description":"confirm workspace","destructive":false}`,
						},
					},
					{
						ID:   "toolu_uname",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      toolregistry.CommandToolName,
							Arguments: `{"command":"uname","args":["-a"],"timeout_ms":10000,"run_mode":"wait_for_exit","idempotency":"read_only","process_scope":"stop_on_finish","description":"capture platform","destructive":false}`,
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
	if len(output.ToolChoices) != 2 {
		t.Fatalf("expected two exec choices, got %#v", output.ToolChoices)
	}
	if output.ToolChoices[0].Command != "pwd" || output.ToolChoices[1].Command != "uname" {
		t.Fatalf("unexpected exec choices: %#v", output.ToolChoices)
	}
	for _, choice := range output.ToolChoices {
		if choice.WorkingDir != "/workspace" {
			t.Fatalf("expected exec choice working directory to use runtime workspace, got %#v", output.ToolChoices)
		}
		if choice.Metadata["tool_call_ids"] != "toolu_pwd,toolu_uname" {
			t.Fatalf("expected shared tool_call_ids metadata, got %#v", choice.Metadata)
		}
	}
	if output.WorkItemID != "" {
		t.Fatalf("unexpected work item id: %q", output.WorkItemID)
	}
}

func TestNextActionCombinesMultipleStructuredReadOnlyToolCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		toolName  string
		toolType  domain.ToolType
		firstArgs string
		nextArgs  string
	}{
		{
			name:      "read",
			toolName:  readtool.ReadToolName,
			toolType:  domain.ToolTypeRead,
			firstArgs: `{"file_path":"/workspace/a.go","offset":0,"limit":0,"pages":""}`,
			nextArgs:  `{"file_path":"/workspace/b.go","offset":0,"limit":0,"pages":""}`,
		},
		{
			name:      "glob",
			toolName:  globtool.GlobToolName,
			toolType:  domain.ToolTypeGlob,
			firstArgs: `{"pattern":"*.go","path":"/workspace"}`,
			nextArgs:  `{"pattern":"*.md","path":"/workspace"}`,
		},
		{
			name:      "grep",
			toolName:  greptool.GrepToolName,
			toolType:  domain.ToolTypeGrep,
			firstArgs: `{"pattern":"needle","path":"/workspace","glob":"","type":"","output_mode":"content","-A":0,"-B":0,"-C":0,"context":0,"-i":false,"-n":true,"multiline":false,"head_limit":250,"offset":0}`,
			nextArgs:  `{"pattern":"thread","path":"/workspace","glob":"","type":"","output_mode":"content","-A":0,"-B":0,"-C":0,"context":0,"-i":false,"-n":true,"multiline":false,"head_limit":250,"offset":0}`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			model := &recordingToolModel{
				response: &llms.ContentResponse{
					Choices: []*llms.ContentChoice{{
						ToolCalls: []llms.ToolCall{
							{
								ID:   "toolu_first",
								Type: "function",
								FunctionCall: &llms.FunctionCall{
									Name:      test.toolName,
									Arguments: test.firstArgs,
								},
							},
							{
								ID:   "toolu_next",
								Type: "function",
								FunctionCall: &llms.FunctionCall{
									Name:      test.toolName,
									Arguments: test.nextArgs,
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
					Event:   domain.Event{Body: "inspect workspace"},
				},
				Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
			})
			if err != nil {
				t.Fatalf("NextAction: %v", err)
			}
			if len(output.ToolChoices) != 2 {
				t.Fatalf("expected two %s choices, got %#v", test.toolType, output.ToolChoices)
			}
			if output.ToolChoices[0].Type != test.toolType || output.ToolChoices[1].Type != test.toolType {
				t.Fatalf("unexpected tool choice types: %#v", output.ToolChoices)
			}
			for _, choice := range output.ToolChoices {
				if choice.Metadata["tool_call_ids"] != "toolu_first,toolu_next" {
					t.Fatalf("expected shared tool_call_ids metadata, got %#v", choice.Metadata)
				}
			}
		})
	}
}

func TestNextActionCombinesMixedToolCalls(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				ToolCalls: []llms.ToolCall{
					{
						ID:   "toolu_server",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      toolregistry.CommandToolName,
							Arguments: `{"command":"npm","args":["run","dev"],"timeout_ms":1000,"run_mode":"start_background","idempotency":"non_idempotent","process_scope":"project","description":"start dev server","destructive":true}`,
						},
					},
					{
						ID:   "toolu_read",
						Type: "function",
						FunctionCall: &llms.FunctionCall{
							Name:      readtool.ReadToolName,
							Arguments: `{"file_path":"/workspace/package.json","offset":0,"limit":0,"pages":""}`,
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
			Event:   domain.Event{Body: "start the app and inspect package metadata"},
		},
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("NextAction: %v", err)
	}
	if len(output.ToolChoices) != 2 {
		t.Fatalf("expected two tool choices, got %#v", output.ToolChoices)
	}
	if !output.ToolChoices[0].Destructive || output.ToolChoices[0].Idempotency != domain.ToolIdempotencyNonIdempotent {
		t.Fatalf("expected first choice destructive/idempotency to be preserved, got %#v", output.ToolChoices[0])
	}
	if output.ToolChoices[0].RunMode != domain.ToolRunModeStartBackground || output.ToolChoices[1].Type != domain.ToolTypeRead {
		t.Fatalf("expected mixed child choices to be preserved, got %#v", output.ToolChoices)
	}
	for _, choice := range output.ToolChoices {
		if choice.Metadata["tool_call_ids"] != "toolu_server,toolu_read" {
			t.Fatalf("expected shared tool_call_ids metadata, got %#v", choice.Metadata)
		}
	}
}

func TestToolChoicePreservesCommandAndArgsForDirectExecution(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_direct",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"go","args":["test","./..."],"timeout_ms":120000,"run_mode":"wait_for_exit","idempotency":"idempotent","process_scope":"stop_on_finish","description":"run tests","destructive":false}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "linux", Exec: "/bin/bash", WorkspaceRoot: "/workspace"},
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
	if choice.Metadata["wrapped_exec_command"] == "true" {
		t.Fatalf("direct command should not be exec wrapped: %#v", choice.Metadata)
	}
	if choice.WorkingDir != "/workspace" {
		t.Fatalf("expected exec choice working directory to use runtime workspace, got %q", choice.WorkingDir)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyIdempotent || choice.ProcessScope != domain.ProcessScopeStopOnFinish {
		t.Fatalf("expected execution metadata to be preserved, got %#v", choice)
	}
}

func TestToolChoiceCapturesMemoryProposeUpdateInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_memory_update",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      memorytool.ProposeUpdateToolName,
			Arguments: `{"memory_id":"memory-123","content":"","kind":"","tags_mode":"keep","tags":[],"confidence_mode":"set","confidence":0,"pinned_mode":"set","pinned":false,"reason":"lower confidence and unpin"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeMemoryProposeUpdate {
		t.Fatalf("expected memory update type, got %q", choice.Type)
	}
	if choice.Intent != "propose memory update memory-123" {
		t.Fatalf("unexpected memory update summary: %q", choice.Intent)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyNonIdempotent || choice.ProcessScope != domain.ProcessScopeStopOnFinish {
		t.Fatalf("unexpected memory update execution metadata: %#v", choice)
	}
	if !strings.Contains(string(choice.Input), `"confidence":0`) || !strings.Contains(string(choice.Input), `"pinned":false`) {
		t.Fatalf("expected raw memory update input to preserve zero/false values, got %s", choice.Input)
	}
}

func TestToolChoiceCapturesMemoryListInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_MemoryList",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      memorytool.ListToolName,
			Arguments: `{"scope":"user","kind":"preference","tags":["communication"],"limit":10}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeMemoryList {
		t.Fatalf("expected memory list type, got %q", choice.Type)
	}
	if choice.Intent != "list user memory kind preference" {
		t.Fatalf("unexpected memory list summary: %q", choice.Intent)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyReadOnly || choice.ProcessScope != domain.ProcessScopeStopOnFinish {
		t.Fatalf("unexpected memory list execution metadata: %#v", choice)
	}
}

func TestToolChoiceUsesExecCwd(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_cwd",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"pnpm","args":["run","dev"],"cwd":"/workspace/example-app","timeout_ms":1000,"run_mode":"start_background","idempotency":"idempotent","process_scope":"project","description":"start app","destructive":false}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.WorkingDir != "/workspace/example-app" {
		t.Fatalf("expected exec cwd to become working directory, got %q", choice.WorkingDir)
	}
}

func TestToolChoiceRejectsModelSuppliedWorkItemID(t *testing.T) {
	t.Parallel()

	_, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_hidden",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"pwd","args":[],"timeout_ms":1000,"run_mode":"wait_for_exit","idempotency":"read_only","process_scope":"stop_on_finish","description":"inspect workspace","destructive":false,"work_item_id":"wi-1"}`,
		},
	}, agent.ToolSelectionInput{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected hidden work item id to be rejected, got %v", err)
	}
}

func TestToolChoiceUsesGlobCwd(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_glob_cwd",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      globtool.GlobToolName,
			Arguments: `{"cwd":"/workspace/example-app","path":"","pattern":"package.json"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.WorkingDir != "/workspace/example-app" {
		t.Fatalf("expected glob cwd to become working directory, got %q", choice.WorkingDir)
	}
	if !strings.Contains(string(choice.Input), `"cwd":"/workspace/example-app"`) {
		t.Fatalf("expected raw glob input to preserve cwd, got %s", choice.Input)
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

func TestToolChoiceCapturesAgentInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_agent",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      agenttool.AgentToolName,
			Arguments: `{"goal":"Audit branch","prompt":"Check git state and report.","allowed_tools":["Exec"],"max_turns":3}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeAgent {
		t.Fatalf("expected Agent tool type, got %q", choice.Type)
	}
	if choice.RunMode != domain.ToolRunModeWaitForExit || choice.Idempotency != domain.ToolIdempotencyNonIdempotent {
		t.Fatalf("unexpected Agent execution metadata: %#v", choice)
	}
	if choice.Metadata["model_tool"] != agenttool.AgentToolName {
		t.Fatalf("expected model tool metadata, got %#v", choice.Metadata)
	}
}

func TestToolChoiceRejectsDisallowedToolInRestrictedContext(t *testing.T) {
	t.Parallel()

	_, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_agent",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      agenttool.AgentToolName,
			Arguments: `{"goal":"Nested","prompt":"Try recursion."}`,
		},
	}, agent.ToolSelectionInput{
		Runtime:       agent.RuntimeContext{WorkspaceRoot: "/workspace"},
		RestrictTools: true,
		ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
	})
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected disallowed tool error, got %v", err)
	}
}

func TestToolChoiceCapturesWorkflowCreateInput(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_schedule",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      scheduletool.WorkflowCreateToolName,
			Arguments: `{"workflow_id":"daily-hello","prompt":"Create a workflow that runs every morning at 9 and sends hello.","commit_message":"Create daily hello workflow"}`,
		},
	}, agent.ToolSelectionInput{
		Context: agent.Context{Event: domain.Event{Body: "every morning send hello"}},
		Runtime: agent.RuntimeContext{WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("tool choice: %v", err)
	}
	if choice.Type != domain.ToolTypeWorkflowCreate {
		t.Fatalf("expected workflow create tool type, got %q", choice.Type)
	}
	if choice.Idempotency != domain.ToolIdempotencyNonIdempotent || choice.RunMode != domain.ToolRunModeWaitForExit {
		t.Fatalf("expected workflow execution metadata, got %#v", choice)
	}
	if choice.Metadata["model_tool"] != scheduletool.WorkflowCreateToolName {
		t.Fatalf("expected workflow metadata, got %#v", choice.Metadata)
	}
	if !strings.Contains(string(choice.Input), `"prompt":"Create a workflow`) {
		t.Fatalf("expected raw workflow input to be preserved, got %s", choice.Input)
	}
}

func TestToolChoiceSplitsPlainCommandStringWithoutExec(t *testing.T) {
	t.Parallel()

	choice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_plain",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"go test ./...","args":[],"description":"run tests"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "darwin", Exec: "/bin/zsh", WorkspaceRoot: "/workspace"},
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
}

func TestToolChoiceWrapsExecOnlyForCommandSyntax(t *testing.T) {
	t.Parallel()

	linuxChoice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_exec",
		Type: "function",
		FunctionCall: &llms.FunctionCall{
			Name:      toolregistry.CommandToolName,
			Arguments: `{"command":"printf hi | wc -c","args":[],"description":"count bytes"}`,
		},
	}, agent.ToolSelectionInput{
		Runtime: agent.RuntimeContext{OS: "linux", Exec: "/bin/bash", WorkspaceRoot: "/workspace"},
	})
	if err != nil {
		t.Fatalf("linux tool choice: %v", err)
	}
	if linuxChoice.Command != "/bin/bash" || strings.Join(linuxChoice.Args, "\x00") != "-c\x00printf hi | wc -c" {
		t.Fatalf("expected OS exec for exec syntax, got %q %#v", linuxChoice.Command, linuxChoice.Args)
	}
	if linuxChoice.WorkingDir != "/workspace" {
		t.Fatalf("expected exec syntax choice working directory to use runtime workspace, got %q", linuxChoice.WorkingDir)
	}

	windowsChoice, err := toolChoiceFromToolCall(llms.ToolCall{
		ID:   "toolu_windows_exec",
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
		t.Fatalf("expected Windows exec for exec syntax, got %q %#v", windowsChoice.Command, windowsChoice.Args)
	}
	if windowsChoice.WorkingDir != `C:\workspace` {
		t.Fatalf("expected Windows exec choice working directory to use runtime workspace, got %q", windowsChoice.WorkingDir)
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
				Content: "I heard: run tests.",
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
	if strings.Contains(systemText, "run tests") {
		t.Fatalf("system prompt should not contain task transcript: %s", systemText)
	}
	userText := messageText(model.messages[1])
	if !strings.Contains(userText, "Transcribed voice message for internal context (voice-message.ogg): run tests") {
		t.Fatalf("user message missing transcript: %s", userText)
	}
}

func TestNextActionIncludesImageAndAudioTranscript(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	imagePath := dir + "/screenshot.png"
	audioPath := dir + "/voice-message.ogg"
	if err := os.WriteFile(imagePath, validTestPNG(t), 0o600); err != nil {
		t.Fatalf("write image attachment: %v", err)
	}
	if err := os.WriteFile(audioPath, []byte("ogg data"), 0o600); err != nil {
		t.Fatalf("write audio attachment: %v", err)
	}
	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: "I can see the screenshot and heard the audio.",
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
				Body:      "Uploaded attachment(s): screenshot.png (image/png), voice-message.ogg (audio/ogg)",
				Payload: map[string]any{
					eventPayloadAttachmentsKey: []domain.EventAttachment{
						{
							ID:          "attachment-1",
							ProjectID:   "project-1",
							EventID:     "event-1",
							Filename:    "screenshot.png",
							ContentType: "image/png",
							SizeBytes:   8,
							LocalPath:   imagePath,
						},
						{
							ID:          "attachment-2",
							ProjectID:   "project-1",
							EventID:     "event-1",
							Filename:    "voice-message.ogg",
							ContentType: "audio/ogg",
							SizeBytes:   8,
							LocalPath:   audioPath,
						},
					},
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
	if countImageParts(model.messages[1]) != 1 {
		t.Fatalf("expected image part, got %#v", model.messages[1].Parts)
	}
	userText := messageText(model.messages[1])
	if !strings.Contains(userText, "Transcribed voice message for internal context (voice-message.ogg): run tests") ||
		!strings.Contains(userText, "Attachment available locally: screenshot.png (image/png") {
		t.Fatalf("user message missing image/audio context:\n%s", userText)
	}
}

func TestNextActionReturnsTerminalAnswer(t *testing.T) {
	t.Parallel()

	engine := &OpenAIEngine{
		reasoningModel: &recordingToolModel{
			response: &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{
					Content: "Report created.",
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
	if output.Status != "completed" || output.NextAction.ResponseMessage != "Report created." {
		t.Fatalf("unexpected final output: %#v", output)
	}
}

func TestNextActionReturnsTerminalAttachments(t *testing.T) {
	t.Parallel()

	engine := &OpenAIEngine{
		reasoningModel: &recordingToolModel{
			response: &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{
					Content: `{"response_message":"Screenshot captured.","response_attachments":[{"path":"/workspace/screenshot.png","filename":"screenshot.png","content_type":"image/png"}]}`,
				}},
			},
		},
	}

	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		Context: agent.Context{Event: domain.Event{Body: "capture screenshot"}},
	})
	if err != nil {
		t.Fatalf("NextAction final: %v", err)
	}
	if output.Status != "completed" || output.NextAction.ResponseMessage != "Screenshot captured." {
		t.Fatalf("unexpected final output: %#v", output)
	}
	if len(output.NextAction.ResponseAttachments) != 1 || output.NextAction.ResponseAttachments[0].Path != "/workspace/screenshot.png" {
		t.Fatalf("unexpected attachments: %#v", output.NextAction.ResponseAttachments)
	}
}

func TestNextActionLeavesRegularJSONTerminalAnswerAsText(t *testing.T) {
	t.Parallel()

	engine := &OpenAIEngine{
		reasoningModel: &recordingToolModel{
			response: &llms.ContentResponse{
				Choices: []*llms.ContentChoice{{
					Content: `{"ok":true}`,
				}},
			},
		},
	}

	output, err := engine.NextAction(context.Background(), agent.NextActionInput{
		Context: agent.Context{Event: domain.Event{Body: "return json"}},
	})
	if err != nil {
		t.Fatalf("NextAction final: %v", err)
	}
	if output.NextAction.ResponseMessage != `{"ok":true}` || len(output.NextAction.ResponseAttachments) != 0 {
		t.Fatalf("unexpected final output: %#v", output.NextAction)
	}
}
