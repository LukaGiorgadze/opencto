package llm

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
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
	planningtool "github.com/opencto/opencto/internal/tools/planning"
)

type nextActionPromptData struct {
	ProjectName        string
	ProjectID          string
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
	if history := conversationContextMessage(input.Context.Conversation, input.Context.ConversationMaxContextChars); history != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, history))
	}
	messages = append(messages, userMessage)
	if pending := pendingPlanningContextMessage(input.NextAction, input.Context.AdditionalEvents); pending != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, pending))
	}
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

func conversationContextMessage(messages []domain.ConversationMessage, maxChars int) string {
	if len(messages) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 8000
	}
	header := "Recent conversation history. Use this as bounded context only; the current user request follows after this message."
	if maxChars <= len(header) {
		return truncateText(header, maxChars)
	}
	remaining := maxChars - len(header) - 1
	selected := make([]string, 0, len(messages))
	for i := len(messages) - 1; i >= 0 && remaining > 0; i-- {
		entry := conversationHistoryEntry(messages[i], remaining)
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if len(entry) > remaining {
			entry = truncateText(entry, remaining)
		}
		selected = append(selected, entry)
		remaining -= len(entry) + 1
	}
	if len(selected) == 0 {
		return header
	}
	var builder strings.Builder
	builder.WriteString(header)
	for i := len(selected) - 1; i >= 0; i-- {
		builder.WriteString("\n")
		builder.WriteString(selected[i])
	}
	return strings.TrimSpace(builder.String())
}

func pendingPlanningContextMessage(nextAction agent.NextAction, additionalEvents []domain.Event) string {
	token := strings.TrimSpace(nextAction.WaitingToken)
	kind := strings.TrimSpace(nextAction.WaitingKind)
	if token == "" || kind == "" {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("Current pending planning state. This is runtime context for the active task, not a new user request.")
	builder.WriteString("\n- kind: ")
	builder.WriteString(kind)
	builder.WriteString("\n- token: ")
	builder.WriteString(token)
	if kind == "plan" {
		builder.WriteString("\n- approval rule: mutate only after an answer explicitly approves this token. A bare approval phrase only counts when this task was resumed by matching token or reply metadata.")
		if hasRoutedPlanningAnswer(additionalEvents, token) {
			builder.WriteString("\n- current answer: this task was resumed from a matching planning token or reply. Use the user's latest answer below to decide whether to execute, revise, or ask a follow-up. Do not call ProposePlan again unless the user requested plan changes.")
		}
	}
	if response := truncateText(nextAction.ResponseMessage, 6000); response != "" {
		builder.WriteString("\n\nPrompt previously shown to the user:\n")
		builder.WriteString(response)
	}
	return strings.TrimSpace(builder.String())
}

func hasRoutedPlanningAnswer(events []domain.Event, token string) bool {
	token = normalizeLLMPlanningToken(token)
	if token == "" {
		return false
	}
	for _, event := range events {
		if explicitLLMPlanApprovalBody(event.Body, token) {
			return true
		}
		if normalizeLLMPlanningToken(event.Metadata[domain.MetadataKeyPlanningToken]) == token {
			return true
		}
	}
	return false
}

func explicitLLMPlanApprovalBody(body string, token string) bool {
	fields := strings.Fields(strings.TrimSpace(body))
	return len(fields) >= 2 && strings.EqualFold(fields[0], "approve") && normalizeLLMPlanningToken(fields[1]) == token
}

func normalizeLLMPlanningToken(token string) string {
	token = strings.Trim(strings.TrimSpace(token), "`.,:;()[]{}")
	return strings.ToUpper(token)
}

func conversationHistoryEntry(message domain.ConversationMessage, budget int) string {
	body := strings.TrimSpace(message.Body)
	if body == "" {
		return ""
	}
	label := conversationHistoryLabel(message)
	bodyBudget := budget - len(label) - 2
	if bodyBudget <= 0 {
		return ""
	}
	if message.Role == domain.ConversationRoleTool && bodyBudget > 1200 {
		bodyBudget = 1200
	}
	body = truncateText(body, bodyBudget)
	return "- " + label + ": " + body
}

func conversationHistoryLabel(message domain.ConversationMessage) string {
	switch message.Role {
	case domain.ConversationRoleAssistant:
		return "assistant"
	case domain.ConversationRoleTool:
		tool := strings.TrimSpace(message.Metadata["tool"])
		status := strings.TrimSpace(message.Metadata["status"])
		var parts []string
		if tool != "" {
			parts = append(parts, tool)
		}
		if status != "" {
			parts = append(parts, status)
		}
		if len(parts) == 0 {
			return "tool"
		}
		return "tool[" + strings.Join(parts, " ") + "]"
	default:
		return "user"
	}
}

func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	const suffix = " [truncated]"
	if limit <= len(suffix) {
		runes := []rune(text)
		if len(runes) <= limit {
			return text
		}
		return string(runes[:limit])
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	prefixLimit := limit - len(suffix)
	if prefixLimit > len(runes) {
		prefixLimit = len(runes)
	}
	return strings.TrimSpace(string(runes[:prefixLimit])) + suffix
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
	if output, handled, err := nextActionWaitingOutput(choice.ToolCalls, input); handled || err != nil {
		return output, err
	}
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

func nextActionWaitingOutput(calls []llms.ToolCall, input agent.NextActionInput) (agent.NextActionOutput, bool, error) {
	if len(calls) == 0 {
		return agent.NextActionOutput{}, false, nil
	}
	var planningCalls []llms.ToolCall
	for _, call := range calls {
		if call.FunctionCall == nil {
			continue
		}
		if isPlanningToolName(call.FunctionCall.Name) {
			planningCalls = append(planningCalls, call)
		}
	}
	if len(planningCalls) == 0 {
		return agent.NextActionOutput{}, false, nil
	}
	if len(planningCalls) != len(calls) || len(planningCalls) != 1 {
		return agent.NextActionOutput{}, true, fmt.Errorf("%w: planning pseudo-tools must be called alone", agent.ErrInvalidToolChoice)
	}

	call := planningCalls[0]
	switch call.FunctionCall.Name {
	case planningtool.AskUserQuestionToolName:
		return askUserQuestionWaitingOutput(call, input)
	case planningtool.ProposePlanToolName:
		return proposePlanWaitingOutput(call, input)
	default:
		return agent.NextActionOutput{}, true, fmt.Errorf("%w: unsupported planning pseudo-tool %q", agent.ErrInvalidToolChoice, call.FunctionCall.Name)
	}
}

func isPlanningToolName(name string) bool {
	switch name {
	case planningtool.AskUserQuestionToolName, planningtool.ProposePlanToolName:
		return true
	default:
		return false
	}
}

func askUserQuestionWaitingOutput(call llms.ToolCall, input agent.NextActionInput) (agent.NextActionOutput, bool, error) {
	definition, ok := toolregistry.DefinitionByName(planningtool.AskUserQuestionToolName)
	if !ok {
		return agent.NextActionOutput{}, true, fmt.Errorf("planning question tool is not registered")
	}
	var request planningtool.AskUserQuestionRequest
	raw := json.RawMessage(strings.TrimSpace(call.FunctionCall.Arguments))
	if err := decodeToolArguments(definition.Name, raw, &request); err != nil {
		return agent.NextActionOutput{}, true, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
	}
	if err := validateAskUserQuestion(request); err != nil {
		return agent.NextActionOutput{}, true, err
	}
	token := waitingToken("Q", input, call.ID)
	message := formatAskUserQuestion(token, request)
	nextAction := input.NextAction
	nextAction.ResponseMessage = message
	nextAction.WaitingToken = token
	nextAction.WaitingKind = "question"
	return agent.NextActionOutput{
		NextAction:   nextAction,
		WaitingToken: token,
		WaitingKind:  "question",
		Status:       "waiting",
	}, true, nil
}

func proposePlanWaitingOutput(call llms.ToolCall, input agent.NextActionInput) (agent.NextActionOutput, bool, error) {
	definition, ok := toolregistry.DefinitionByName(planningtool.ProposePlanToolName)
	if !ok {
		return agent.NextActionOutput{}, true, fmt.Errorf("propose plan tool is not registered")
	}
	var request planningtool.ProposePlanRequest
	raw := json.RawMessage(strings.TrimSpace(call.FunctionCall.Arguments))
	if err := decodeToolArguments(definition.Name, raw, &request); err != nil {
		return agent.NextActionOutput{}, true, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
	}
	if err := validateProposePlan(request); err != nil {
		return agent.NextActionOutput{}, true, err
	}
	token := waitingToken("P", input, call.ID)
	message := formatProposedPlan(token, request)
	nextAction := input.NextAction
	nextAction.ResponseMessage = message
	nextAction.WaitingToken = token
	nextAction.WaitingKind = "plan"
	return agent.NextActionOutput{
		NextAction:   nextAction,
		WaitingToken: token,
		WaitingKind:  "plan",
		Status:       "waiting",
	}, true, nil
}

func validateAskUserQuestion(request planningtool.AskUserQuestionRequest) error {
	if strings.TrimSpace(request.Question) == "" {
		return fmt.Errorf("%w: planning question is required", agent.ErrInvalidNextAction)
	}
	if len(request.Options) < 2 || len(request.Options) > 4 {
		return fmt.Errorf("%w: planning question requires 2-4 options", agent.ErrInvalidNextAction)
	}
	for _, option := range request.Options {
		if strings.TrimSpace(option.Label) == "" || strings.TrimSpace(option.Description) == "" {
			return fmt.Errorf("%w: planning question options require label and description", agent.ErrInvalidNextAction)
		}
	}
	return nil
}

func validateProposePlan(request planningtool.ProposePlanRequest) error {
	if strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Summary) == "" {
		return fmt.Errorf("%w: proposed plan requires title and summary", agent.ErrInvalidNextAction)
	}
	if len(nonEmptyList(request.Build)) == 0 || len(nonEmptyList(request.Steps)) == 0 || len(nonEmptyList(request.Verification)) == 0 {
		return fmt.Errorf("%w: proposed plan requires build, steps, and verification items", agent.ErrInvalidNextAction)
	}
	return nil
}

func waitingToken(prefix string, input agent.NextActionInput, callID string) string {
	hash := sha1.Sum([]byte(strings.Join([]string{
		strings.TrimSpace(prefix),
		strings.TrimSpace(input.ProjectID),
		strings.TrimSpace(input.Context.Event.ID),
		strconv.Itoa(input.ExecutionCycle),
		strings.TrimSpace(callID),
	}, "\x00")))
	return strings.ToUpper(strings.TrimSpace(prefix)) + "-" + hex.EncodeToString(hash[:])[:8]
}

func formatAskUserQuestion(token string, request planningtool.AskUserQuestionRequest) string {
	var builder strings.Builder
	builder.WriteString("**Question ")
	builder.WriteString(token)
	builder.WriteString("**")
	if header := strings.TrimSpace(request.Header); header != "" {
		builder.WriteString("\n")
		builder.WriteString(header)
	}
	builder.WriteString("\n\n")
	builder.WriteString(strings.TrimSpace(request.Question))
	builder.WriteString("\n\nOptions:")
	for index, option := range request.Options {
		builder.WriteString("\n")
		builder.WriteString(strconv.Itoa(index + 1))
		builder.WriteString(". ")
		builder.WriteString(strings.TrimSpace(option.Label))
		builder.WriteString(": ")
		builder.WriteString(strings.TrimSpace(option.Description))
	}
	builder.WriteString("\n\nReply with `")
	builder.WriteString(token)
	builder.WriteString(": <answer>`.")
	return strings.TrimSpace(builder.String())
}

func formatProposedPlan(token string, request planningtool.ProposePlanRequest) string {
	var builder strings.Builder
	builder.WriteString("**Plan ")
	builder.WriteString(token)
	builder.WriteString(": ")
	builder.WriteString(strings.TrimSpace(request.Title))
	builder.WriteString("**\n\n")
	builder.WriteString(strings.TrimSpace(request.Summary))
	writePlanSection(&builder, "Build", request.Build)
	writePlanSection(&builder, "Skip", request.Skip)
	writePlanSection(&builder, "Risks", request.Risks)
	writePlanSection(&builder, "Tradeoffs", request.Tradeoffs)
	writePlanSection(&builder, "Architecture", request.Architecture)
	writePlanSection(&builder, "Steps", request.Steps)
	writePlanSection(&builder, "Verification", request.Verification)
	builder.WriteString("\n\nReply with `approve ")
	builder.WriteString(token)
	builder.WriteString("` to let OpenCTO implement this plan, or `")
	builder.WriteString(token)
	builder.WriteString(": <changes>` to revise it.")
	return strings.TrimSpace(builder.String())
}

func writePlanSection(builder *strings.Builder, title string, items []string) {
	cleaned := nonEmptyList(items)
	if len(cleaned) == 0 {
		return
	}
	builder.WriteString("\n\n")
	builder.WriteString(title)
	builder.WriteString(":")
	for _, item := range cleaned {
		builder.WriteString("\n- ")
		builder.WriteString(item)
	}
}

func nonEmptyList(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
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
