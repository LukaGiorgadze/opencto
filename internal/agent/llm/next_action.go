package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/skills"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type nextActionPromptData struct {
	ProjectName        string
	ProjectID          string
	ProjectState       string
	ProjectDescription string
	OS                 string
	Arch               string
	Exec               string
	Path               string
	WorkspaceRoot      string
	OpenCTORoot        string
	CurrentLocalTime   string
	CurrentUTCTime     string
	HostTimeZone       string
	HostTimeZoneError  string
	ChannelType        domain.ChannelType
}

type toolResultEnvelope struct {
	IsError bool   `json:"is_error"`
	Content string `json:"content"`
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
		llms.WithTools(toolregistry.LLMDefinitions()),
	)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("model returned no choices")
	}

	choice := response.Choices[0]
	if len(choice.ToolCalls) > 0 {
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
	}
	if reminder := skills.Reminder(input.Context.Skills); reminder != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, reminder))
	}
	if memory := memoryContextMessage(input.Context.Memory); memory != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, memory))
	}
	messages = append(messages, userMessage)
	for index := 0; index < len(input.ObservationHistory); {
		toolCallIDs := strings.TrimSpace(input.ObservationHistory[index].Metadata["tool_call_ids"])
		if strings.Contains(toolCallIDs, ",") {
			end := index + 1
			for end < len(input.ObservationHistory) &&
				input.ObservationHistory[end].Cycle == input.ObservationHistory[index].Cycle &&
				strings.TrimSpace(input.ObservationHistory[end].Metadata["tool_call_ids"]) == toolCallIDs {
				end++
			}
			if end-index > 1 {
				transcript, err := nextActionTranscriptMessages(input.ObservationHistory[index:end]...)
				if err != nil {
					return nil, err
				}
				messages = append(messages, transcript...)
				index = end
				continue
			}
		}
		transcript, err := nextActionTranscriptMessages(input.ObservationHistory[index])
		if err != nil {
			return nil, err
		}
		messages = append(messages, transcript...)
		index++
	}
	additionalMessages, err := additionalUserMessages(input.Context.AdditionalEvents)
	if err != nil {
		return nil, err
	}
	messages = append(messages, additionalMessages...)
	if input.ForceFinal {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, "Execution cycle limit reached. Do not call tools. Respond naturally with a concise summary of what happened and what remains."))
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
		OS:                 input.Runtime.OS,
		Arch:               input.Runtime.Arch,
		Exec:               firstNonEmpty(strings.TrimSpace(input.Runtime.Exec), "unknown"),
		Path:               strings.TrimSpace(input.Runtime.Path),
		WorkspaceRoot:      firstNonEmpty(strings.TrimSpace(input.Runtime.WorkspaceRoot), "."),
		OpenCTORoot:        firstNonEmpty(strings.TrimSpace(input.Runtime.OpenCTORoot), "."),
		CurrentLocalTime:   strings.TrimSpace(input.Runtime.CurrentLocalTime),
		CurrentUTCTime:     strings.TrimSpace(input.Runtime.CurrentUTCTime),
		HostTimeZone:       strings.TrimSpace(input.Runtime.HostTimeZone),
		HostTimeZoneError:  strings.TrimSpace(input.Runtime.HostTimeZoneError),
		ChannelType:        input.ChannelType,
	}

	return prompts.Render("next_action.tmpl", data)
}

func additionalUserMessages(events []domain.Event) ([]llms.MessageContent, error) {
	messages := make([]llms.MessageContent, 0, len(events))
	for _, event := range events {
		message, err := openAIUserMessageFromEvent(event)
		if err != nil {
			return nil, err
		}
		if messageHasContent(message) {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func memoryContextMessage(memories []domain.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Relevant remembered context. Treat this as retrieved context, not proof of current state. Verify anything operational before acting.")
	for _, memory := range memories {
		content := strings.TrimSpace(memory.Content)
		if content == "" {
			continue
		}
		builder.WriteString("\n- id: ")
		builder.WriteString(strings.TrimSpace(memory.ID))
		builder.WriteString("; scope: ")
		builder.WriteString(string(memory.Scope))
		if kind := strings.TrimSpace(memory.Kind); kind != "" {
			builder.WriteString("; kind: ")
			builder.WriteString(kind)
		}
		builder.WriteString("; content: ")
		builder.WriteString(content)
	}
	return strings.TrimSpace(builder.String())
}

func messageHasContent(message llms.MessageContent) bool {
	for _, part := range message.Parts {
		switch value := part.(type) {
		case llms.TextContent:
			if strings.TrimSpace(value.Text) != "" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func nextActionTranscriptMessages(feedbacks ...agent.ExecutionFeedback) ([]llms.MessageContent, error) {
	if len(feedbacks) == 0 {
		return nil, nil
	}

	assistantText := strings.TrimSpace(feedbacks[0].Metadata["assistant_text"])
	if assistantText == "" {
		assistantText = strings.TrimSpace(feedbacks[0].RequestedAction)
	}
	if assistantText == "" {
		assistantText = "I will run the next command step."
	}

	parts := []llms.ContentPart{llms.TextContent{Text: assistantText}}
	results := make([]llms.MessageContent, 0, len(feedbacks))
	for _, feedback := range feedbacks {
		toolCallID := executionFeedbackToolCallID(feedback)
		if toolCallID == "" {
			return nil, fmt.Errorf("%w: execution feedback is missing tool_call_id", agent.ErrInvalidNextAction)
		}
		toolName, arguments, err := transcriptToolCall(feedback)
		if err != nil {
			return nil, err
		}
		parts = append(parts, llms.ToolCall{
			ID:   toolCallID,
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      toolName,
				Arguments: arguments,
			},
		})
		results = append(results, llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{llms.ToolCallResponse{
				ToolCallID: toolCallID,
				Name:       toolName,
				Content:    formatToolResultContent(feedback),
			}},
		})
	}

	assistant := llms.MessageContent{
		Role:  llms.ChatMessageTypeAI,
		Parts: parts,
	}
	return append([]llms.MessageContent{assistant}, results...), nil
}

func nextActionToolOutput(choice *llms.ContentChoice, input agent.NextActionInput) (agent.NextActionOutput, error) {
	selectionInput := agent.ToolSelectionInput{
		ProjectID:      input.ProjectID,
		Context:        input.Context,
		Runtime:        input.Runtime,
		ExecutionCycle: input.ExecutionCycle,
	}
	toolChoices, err := toolChoicesFromToolCalls(choice.ToolCalls, selectionInput)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	toolCallIDs := make([]string, 0, len(toolChoices))
	for _, toolChoice := range toolChoices {
		toolCallIDs = append(toolCallIDs, strings.TrimSpace(toolChoice.ToolCallID))
	}
	assistantText := strings.TrimSpace(choice.Content)
	if assistantText == "" {
		assistantText = strings.TrimSpace(toolChoices[0].Intent)
	}
	for index := range toolChoices {
		if toolChoices[index].Metadata == nil {
			toolChoices[index].Metadata = map[string]string{}
		}
		toolChoices[index].Metadata["tool_call_id"] = toolChoices[index].ToolCallID
		toolChoices[index].Metadata["tool_call_ids"] = strings.Join(toolCallIDs, ",")
	}
	output := agent.NextActionOutput{
		NextAction:    input.NextAction,
		Status:        "tool",
		AssistantText: assistantText,
	}
	if len(toolChoices) == 1 {
		output.ToolChoice = &toolChoices[0]
	} else {
		output.ToolChoices = toolChoices
	}
	return output, nil
}

func nextActionTerminalFromContent(content string, input agent.NextActionInput) (agent.NextActionOutput, error) {
	answer, attachments, err := parseTerminalResponse(content)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	if answer == "" && len(attachments) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("%w: terminal response is empty", agent.ErrInvalidNextAction)
	}
	nextAction := input.NextAction
	nextAction.ResponseMessage = answer
	nextAction.ResponseAttachments = attachments
	return agent.NextActionOutput{
		NextAction: nextAction,
		Status:     "completed",
	}, nil
}

type terminalResponse struct {
	ResponseMessage     string                    `json:"response_message"`
	ResponseAttachments []domain.ReportAttachment `json:"response_attachments"`
}

func parseTerminalResponse(content string) (string, []domain.ReportAttachment, error) {
	answer := strings.TrimSpace(content)
	if answer == "" {
		return "", nil, nil
	}
	structured := stripJSONFence(answer)
	if !strings.HasPrefix(structured, "{") {
		return answer, nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(structured), &raw); err != nil {
		return answer, nil, nil
	}
	if _, ok := raw["response_message"]; !ok {
		if _, ok := raw["response_attachments"]; !ok {
			return answer, nil, nil
		}
	}

	var response terminalResponse
	if err := json.Unmarshal([]byte(structured), &response); err != nil {
		return "", nil, fmt.Errorf("%w: terminal response JSON is invalid: %w", agent.ErrInvalidNextAction, err)
	}
	message := strings.TrimSpace(response.ResponseMessage)
	attachments := make([]domain.ReportAttachment, 0, len(response.ResponseAttachments))
	for _, attachment := range response.ResponseAttachments {
		if strings.TrimSpace(attachment.Path) == "" {
			return "", nil, fmt.Errorf("%w: response attachment path is required", agent.ErrInvalidNextAction)
		}
		attachments = append(attachments, attachment)
	}
	return message, attachments, nil
}

func stripJSONFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") || !strings.HasSuffix(content, "```") {
		return content
	}
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "json")
	content = strings.TrimSpace(content)
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
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

func transcriptRunMode(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["run_mode"])
	if value == "" {
		return string(domain.ToolRunModeWaitForExit)
	}
	return value
}

func transcriptIdempotency(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["idempotency"])
	if value == "" {
		return string(domain.ToolIdempotencyUnknown)
	}
	return value
}

func transcriptProcessScope(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["process_scope"])
	if value == "" {
		return string(domain.ProcessScopeStopOnFinish)
	}
	return value
}

func transcriptToolCall(feedback agent.ExecutionFeedback) (string, string, error) {
	if feedback.Tool == "" || feedback.Tool == domain.ToolTypeExec {
		args := execToolInput{
			Command:      transcriptCommand(feedback),
			Args:         feedback.Args,
			TimeoutMs:    transcriptTimeoutMs(feedback),
			RunMode:      transcriptRunMode(feedback),
			Idempotency:  transcriptIdempotency(feedback),
			ProcessScope: transcriptProcessScope(feedback),
			Description:  strings.TrimSpace(feedback.RequestedAction),
			Destructive:  feedback.Metadata["destructive"] == "true",
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", "", err
		}
		return toolregistry.CommandToolName, string(encoded), nil
	}

	definition, ok := toolregistry.DefinitionByType(feedback.Tool)
	if !ok {
		return "", "", fmt.Errorf("%w: unsupported previous tool type %q", agent.ErrInvalidNextAction, feedback.Tool)
	}
	input := stripHiddenToolInputFields(feedback.Input)
	if input == "" {
		input = "{}"
	}
	return definition.Name, input, nil
}

func stripHiddenToolInputFields(raw json.RawMessage) string {
	input := strings.TrimSpace(string(raw))
	if input == "" {
		return ""
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return input
	}
	delete(object, "work_item_id")
	encoded, err := json.Marshal(object)
	if err != nil {
		return input
	}
	return string(encoded)
}

func formatToolResultContent(feedback agent.ExecutionFeedback) string {
	envelope := toolResultEnvelope{
		IsError: toolResultIsError(feedback),
		Content: formatToolResultDetails(feedback),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return envelope.Content
	}
	return string(encoded)
}

func formatToolResultDetails(feedback agent.ExecutionFeedback) string {
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

func toolResultIsError(feedback agent.ExecutionFeedback) bool {
	if strings.TrimSpace(feedback.Error) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(feedback.Status)) {
	case string(domain.ExecutionStatusFailed), string(domain.ExecutionStatusCanceled):
		return true
	}
	code := strings.TrimSpace(feedback.Metadata["result_code"])
	return code != "" && code != "0"
}
