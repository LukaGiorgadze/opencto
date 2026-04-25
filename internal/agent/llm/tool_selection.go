package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type toolSelectionPromptData struct {
	ProjectName          string
	ProjectID            string
	ProjectState         string
	ProjectDescription   string
	KnownFacts           string
	OpenContradictions   string
	OS                   string
	Arch                 string
	Shell                string
	ProjectRoot          string
	ClassificationIntent agent.ClassificationIntent
	ClassificationRoute  agent.ClassificationRoute
	ClassificationTier   domain.RiskTier
	PlanSummary          string
	PlanExecutionOrder   string
	PlanTestStrategy     string
	WorkItems            string
	CurrentWorkItem      string
	ExecutionCycle       int
}

type shellToolInput struct {
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	TimeoutMs   int      `json:"timeout_ms,omitempty"`
	Description string   `json:"description,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
}

type agentLoopLLMOutput struct {
	Action             string `json:"action"`
	WorkItemID         string `json:"work_item_id,omitempty"`
	WorkItemStatus     string `json:"work_item_status,omitempty"`
	ObservationSummary string `json:"observation_summary,omitempty"`
	ResponseMessage    string `json:"response_message,omitempty"`
}

func (e *OpenAIEngine) decideNextActionWithJSON(ctx context.Context, input agent.ToolSelectionInput) (agent.AgentLoopDecision, error) {
	if e.reasoningModel == nil {
		return agent.AgentLoopDecision{}, fmt.Errorf("agent loop model is not configured")
	}

	messages, err := buildToolSelectionMessages(input)
	if err != nil {
		return agent.AgentLoopDecision{}, err
	}

	response, err := e.reasoningModel.GenerateContent(
		ctx,
		messages,
		llms.WithTools(toolregistry.SelectorLLMDefinitions()),
	)
	if err != nil {
		return agent.AgentLoopDecision{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.AgentLoopDecision{}, fmt.Errorf("model returned no choices")
	}

	return agentLoopDecisionFromContentResponse(response, input)
}

func buildToolSelectionMessages(input agent.ToolSelectionInput) ([]llms.MessageContent, error) {
	systemPrompt, err := renderToolSelectionPrompt(input)
	if err != nil {
		return nil, err
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
	}
	if userText := strings.TrimSpace(input.Context.Event.Body); userText != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, userText))
	}
	for _, feedback := range toolSelectionFeedback(input) {
		message := formatToolObservationMessage(feedback)
		if message != "" {
			messages = append(messages, llms.TextParts(llms.ChatMessageTypeAI, message))
		}
	}
	return messages, nil
}

func renderToolSelectionPrompt(input agent.ToolSelectionInput) (string, error) {
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := toolSelectionPromptData{
		ProjectName:          projectName,
		ProjectID:            input.ProjectID,
		ProjectState:         formatProjectState(input.Context.ActiveWorkItems, input.Context.OpenContradictions),
		ProjectDescription:   strings.TrimSpace(input.Context.Project.Description),
		KnownFacts:           formatKnownFacts(input.Context.ProjectFacts),
		OpenContradictions:   formatOpenContradictions(input.Context.OpenContradictions),
		OS:                   input.Runtime.OS,
		Arch:                 input.Runtime.Arch,
		Shell:                firstNonEmpty(strings.TrimSpace(input.Runtime.Shell), "unknown"),
		ProjectRoot:          firstNonEmpty(strings.TrimSpace(input.Runtime.WorkspaceRoot), "."),
		ClassificationIntent: input.Classification.Intent,
		ClassificationRoute:  input.Classification.RoutedTo,
		ClassificationTier:   input.Classification.Tier,
		PlanSummary:          strings.TrimSpace(input.Plan.Summary),
		PlanExecutionOrder:   formatExecutionOrderMetadata(input.Plan.Metadata["execution_order_json"]),
		PlanTestStrategy:     strings.TrimSpace(input.Plan.Metadata["test_strategy"]),
		WorkItems:            formatSelectionWorkItems(input.WorkItems),
		CurrentWorkItem:      formatSelectionCurrentWorkItem(input.WorkItems, input.CurrentWorkItemID),
		ExecutionCycle:       input.ExecutionCycle,
	}

	return prompts.Render("tool_choice.tmpl", data)
}

func agentLoopDecisionFromContentResponse(response *llms.ContentResponse, input agent.ToolSelectionInput) (agent.AgentLoopDecision, error) {
	if response == nil || len(response.Choices) == 0 {
		return agent.AgentLoopDecision{}, fmt.Errorf("agent loop returned no choices")
	}

	choice := response.Choices[0]
	output, err := decodeJSONOutput[agentLoopLLMOutput](choice.Content)
	if err != nil {
		return agent.AgentLoopDecision{}, err
	}

	var toolChoice *agent.ToolChoice
	switch len(choice.ToolCalls) {
	case 0:
	case 1:
		choice, err := toolChoiceFromToolCall(choice.ToolCalls[0], input)
		if err != nil {
			return agent.AgentLoopDecision{}, err
		}
		toolChoice = &choice
	default:
		return agent.AgentLoopDecision{}, fmt.Errorf("%w: continue action requires exactly one tool call, got %d", agent.ErrInvalidAgentLoopDecision, len(choice.ToolCalls))
	}

	return normalizeAgentLoopDecision(output, input, toolChoice)
}

func normalizeAgentLoopDecision(output agentLoopLLMOutput, input agent.ToolSelectionInput, toolChoice *agent.ToolChoice) (agent.AgentLoopDecision, error) {
	action := normalizeAgentLoopAction(output.Action, toolChoice != nil, output.ResponseMessage)
	if action == "" {
		return agent.AgentLoopDecision{}, fmt.Errorf("%w: action must be one of continue, complete, clarify, or block", agent.ErrInvalidAgentLoopDecision)
	}

	workItemID := firstNonEmpty(output.WorkItemID, input.CurrentWorkItemID)
	workItemStatus, err := normalizeAgentLoopWorkItemStatus(output.WorkItemStatus, action, input.LastObservation != nil)
	if err != nil {
		return agent.AgentLoopDecision{}, err
	}

	decision := agent.AgentLoopDecision{
		Action:             action,
		WorkItemID:         workItemID,
		WorkItemStatus:     workItemStatus,
		ObservationSummary: strings.TrimSpace(output.ObservationSummary),
		ResponseMessage:    strings.TrimSpace(output.ResponseMessage),
	}

	switch action {
	case agent.AgentLoopActionContinue:
		if toolChoice == nil {
			return agent.AgentLoopDecision{}, fmt.Errorf("%w: continue action requires exactly one tool call", agent.ErrInvalidAgentLoopDecision)
		}
		if toolChoice.Metadata == nil {
			toolChoice.Metadata = map[string]string{}
		}
		toolChoice.Metadata["agent_loop_action"] = string(action)
		decision.ToolChoice = toolChoice
		decision.ResponseMessage = ""
	case agent.AgentLoopActionComplete:
		if toolChoice != nil {
			return agent.AgentLoopDecision{}, fmt.Errorf("%w: complete action cannot include a tool call", agent.ErrInvalidAgentLoopDecision)
		}
		if decision.ResponseMessage == "" {
			decision.ResponseMessage = firstNonEmpty(decision.ObservationSummary, "Execution completed.")
		}
	case agent.AgentLoopActionClarify:
		if toolChoice != nil {
			return agent.AgentLoopDecision{}, fmt.Errorf("%w: clarify action cannot include a tool call", agent.ErrInvalidAgentLoopDecision)
		}
		if decision.ResponseMessage == "" {
			return agent.AgentLoopDecision{}, fmt.Errorf("%w: clarify action requires response_message", agent.ErrInvalidAgentLoopDecision)
		}
	case agent.AgentLoopActionBlock:
		if toolChoice != nil {
			return agent.AgentLoopDecision{}, fmt.Errorf("%w: block action cannot include a tool call", agent.ErrInvalidAgentLoopDecision)
		}
		if decision.ResponseMessage == "" {
			decision.ResponseMessage = firstNonEmpty(decision.ObservationSummary, "Execution is blocked.")
		}
	default:
		return agent.AgentLoopDecision{}, fmt.Errorf("%w: unsupported action %q", agent.ErrInvalidAgentLoopDecision, action)
	}

	return decision, nil
}

func normalizeAgentLoopAction(value string, hasTool bool, responseMessage string) agent.AgentLoopAction {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(agent.AgentLoopActionContinue), "act", "tool":
		return agent.AgentLoopActionContinue
	case string(agent.AgentLoopActionComplete), "done", "finish", "final":
		return agent.AgentLoopActionComplete
	case string(agent.AgentLoopActionClarify), "ask":
		return agent.AgentLoopActionClarify
	case string(agent.AgentLoopActionBlock), "blocked", "fail", "failed":
		return agent.AgentLoopActionBlock
	}
	if hasTool {
		return agent.AgentLoopActionContinue
	}
	if strings.TrimSpace(responseMessage) != "" {
		return agent.AgentLoopActionComplete
	}
	return ""
}

func normalizeAgentLoopWorkItemStatus(value string, action agent.AgentLoopAction, hasObservation bool) (domain.WorkItemStatus, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		if !hasObservation {
			return "", nil
		}
		switch action {
		case agent.AgentLoopActionComplete:
			return domain.WorkItemStatusCompleted, nil
		case agent.AgentLoopActionClarify, agent.AgentLoopActionBlock:
			return domain.WorkItemStatusBlocked, nil
		case agent.AgentLoopActionContinue:
			return domain.WorkItemStatusReady, nil
		default:
			return "", nil
		}
	case string(domain.WorkItemStatusReady):
		return domain.WorkItemStatusReady, nil
	case string(domain.WorkItemStatusCompleted):
		return domain.WorkItemStatusCompleted, nil
	case string(domain.WorkItemStatusFailed):
		return domain.WorkItemStatusFailed, nil
	case string(domain.WorkItemStatusBlocked):
		return domain.WorkItemStatusBlocked, nil
	default:
		return "", fmt.Errorf("%w: unsupported work item status %q", agent.ErrInvalidAgentLoopDecision, value)
	}
}

func toolChoiceFromToolCall(call llms.ToolCall, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if call.FunctionCall == nil {
		return agent.ToolChoice{}, fmt.Errorf("tool call %q missing function payload", call.ID)
	}

	definition, ok := toolregistry.SelectorDefinitionByName(call.FunctionCall.Name)
	if !ok {
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool call %q", call.FunctionCall.Name)
	}

	switch definition.Type {
	case domain.ToolTypeShell:
		var args shellToolInput
		if err := json.Unmarshal([]byte(call.FunctionCall.Arguments), &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return shellToolChoiceFromInput(definition, call, args, input)
	default:
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool type %q for call %q", definition.Type, call.FunctionCall.Name)
	}
}

func shellToolChoiceFromInput(definition toolregistry.SelectorDefinition, call llms.ToolCall, args shellToolInput, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	commandText := strings.TrimSpace(args.Command)
	if commandText == "" {
		return agent.ToolChoice{}, fmt.Errorf("%s tool requires a command", definition.Name)
	}
	command := commandText
	commandArgs := trimStringList(args.Args, 100)
	wrapped := false
	if shouldWrapShellCommand(commandText, commandArgs) {
		command = firstNonEmpty(strings.TrimSpace(input.Runtime.Shell), "/bin/sh")
		commandArgs = []string{"-lc", commandText}
		wrapped = true
	}

	metadata := map[string]string{
		"model_tool":   definition.Name,
		"tool_call_id": call.ID,
	}
	if wrapped {
		metadata["wrapped_shell_command"] = "true"
		metadata["original_command"] = commandText
	}

	return agent.ToolChoice{
		Type:         definition.Type,
		Intent:       firstNonEmpty(strings.TrimSpace(args.Description), commandText),
		Command:      command,
		Args:         commandArgs,
		WorkingDir:   firstNonEmpty(strings.TrimSpace(args.WorkingDir), strings.TrimSpace(input.Runtime.WorkspaceRoot)),
		TimeoutMs:    clampToolTimeoutMs(args.TimeoutMs),
		InputSummary: firstNonEmpty(strings.TrimSpace(args.Description), commandText, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  args.Destructive,
		Metadata:     metadata,
	}, nil
}

func shouldWrapShellCommand(command string, args []string) bool {
	if len(args) > 0 {
		return false
	}
	return strings.ContainsAny(command, " \t\r\n;&|<>*$`()")
}

func formatPlanMetadataList(value string) string {
	items := decodeJSONMetadataList(value)
	if len(items) == 0 {
		return "none"
	}
	return strings.Join(items, "; ")
}

func formatExecutionOrderMetadata(value string) string {
	groups := decodeJSONMetadataMatrix(value)
	if len(groups) == 0 {
		return ""
	}
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		parts = append(parts, "["+strings.Join(group, " || ")+"]")
	}
	return strings.Join(parts, " -> ")
}

func formatSelectionWorkItems(items []domain.WorkItem) string {
	if len(items) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "untitled work item"
		}
		description := strings.TrimSpace(item.Description)
		entry := fmt.Sprintf("%s [id=%s,status=%s,tier=%d]", title, item.ID, item.Status, item.RiskTier)
		if description != "" {
			entry += ": " + description
		}
		var details []string
		if dependsOn := formatPlanMetadataList(item.Metadata["depends_on_json"]); dependsOn != "none" {
			details = append(details, "depends_on="+dependsOn)
		}
		if len(details) > 0 {
			entry += " (" + strings.Join(details, "; ") + ")"
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}

func formatSelectionCurrentWorkItem(items []domain.WorkItem, currentID string) string {
	currentID = strings.TrimSpace(currentID)
	if currentID == "" {
		return "none"
	}
	for _, item := range items {
		if item.ID == currentID {
			return formatSelectionWorkItems([]domain.WorkItem{item})
		}
	}
	return fmt.Sprintf("unknown [id=%s]", currentID)
}

func toolSelectionFeedback(input agent.ToolSelectionInput) []agent.ExecutionFeedback {
	if len(input.ObservationHistory) > 0 {
		return input.ObservationHistory
	}
	if input.LastObservation != nil {
		return []agent.ExecutionFeedback{*input.LastObservation}
	}
	return nil
}

func formatToolObservationMessage(feedback agent.ExecutionFeedback) string {
	lines := []string{
		"Work item: " + firstNonEmpty(strings.TrimSpace(feedback.WorkItemID), "unknown"),
		"Tool: " + strings.TrimSpace(string(feedback.Tool)),
		"Input: " + strings.TrimSpace(feedback.RequestedAction),
	}
	if command := strings.TrimSpace(feedback.Command); command != "" {
		lines = append(lines, "Command: "+command)
	}
	if len(feedback.Args) > 0 {
		lines = append(lines, "Args: "+formatToolArgs(feedback.Args))
	}
	if workingDir := strings.TrimSpace(feedback.Metadata["working_directory"]); workingDir != "" {
		lines = append(lines, "Working directory: "+workingDir)
	}
	lines = append(lines,
		"Status: "+strings.TrimSpace(feedback.Status),
		"Observation: "+strings.TrimSpace(feedback.Observation),
	)
	if errMsg := strings.TrimSpace(feedback.Error); errMsg != "" {
		lines = append(lines, "Error: "+errMsg)
	}
	return strings.Join(lines, "\n")
}

func formatToolArgs(args []string) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		return strings.Join(args, " ")
	}
	return string(encoded)
}

func clampToolTimeoutMs(value int) int {
	switch {
	case value <= 0:
		return 120000
	case value > 600000:
		return 600000
	default:
		return value
	}
}
