package activities

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log/slog"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
	"github.com/opencto/opencto/internal/tools/git"
	"github.com/opencto/opencto/internal/tools/shell"
)

type ProjectStore interface {
	Append(context.Context, domain.Event) error
	ListByProject(context.Context, string, int) ([]domain.Event, error)
	ListPending(context.Context, string) ([]domain.WorkItem, error)
	UpsertWorkItem(context.Context, domain.WorkItem) error
	GetWorkItem(context.Context, string, string) (domain.WorkItem, error)
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
	AppendADR(context.Context, domain.ADR) error
}

type Reporter interface {
	Report(context.Context, domain.Event, string) error
}

type SemanticEmbedder interface {
	EmbedDocuments(context.Context, []string) ([][]float32, error)
	EmbedQuery(context.Context, string) ([]float32, error)
}

type Activities struct {
	Store           ProjectStore
	Engine          agent.Engine
	Shell           shell.Executor
	ADRWriter       *git.ADRWriter
	Reporter        Reporter
	Project         domain.Project
	WorkspaceRoot   string
	AvailableSkills []string
	MemoryEmbedder  SemanticEmbedder
	EmbeddingModel  string
	Logger          *slog.Logger
}

type NextActionRequest struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	Decision           agent.DecisionOutput      `json:"decision"`
	LastResult         *ExecuteToolResult        `json:"last_result,omitempty"`
	ObservationHistory []agent.ExecutionFeedback `json:"observation_history,omitempty"`
	ExecutionCycle     int                       `json:"execution_cycle"`
	ForceFinal         bool                      `json:"force_final,omitempty"`
	ResumedFromPause   bool                      `json:"resumed_from_pause,omitempty"`
}

type NextActionResult struct {
	Decision      agent.DecisionOutput     `json:"decision"`
	ToolChoice    *agent.ToolChoice        `json:"tool_choice,omitempty"`
	WorkItemID    string                   `json:"work_item_id,omitempty"`
	Observation   *agent.ExecutionFeedback `json:"observation,omitempty"`
	FinalAnswer   string                   `json:"final_answer,omitempty"`
	Status        string                   `json:"status"`
	AssistantText string                   `json:"assistant_text,omitempty"`
}

type ExecuteToolRequest struct {
	ProjectID  string           `json:"project_id"`
	WorkItemID string           `json:"work_item_id"`
	ToolChoice agent.ToolChoice `json:"tool_choice"`
}

type ExecuteToolResult struct {
	Cycle            int                    `json:"cycle"`
	WorkItemID       string                 `json:"work_item_id,omitempty"`
	ToolCallID       string                 `json:"tool_call_id,omitempty"`
	Tool             domain.ToolType        `json:"tool,omitempty"`
	Status           domain.ExecutionStatus `json:"status"`
	RequestedAction  string                 `json:"requested_action,omitempty"`
	Command          string                 `json:"command,omitempty"`
	Args             []string               `json:"args,omitempty"`
	Observation      string                 `json:"observation,omitempty"`
	Error            string                 `json:"error,omitempty"`
	WorkingDirectory string                 `json:"working_directory,omitempty"`
	ResultCode       string                 `json:"result_code,omitempty"`
	Metadata         map[string]string      `json:"metadata,omitempty"`
}

const (
	NextActionStatusTool      = "tool"
	NextActionStatusCompleted = "completed"
	NextActionStatusBlocked   = "blocked"
	NextActionStatusFailed    = "failed"
	NextActionStatusIgnored   = "ignored"
)

func (r NextActionResult) IsTerminal() bool {
	return r.Status != NextActionStatusTool
}

const maxObservationSummaryLength = 1500

type toolExecutionContext struct {
	ProjectID          string
	WorkItemID         string
	ToolCallID         string
	Cycle              int
	StartedAt          time.Time
	ExecutionAttemptID string
	InvocationID       string
	Timeout            time.Duration
	FallbackCandidates []domain.ToolType
}

func (a *Activities) LoadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	return a.loadContext(ctx, event)
}

func (a *Activities) loadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	activeWorkItems, err := a.Store.ListPending(ctx, event.ProjectID)
	if err != nil {
		return agent.Context{}, err
	}

	project := a.Project
	if strings.TrimSpace(project.ID) == "" {
		project.ID = event.ProjectID
	}
	return agent.Context{
		Event:           event,
		Project:         project,
		ActiveWorkItems: activeWorkItems,
	}, nil
}

func (a *Activities) NextAction(ctx context.Context, request NextActionRequest) (NextActionResult, error) {
	if a.Engine == nil {
		return NextActionResult{}, fmt.Errorf("decision engine is not configured")
	}
	if a.Store == nil {
		return NextActionResult{}, fmt.Errorf("project store is not configured")
	}

	projectID := strings.TrimSpace(request.ProjectID)
	event := request.Event
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	if projectID == "" {
		return NextActionResult{}, fmt.Errorf("project_id is required")
	}
	event.ProjectID = projectID
	if request.ExecutionCycle <= 1 && !request.ResumedFromPause {
		if err := a.Store.Append(ctx, event); err != nil {
			return NextActionResult{}, err
		}
	}

	loaded, err := a.loadContext(ctx, event)
	if err != nil {
		return NextActionResult{}, err
	}

	now := time.Now().UTC()
	decision := request.Decision
	if err := ensureNextActionDecision(&decision, projectID, event, now); err != nil {
		return NextActionResult{}, err
	}

	history := append([]agent.ExecutionFeedback(nil), request.ObservationHistory...)
	var observation *agent.ExecutionFeedback
	if request.LastResult != nil {
		feedback := executionFeedback(*request.LastResult)
		observation = &feedback
		history = append(history, feedback)
		decision.ToolChoice = agent.ToolChoice{}
		if err := applyObservationToDecision(&decision, feedback, now); err != nil {
			return NextActionResult{}, err
		}
	}

	engineOutput, err := a.Engine.NextAction(ctx, agent.NextActionInput{
		ProjectID:          projectID,
		Context:            loaded,
		Decision:           decision,
		Runtime:            buildRuntimeContext(a.WorkspaceRoot),
		ExecutionCycle:     request.ExecutionCycle,
		ForceFinal:         request.ForceFinal,
		ResumedFromPause:   request.ResumedFromPause,
		LastObservation:    observation,
		ObservationHistory: history,
	})
	if err != nil {
		return NextActionResult{}, err
	}
	if len(engineOutput.Decision.WorkItems) > 0 || !engineOutput.Decision.ToolChoice.IsZero() || strings.TrimSpace(engineOutput.Decision.ResponseMessage) != "" {
		decision = engineOutput.Decision
	}
	if err := ensureNextActionDecision(&decision, projectID, event, now); err != nil {
		return NextActionResult{}, err
	}

	if request.ForceFinal && engineOutput.Status == NextActionStatusTool {
		engineOutput.Status = NextActionStatusBlocked
		engineOutput.FinalAnswer = cycleLimitFinalAnswer(history, maxObservationSummaryLength)
		engineOutput.ToolChoice = nil
	}

	switch engineOutput.Status {
	case NextActionStatusTool:
		return a.prepareToolNextAction(ctx, decision, observation, engineOutput, request.ExecutionCycle, now)
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return a.finishNextAction(ctx, event, decision, observation, engineOutput, now)
	default:
		return NextActionResult{}, fmt.Errorf("unsupported next action status %q", engineOutput.Status)
	}
}

func (a *Activities) prepareToolNextAction(ctx context.Context, decision agent.DecisionOutput, observation *agent.ExecutionFeedback, output agent.NextActionOutput, cycle int, now time.Time) (NextActionResult, error) {
	if output.ToolChoice == nil {
		return NextActionResult{}, fmt.Errorf("%w: tool status requires exactly one tool choice", agent.ErrInvalidToolChoice)
	}

	choice := *output.ToolChoice
	workItemID := nextActionToolWorkItemID(output, choice, decision)
	if strings.TrimSpace(workItemID) == "" {
		return NextActionResult{}, fmt.Errorf("%w: tool choice is missing work item id", agent.ErrInvalidToolChoice)
	}
	if err := ensureToolWorkItem(&decision, workItemID, now); err != nil {
		return NextActionResult{}, err
	}
	if err := completePreviousWorkItemForNextAction(&decision, workItemID, observation, now); err != nil {
		return NextActionResult{}, err
	}

	index := decisionWorkItemIndexByID(decision.WorkItems, workItemID)
	if index >= 0 {
		decision.WorkItems[index].Status = domain.WorkItemStatusRunning
		decision.WorkItems[index].UpdatedAt = now
	}

	assistantText := strings.TrimSpace(output.AssistantText)
	if assistantText == "" {
		assistantText = strings.TrimSpace(choice.Intent)
	}
	ensureToolChoiceMetadata(&choice, workItemID, cycle, assistantText)
	decision.ToolChoice = choice
	decision.ResponseMessage = ""

	if err := a.persistDecision(ctx, decision); err != nil {
		return NextActionResult{}, err
	}

	return NextActionResult{
		Decision:      decision,
		ToolChoice:    &choice,
		WorkItemID:    workItemID,
		Observation:   observation,
		Status:        NextActionStatusTool,
		AssistantText: assistantText,
	}, nil
}

func (a *Activities) finishNextAction(ctx context.Context, event domain.Event, decision agent.DecisionOutput, observation *agent.ExecutionFeedback, output agent.NextActionOutput, now time.Time) (NextActionResult, error) {
	message := strings.TrimSpace(output.FinalAnswer)
	if message == "" {
		message = strings.TrimSpace(output.Decision.ResponseMessage)
	}
	if message == "" {
		return NextActionResult{}, fmt.Errorf("%w: terminal next action is missing final answer", agent.ErrInvalidNextAction)
	}

	decision.ToolChoice = agent.ToolChoice{}
	decision.ResponseMessage = message
	markFinalDecisionWorkItems(&decision, terminalWorkItemStatus(output.Status), observation, now)

	if err := a.persistDecision(ctx, decision); err != nil {
		return NextActionResult{}, err
	}
	if output.Status != NextActionStatusIgnored {
		if a.ADRWriter != nil && len(decision.WorkItems) > 0 {
			_, _ = a.writeADR(ctx, event.ProjectID, "Execution Summary", message, workItemSummaries(decision.WorkItems))
		}
		if a.Reporter != nil {
			if err := a.Reporter.Report(ctx, event, message); err != nil {
				return NextActionResult{}, err
			}
		}
	}

	return NextActionResult{
		Decision:      decision,
		Observation:   observation,
		FinalAnswer:   message,
		Status:        output.Status,
		AssistantText: output.AssistantText,
	}, nil
}

func (a *Activities) persistDecision(ctx context.Context, decision agent.DecisionOutput) error {
	for _, item := range decision.WorkItems {
		if item.ID == "" {
			continue
		}
		if err := a.Store.UpsertWorkItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func workItemSummaries(items []domain.WorkItem) []string {
	summaries := make([]string, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		description := strings.TrimSpace(item.Description)
		switch {
		case title != "" && description != "":
			summaries = append(summaries, title+": "+description)
		case title != "":
			summaries = append(summaries, title)
		case description != "":
			summaries = append(summaries, description)
		}
	}
	return summaries
}

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, err
	}
	if a.Store == nil {
		return ExecuteToolResult{}, fmt.Errorf("project store is not configured")
	}
	if a.Shell == nil {
		return ExecuteToolResult{}, fmt.Errorf("shell executor is not configured")
	}

	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       request.ToolChoice.Type,
		Summary:    request.ToolChoice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
		},
	}
	if err := a.Store.UpsertExecutionAttempt(ctx, attempt); err != nil {
		return ExecuteToolResult{}, err
	}

	result, err := a.Shell.Run(ctx, shell.Request{
		ProjectID:          execution.ProjectID,
		Intent:             request.ToolChoice.Intent,
		Command:            request.ToolChoice.Command,
		Args:               request.ToolChoice.Args,
		WorkingDir:         request.ToolChoice.WorkingDir,
		WorkspaceRoot:      a.WorkspaceRoot,
		Timeout:            execution.Timeout,
		FallbackCandidates: execution.FallbackCandidates,
	})

	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   request.ToolChoice.WorkingDir,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		OutputSummary:      strings.TrimSpace(result.Stdout),
		ResultCode:         fmt.Sprintf("%d", result.ExitCode),
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata: map[string]string{
			"shell_exit_status": fmt.Sprintf("%d", result.ExitCode),
			"started_at":        result.StartedAt.UTC().Format(time.RFC3339Nano),
			"completed_at":      result.CompletedAt.UTC().Format(time.RFC3339Nano),
			"tool_call_id":      execution.ToolCallID,
		},
	}
	for key, value := range request.ToolChoice.Metadata {
		if strings.TrimSpace(value) != "" {
			invocation.Metadata[key] = value
		}
	}

	var errorMessage string
	if err != nil {
		attempt.Status = domain.ExecutionStatusFailed
		attempt.OutputSummary = summarizeObservation(result.Stdout, result.Stderr, err)
		invocation.ErrorDetails = err.Error()
		invocation.OutputSummary = attempt.OutputSummary
		errorMessage = err.Error()
	} else {
		attempt.Status = domain.ExecutionStatusSucceeded
		attempt.OutputSummary = summarizeObservation(result.Stdout, result.Stderr, nil)
		invocation.OutputSummary = attempt.OutputSummary
	}

	if persistErr := a.Store.UpsertExecutionAttempt(ctx, attempt); persistErr != nil {
		return ExecuteToolResult{}, persistErr
	}
	if persistErr := a.Store.UpsertToolInvocation(ctx, invocation); persistErr != nil {
		return ExecuteToolResult{}, persistErr
	}

	return ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           attempt.Status,
		RequestedAction:  request.ToolChoice.Intent,
		Command:          request.ToolChoice.Command,
		Args:             request.ToolChoice.Args,
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: request.ToolChoice.WorkingDir,
		ResultCode:       invocation.ResultCode,
		Metadata:         invocation.Metadata,
	}, nil
}

func newToolExecutionContext(request ExecuteToolRequest) (toolExecutionContext, error) {
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		return toolExecutionContext{}, fmt.Errorf("project_id is required")
	}
	workItemID := strings.TrimSpace(request.WorkItemID)
	if workItemID == "" {
		return toolExecutionContext{}, fmt.Errorf("work_item_id is required")
	}
	toolCallID := strings.TrimSpace(request.ToolChoice.ToolCallID)
	if toolCallID == "" && request.ToolChoice.Metadata != nil {
		toolCallID = strings.TrimSpace(request.ToolChoice.Metadata["tool_call_id"])
	}
	if toolCallID == "" {
		return toolExecutionContext{}, fmt.Errorf("tool_call_id is required")
	}
	executionID := stableActivityID("execution-attempt", projectID, workItemID, toolCallID)
	invocationID := stableActivityID("tool-invocation", projectID, workItemID, toolCallID)
	return toolExecutionContext{
		ProjectID:          projectID,
		WorkItemID:         workItemID,
		ToolCallID:         toolCallID,
		Cycle:              executionCycle(request.ToolChoice.Metadata),
		StartedAt:          time.Now().UTC(),
		ExecutionAttemptID: executionID,
		InvocationID:       invocationID,
		Timeout:            toolChoiceTimeout(request.ToolChoice),
		FallbackCandidates: toolregistry.FallbackCandidates(request.ToolChoice.Type),
	}, nil
}

func executionFeedback(result ExecuteToolResult) agent.ExecutionFeedback {
	metadata := cloneMetadata(result.Metadata)
	if result.WorkingDirectory != "" {
		metadata["working_directory"] = result.WorkingDirectory
	}
	if result.ResultCode != "" {
		metadata["result_code"] = result.ResultCode
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return agent.ExecutionFeedback{
		Cycle:           result.Cycle,
		WorkItemID:      result.WorkItemID,
		ToolCallID:      result.ToolCallID,
		Tool:            result.Tool,
		Status:          string(result.Status),
		RequestedAction: result.RequestedAction,
		Command:         result.Command,
		Args:            result.Args,
		Observation:     result.Observation,
		Error:           result.Error,
		Metadata:        metadata,
	}
}

func ensureNextActionDecision(decision *agent.DecisionOutput, projectID string, event domain.Event, now time.Time) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	summary := firstNonEmpty(strings.TrimSpace(event.Body), "Handle inbound request.")
	if len(decision.WorkItems) == 0 {
		workItemID := stableActivityID("work-item", projectID, event.ID, "1")
		decision.WorkItems = []domain.WorkItem{{
			ID:          workItemID,
			ProjectID:   projectID,
			Title:       "Handle request",
			Description: summary,
			Status:      domain.WorkItemStatusReady,
			CreatedAt:   now,
			UpdatedAt:   now,
		}}
		return nil
	}
	for index := range decision.WorkItems {
		if decision.WorkItems[index].ProjectID == "" {
			decision.WorkItems[index].ProjectID = projectID
		}
		if decision.WorkItems[index].Status == "" {
			decision.WorkItems[index].Status = domain.WorkItemStatusReady
		}
		if decision.WorkItems[index].CreatedAt.IsZero() {
			decision.WorkItems[index].CreatedAt = now
		}
		if decision.WorkItems[index].UpdatedAt.IsZero() {
			decision.WorkItems[index].UpdatedAt = now
		}
	}
	return nil
}

func ensureToolWorkItem(decision *agent.DecisionOutput, workItemID string, now time.Time) error {
	if decisionWorkItemIndexByID(decision.WorkItems, workItemID) >= 0 {
		return nil
	}
	if strings.TrimSpace(workItemID) == "" {
		return fmt.Errorf("work item id is required")
	}
	item := domain.WorkItem{
		ID:          workItemID,
		ProjectID:   decisionProjectID(*decision),
		Title:       "Handle request",
		Description: "Handle inbound request.",
		Status:      domain.WorkItemStatusReady,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	decision.WorkItems = append(decision.WorkItems, item)
	return nil
}

func decisionProjectID(decision agent.DecisionOutput) string {
	for _, item := range decision.WorkItems {
		if projectID := strings.TrimSpace(item.ProjectID); projectID != "" {
			return projectID
		}
	}
	return ""
}

func currentNextActionWorkItemID(decision agent.DecisionOutput, observation *agent.ExecutionFeedback) string {
	if observation != nil {
		workItemID := strings.TrimSpace(observation.WorkItemID)
		index := decisionWorkItemIndexByID(decision.WorkItems, workItemID)
		if index >= 0 && decision.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			return workItemID
		}
	}
	for _, item := range decision.WorkItems {
		if item.Status != domain.WorkItemStatusCompleted {
			return item.ID
		}
	}
	return ""
}

func nextActionToolWorkItemID(output agent.NextActionOutput, choice agent.ToolChoice, decision agent.DecisionOutput) string {
	if strings.TrimSpace(output.WorkItemID) != "" {
		return strings.TrimSpace(output.WorkItemID)
	}
	if strings.TrimSpace(choice.Metadata["work_item_id"]) != "" {
		return strings.TrimSpace(choice.Metadata["work_item_id"])
	}
	return currentNextActionWorkItemID(decision, nil)
}

func completePreviousWorkItemForNextAction(decision *agent.DecisionOutput, nextWorkItemID string, observation *agent.ExecutionFeedback, now time.Time) error {
	if observation == nil {
		return nil
	}
	if observation.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(observation.Error) != "" {
		return nil
	}
	previousWorkItemID := strings.TrimSpace(observation.WorkItemID)
	if previousWorkItemID == "" || previousWorkItemID == strings.TrimSpace(nextWorkItemID) {
		return nil
	}
	index := decisionWorkItemIndexByID(decision.WorkItems, previousWorkItemID)
	if index < 0 {
		return fmt.Errorf("work item %q not found for status update", previousWorkItemID)
	}
	decision.WorkItems[index].Status = domain.WorkItemStatusCompleted
	decision.WorkItems[index].UpdatedAt = now
	return nil
}

func ensureToolChoiceMetadata(choice *agent.ToolChoice, workItemID string, cycle int, assistantText string) {
	if choice.Metadata == nil {
		choice.Metadata = map[string]string{}
	}
	toolCallID := strings.TrimSpace(choice.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(choice.Metadata["tool_call_id"])
	}
	if toolCallID == "" {
		toolCallID = "toolu_" + stableActivityID("tool-call", workItemID, strconv.Itoa(cycle))
	}
	choice.ToolCallID = toolCallID
	choice.Metadata["tool_call_id"] = toolCallID
	choice.Metadata["work_item_id"] = workItemID
	choice.Metadata["execution_cycle"] = strconv.Itoa(cycle)
	if strings.TrimSpace(assistantText) != "" {
		choice.Metadata["assistant_text"] = strings.TrimSpace(assistantText)
	}
	if choice.WorkingDir != "" {
		choice.Metadata["working_directory"] = choice.WorkingDir
	}
	if choice.TimeoutMs > 0 {
		choice.Metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
}

func markFinalDecisionWorkItems(decision *agent.DecisionOutput, status domain.WorkItemStatus, observation *agent.ExecutionFeedback, now time.Time) {
	if status == "" {
		status = domain.WorkItemStatusCompleted
	}
	if observation != nil && strings.TrimSpace(observation.WorkItemID) != "" {
		index := decisionWorkItemIndexByID(decision.WorkItems, observation.WorkItemID)
		if index >= 0 && decision.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			decision.WorkItems[index].Status = status
			decision.WorkItems[index].UpdatedAt = now
		}
	}
	for index := range decision.WorkItems {
		if decision.WorkItems[index].Status == domain.WorkItemStatusCompleted {
			continue
		}
		decision.WorkItems[index].Status = status
		decision.WorkItems[index].UpdatedAt = now
	}
}

func terminalWorkItemStatus(status string) domain.WorkItemStatus {
	switch status {
	case NextActionStatusBlocked:
		return domain.WorkItemStatusBlocked
	case NextActionStatusFailed:
		return domain.WorkItemStatusFailed
	default:
		return domain.WorkItemStatusCompleted
	}
}

func cycleLimitFinalAnswer(history []agent.ExecutionFeedback, limit int) string {
	if len(history) == 0 {
		return "Stopped after reaching the execution cycle limit before a final answer was produced."
	}
	last := history[len(history)-1]
	summary := strings.TrimSpace(last.Observation)
	if summary == "" {
		summary = strings.TrimSpace(last.Error)
	}
	return compactObservation(fmt.Sprintf("Stopped after reaching the execution cycle limit. Last result: %s", summary), limit)
}

func applyObservationToDecision(decision *agent.DecisionOutput, observation agent.ExecutionFeedback, now time.Time) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	if observation.WorkItemID == "" {
		return nil
	}
	status := domain.WorkItemStatusReady
	if observation.Status == string(domain.ExecutionStatusCanceled) {
		status = domain.WorkItemStatusBlocked
	}
	if err := setDecisionWorkItemStatus(decision, observation.WorkItemID, status, now); err != nil {
		return err
	}
	index := decisionWorkItemIndexByID(decision.WorkItems, observation.WorkItemID)
	if index >= 0 {
		if decision.WorkItems[index].Metadata == nil {
			decision.WorkItems[index].Metadata = map[string]string{}
		}
		decision.WorkItems[index].Metadata["last_execution_status"] = observation.Status
		if code := strings.TrimSpace(observation.Metadata["result_code"]); code != "" {
			decision.WorkItems[index].Metadata["last_result_code"] = code
		}
	}
	return nil
}

func setDecisionWorkItemStatus(decision *agent.DecisionOutput, workItemID string, status domain.WorkItemStatus, now time.Time) error {
	for index := range decision.WorkItems {
		if decision.WorkItems[index].ID == workItemID {
			decision.WorkItems[index].Status = status
			decision.WorkItems[index].UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("work item %q not found for status update", workItemID)
}

func decisionWorkItemIndexByID(items []domain.WorkItem, workItemID string) int {
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		return -1
	}
	for index, item := range items {
		if item.ID == workItemID {
			return index
		}
	}
	return -1
}

func executionCycle(metadata map[string]string) int {
	if len(metadata) == 0 {
		return 1
	}
	value := strings.TrimSpace(metadata["execution_cycle"])
	if value == "" {
		return 1
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 1
	}
	return parsed
}

func summarizeObservation(stdout, stderr string, err error) string {
	if text := strings.TrimSpace(stdout); text != "" {
		return compactObservation(text, maxObservationSummaryLength)
	}
	if text := strings.TrimSpace(stderr); text != "" {
		return compactObservation(text, maxObservationSummaryLength)
	}
	if err != nil {
		return err.Error()
	}
	return "Execution completed."
}

func compactObservation(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if limit <= 0 || len(text) <= limit {
		return text
	}

	cut := limit
	for cut > 0 && text[cut-1] != '\n' && text[cut-1] != ' ' && text[cut-1] != '\t' {
		cut--
	}
	if cut < limit/2 {
		cut = limit
	}

	return strings.TrimSpace(text[:cut]) + "\n...[output truncated]"
}

func toolChoiceTimeout(choice agent.ToolChoice) time.Duration {
	if choice.TimeoutMs > 0 {
		return time.Duration(choice.TimeoutMs) * time.Millisecond
	}
	return 60 * time.Second
}

func buildRuntimeContext(workspaceRoot string) agent.RuntimeContext {
	shellPath := strings.TrimSpace(os.Getenv("SHELL"))
	return agent.RuntimeContext{
		OS:            goruntime.GOOS,
		Arch:          goruntime.GOARCH,
		Shell:         shellPath,
		Path:          os.Getenv("PATH"),
		WorkspaceRoot: workspaceRoot,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneMetadata(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func stableActivityID(parts ...string) string {
	joined := strings.Join(parts, "\x00")
	sum := sha1.Sum([]byte(joined))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (a *Activities) writeADR(ctx context.Context, projectID, title, summary string, details []string) (domain.ADR, error) {
	if a.ADRWriter == nil {
		return domain.ADR{}, nil
	}
	adr, err := a.ADRWriter.WriteSummary(ctx, projectID, title, summary, details)
	if err != nil {
		return domain.ADR{}, err
	}
	return adr, a.Store.AppendADR(ctx, adr)
}
