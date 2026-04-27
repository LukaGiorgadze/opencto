package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type nextActionPromptData struct {
	ProjectName        string
	ProjectID          string
	ProjectState       string
	ProjectDescription string
	UserRequest        string
	OS                 string
	Arch               string
	Shell              string
	Path               string
	ProjectRoot        string
	ExecutionCycle     int
	ForceFinal         bool
}

type nextActionTerminalOutput struct {
	Status      string `json:"status,omitempty"`
	FinalAnswer string `json:"final_answer,omitempty"`
}

func (e *OpenAIEngine) NextAction(ctx context.Context, input agent.NextActionInput) (agent.NextActionOutput, error) {
	if e.reasoningModel == nil {
		return agent.NextActionOutput{}, fmt.Errorf("next action model is not configured")
	}

	input, err := e.enrichInputWithAttachmentTranscripts(ctx, input)
	if err != nil {
		return agent.NextActionOutput{}, err
	}

	messages, err := buildNextActionMessages(input)
	if err != nil {
		return agent.NextActionOutput{}, err
	}

	response, err := e.reasoningModel.GenerateContent(
		ctx,
		messages,
		llms.WithTools(toolregistry.SelectorLLMDefinitions()),
	)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("model returned no choices")
	}

	choice := response.Choices[0]
	if len(choice.ToolCalls) > 1 {
		return agent.NextActionOutput{}, fmt.Errorf("%w: next action requires exactly one tool call or one final answer", agent.ErrInvalidNextAction)
	}
	if len(choice.ToolCalls) == 1 {
		return nextActionToolOutput(choice, input)
	}
	return nextActionTerminalFromContent(choice.Content, input)
}

func buildNextActionMessages(input agent.NextActionInput) ([]llms.MessageContent, error) {
	prompt, err := renderNextActionPrompt(input)
	if err != nil {
		return nil, err
	}

	userMessage, err := openAIUserMessageFromEvent(input.Context.Event)
	if err != nil {
		return nil, err
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, prompt),
		userMessage,
	}
	for _, feedback := range input.ObservationHistory {
		assistantMessage, toolResultMessage, err := nextActionTranscriptMessages(feedback)
		if err != nil {
			return nil, err
		}
		messages = append(messages, assistantMessage, toolResultMessage)
	}
	return messages, nil
}

func renderNextActionPrompt(input agent.NextActionInput) (string, error) {
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := nextActionPromptData{
		ProjectName:        projectName,
		ProjectID:          input.ProjectID,
		ProjectState:       formatProjectState(input.Context.ActiveWorkItems),
		ProjectDescription: strings.TrimSpace(input.Context.Project.Description),
		UserRequest:        strings.TrimSpace(input.Context.Event.Body),
		OS:                 input.Runtime.OS,
		Arch:               input.Runtime.Arch,
		Shell:              firstNonEmpty(strings.TrimSpace(input.Runtime.Shell), "unknown"),
		Path:               strings.TrimSpace(input.Runtime.Path),
		ProjectRoot:        firstNonEmpty(strings.TrimSpace(input.Runtime.WorkspaceRoot), "."),
		ExecutionCycle:     input.ExecutionCycle,
		ForceFinal:         input.ForceFinal,
	}

	return prompts.Render("next_action.tmpl", data)
}

func nextActionTranscriptMessages(feedback agent.ExecutionFeedback) (llms.MessageContent, llms.MessageContent, error) {
	toolCallID := executionFeedbackToolCallID(feedback)
	if toolCallID == "" {
		return llms.MessageContent{}, llms.MessageContent{}, fmt.Errorf("%w: execution feedback is missing tool_call_id", agent.ErrInvalidNextAction)
	}

	assistantText := strings.TrimSpace(feedback.Metadata["assistant_text"])
	if assistantText == "" {
		assistantText = strings.TrimSpace(feedback.RequestedAction)
	}
	if assistantText == "" {
		assistantText = "I will run the next command step."
	}

	args := shellToolInput{
		Command:     transcriptCommand(feedback),
		Args:        feedback.Args,
		WorkingDir:  strings.TrimSpace(feedback.Metadata["working_directory"]),
		TimeoutMs:   transcriptTimeoutMs(feedback),
		Description: strings.TrimSpace(feedback.RequestedAction),
		Destructive: feedback.Metadata["destructive"] == "true",
		WorkItemID:  strings.TrimSpace(feedback.WorkItemID),
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return llms.MessageContent{}, llms.MessageContent{}, err
	}

	assistant := llms.MessageContent{
		Role: llms.ChatMessageTypeAI,
		Parts: []llms.ContentPart{
			llms.TextContent{Text: assistantText},
			llms.ToolCall{
				ID:   toolCallID,
				Type: "function",
				FunctionCall: &llms.FunctionCall{
					Name:      toolregistry.SelectorToolCommandName,
					Arguments: string(encoded),
				},
			},
		},
	}

	result := llms.MessageContent{
		Role: llms.ChatMessageTypeTool,
		Parts: []llms.ContentPart{llms.ToolCallResponse{
			ToolCallID: toolCallID,
			Name:       toolregistry.SelectorToolCommandName,
			Content:    formatToolResultContent(feedback),
		}},
	}
	return assistant, result, nil
}

func nextActionToolOutput(choice *llms.ContentChoice, input agent.NextActionInput) (agent.NextActionOutput, error) {
	selectionInput := agent.ToolSelectionInput{
		ProjectID:      input.ProjectID,
		Context:        input.Context,
		Runtime:        input.Runtime,
		ExecutionCycle: input.ExecutionCycle,
	}
	toolChoice, err := toolChoiceFromToolCall(choice.ToolCalls[0], selectionInput)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	workItemID := strings.TrimSpace(toolChoice.Metadata["work_item_id"])
	if toolChoice.Metadata == nil {
		toolChoice.Metadata = map[string]string{}
	}
	toolChoice.Metadata["tool_call_id"] = toolChoice.ToolCallID
	if workItemID != "" {
		toolChoice.Metadata["work_item_id"] = workItemID
	}
	assistantText := strings.TrimSpace(choice.Content)
	if assistantText == "" {
		assistantText = strings.TrimSpace(toolChoice.Intent)
	}
	return agent.NextActionOutput{
		Decision:      input.Decision,
		ToolChoice:    &toolChoice,
		WorkItemID:    workItemID,
		Status:        "tool",
		AssistantText: assistantText,
	}, nil
}

func nextActionTerminalFromContent(content string, input agent.NextActionInput) (agent.NextActionOutput, error) {
	var parsed nextActionTerminalOutput
	if err := decodeIntoJSONOutput(content, &parsed); err != nil {
		return agent.NextActionOutput{}, err
	}

	status, finalStatus, err := normalizeNextActionTerminalStatus(parsed)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	answer := strings.TrimSpace(parsed.FinalAnswer)
	if answer == "" {
		return agent.NextActionOutput{}, fmt.Errorf("%w: final answer is empty", agent.ErrInvalidNextAction)
	}
	_ = finalStatus
	return agent.NextActionOutput{
		Decision:    input.Decision,
		FinalAnswer: answer,
		Status:      status,
	}, nil
}

func decodeIntoJSONOutput(raw string, output any) error {
	decoder := json.NewDecoder(strings.NewReader(extractJSON(raw)))
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return fmt.Errorf("model returned multiple JSON values")
	case io.EOF:
		return nil
	default:
		return err
	}
}

func normalizeNextActionTerminalStatus(output nextActionTerminalOutput) (string, domain.WorkItemStatus, error) {
	status := strings.ToLower(strings.TrimSpace(output.Status))
	if status == "" {
		return "", "", fmt.Errorf("%w: final status is empty", agent.ErrInvalidNextAction)
	}

	switch status {
	case "completed", "complete", "final", "succeeded", "success", "finished", "finish":
		return "completed", domain.WorkItemStatusCompleted, nil
	case "blocked", "block":
		return "blocked", domain.WorkItemStatusBlocked, nil
	case "failed", "failure", "fail":
		return "failed", domain.WorkItemStatusFailed, nil
	case "ignored", "ignore", "skip", "skipped":
		return "ignored", domain.WorkItemStatusCompleted, nil
	default:
		return "", "", fmt.Errorf("%w: unsupported final status %q", agent.ErrInvalidNextAction, status)
	}
}

func executionFeedbackToolCallID(feedback agent.ExecutionFeedback) string {
	if strings.TrimSpace(feedback.ToolCallID) != "" {
		return strings.TrimSpace(feedback.ToolCallID)
	}
	return strings.TrimSpace(feedback.Metadata["tool_call_id"])
}

func transcriptCommand(feedback agent.ExecutionFeedback) string {
	if command := strings.TrimSpace(feedback.Metadata["original_command"]); command != "" {
		return command
	}
	return strings.TrimSpace(feedback.Command)
}

func transcriptTimeoutMs(feedback agent.ExecutionFeedback) int {
	value := strings.TrimSpace(feedback.Metadata["timeout_ms"])
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func formatToolResultContent(feedback agent.ExecutionFeedback) string {
	lines := make([]string, 0, 3)
	if code := strings.TrimSpace(feedback.Metadata["result_code"]); code != "" {
		lines = append(lines, "exit_code: "+code)
	}
	if observation := strings.TrimSpace(feedback.Observation); observation != "" {
		lines = append(lines, "output:\n"+observation)
	}
	if errMsg := strings.TrimSpace(feedback.Error); errMsg != "" {
		lines = append(lines, "error:\n"+errMsg)
	}
	return strings.Join(lines, "\n")
}
