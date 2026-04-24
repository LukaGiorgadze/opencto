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
	ProjectName           string
	ProjectID             string
	ProjectState          string
	ProjectDescription    string
	KnownFacts            string
	OpenContradictions    string
	RecentDecisions       string
	OS                    string
	Arch                  string
	Shell                 string
	ProjectRoot           string
	RequestSummary        string
	ClassificationIntent  agent.ClassificationIntent
	ClassificationRoute   agent.ClassificationRoute
	ClassificationTier    domain.RiskTier
	PlanSummary           string
	PlanExecutionOrder    string
	PlanTestStrategy      string
	WorkItems             string
	ExecutionCycle        int
	HasObservationHistory bool
	ObservationHistory    string
	HasLastObservation    bool
	LastWorkItemID        string
	LastTool              string
	LastInput             string
	LastStatus            string
	LastObservation       string
	LastError             string
	RegisteredTools       string
	ExecutionToolName     string
}

type shellToolInput struct {
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	TimeoutMs   int      `json:"timeout_ms,omitempty"`
	Description string   `json:"description,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
}

func (e *OpenAIEngine) selectToolWithRegisteredTools(ctx context.Context, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if e.toolModel == nil {
		return agent.ToolChoice{}, fmt.Errorf("tool selection model is not configured")
	}

	systemPrompt, err := renderToolSelectionPrompt(input)
	if err != nil {
		return agent.ToolChoice{}, err
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, "Choose the next action now. If execution is needed, call the Shell tool. If a direct response is correct, return plain text only."),
	}

	response, err := e.toolModel.GenerateContent(
		ctx,
		messages,
		llms.WithTools(toolregistry.SelectorLLMDefinitions()),
	)
	if err != nil {
		return agent.ToolChoice{}, err
	}

	return toolChoiceFromContentResponse(response, input)
}

func renderToolSelectionPrompt(input agent.ToolSelectionInput) (string, error) {
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := toolSelectionPromptData{
		ProjectName:           projectName,
		ProjectID:             input.ProjectID,
		ProjectState:          formatProjectState(input.Context.ActiveWorkItems, input.Context.OpenContradictions),
		ProjectDescription:    strings.TrimSpace(input.Context.Project.Description),
		KnownFacts:            formatKnownFacts(input.Context.ProjectFacts),
		OpenContradictions:    formatOpenContradictions(input.Context.OpenContradictions),
		RecentDecisions:       formatRecentDecisions(input.Context.RecentDecisions),
		OS:                    input.Runtime.OS,
		Arch:                  input.Runtime.Arch,
		Shell:                 firstNonEmpty(strings.TrimSpace(input.Runtime.Shell), "unknown"),
		ProjectRoot:           firstNonEmpty(strings.TrimSpace(input.Runtime.WorkspaceRoot), "."),
		RequestSummary:        strings.TrimSpace(input.Context.Event.Body),
		ClassificationIntent:  input.Classification.Intent,
		ClassificationRoute:   input.Classification.RoutedTo,
		ClassificationTier:    input.Classification.Tier,
		PlanSummary:           strings.TrimSpace(input.Plan.Summary),
		PlanExecutionOrder:    formatExecutionOrderMetadata(input.Plan.Metadata["execution_order_json"]),
		PlanTestStrategy:      strings.TrimSpace(input.Plan.Metadata["test_strategy"]),
		WorkItems:             formatSelectionWorkItems(input.WorkItems),
		ExecutionCycle:        input.ExecutionCycle,
		HasObservationHistory: len(input.ObservationHistory) > 1,
		ObservationHistory:    formatObservationHistory(input.ObservationHistory),
		HasLastObservation:    input.LastObservation != nil,
		RegisteredTools:       strings.Join(toolregistry.SelectorPromptSummaries(), "\n"),
		ExecutionToolName:     toolregistry.SelectorToolShellName,
	}
	if input.LastObservation != nil {
		data.LastWorkItemID = strings.TrimSpace(input.LastObservation.WorkItemID)
		data.LastTool = string(input.LastObservation.Tool)
		data.LastInput = strings.TrimSpace(input.LastObservation.RequestedAction)
		data.LastStatus = strings.TrimSpace(input.LastObservation.Status)
		data.LastObservation = strings.TrimSpace(input.LastObservation.Observation)
		data.LastError = strings.TrimSpace(input.LastObservation.Error)
	}

	return prompts.Render("tool_choice.tmpl", data)
}

func toolChoiceFromContentResponse(response *llms.ContentResponse, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if response == nil || len(response.Choices) == 0 {
		return agent.ToolChoice{}, fmt.Errorf("tool selection returned no choices")
	}

	choice := response.Choices[0]
	if len(choice.ToolCalls) > 0 {
		return toolChoiceFromToolCall(choice.ToolCalls[0], input, len(choice.ToolCalls) > 1)
	}

	content := strings.TrimSpace(choice.Content)
	if content == "" {
		return agent.ToolChoice{}, fmt.Errorf("tool selection returned neither tool call nor content")
	}

	return agent.ToolChoice{
		Intent:          content,
		InputSummary:    strings.TrimSpace(input.Context.Event.Body),
		ResponseMessage: content,
		Metadata: map[string]string{
			"model_tool": "content_fallback",
		},
	}, nil
}

func toolChoiceFromToolCall(call llms.ToolCall, input agent.ToolSelectionInput, multiple bool) (agent.ToolChoice, error) {
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
		return shellToolChoiceFromInput(definition, call, args, input, multiple)
	default:
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool type %q for call %q", definition.Type, call.FunctionCall.Name)
	}
}

func shellToolChoiceFromInput(definition toolregistry.SelectorDefinition, call llms.ToolCall, args shellToolInput, input agent.ToolSelectionInput, multiple bool) (agent.ToolChoice, error) {
	commandText := strings.TrimSpace(args.Command)
	if commandText == "" {
		return agent.ToolChoice{}, fmt.Errorf("%s tool requires a command", definition.Name)
	}

	metadata := map[string]string{
		"model_tool":   definition.Name,
		"tool_call_id": call.ID,
	}
	if multiple {
		metadata["dropped_additional_tool_calls"] = "true"
	}

	return agent.ToolChoice{
		Type:         definition.Type,
		Intent:       firstNonEmpty(strings.TrimSpace(args.Description), commandText),
		Command:      commandText,
		Args:         trimStringList(args.Args, 100),
		WorkingDir:   firstNonEmpty(strings.TrimSpace(args.WorkingDir), strings.TrimSpace(input.Runtime.WorkspaceRoot)),
		TimeoutMs:    clampToolTimeoutMs(args.TimeoutMs),
		InputSummary: firstNonEmpty(strings.TrimSpace(args.Description), commandText, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  args.Destructive,
		Metadata:     metadata,
	}, nil
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

func formatObservationHistory(history []agent.ExecutionFeedback) string {
	if len(history) <= 1 {
		return ""
	}
	// Exclude the last entry — it is already rendered as "Last Observation".
	prior := history[:len(history)-1]
	parts := make([]string, 0, len(prior))
	for _, obs := range prior {
		entry := fmt.Sprintf("- Cycle %d [%s] %s → %s", obs.Cycle, obs.Tool, strings.TrimSpace(obs.RequestedAction), strings.TrimSpace(obs.Status))
		observation := strings.TrimSpace(obs.Observation)
		if observation != "" {
			// Truncate long observations to keep the prompt concise.
			if len(observation) > 200 {
				observation = observation[:200] + "…"
			}
			entry += ": " + observation
		}
		if errMsg := strings.TrimSpace(obs.Error); errMsg != "" {
			entry += " (error: " + errMsg + ")"
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "\n")
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
