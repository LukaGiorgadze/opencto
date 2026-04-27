package activities

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	readtool "github.com/opencto/opencto/internal/tools/read"
	"github.com/opencto/opencto/internal/tools/shell"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

type ProjectStore interface {
	Append(context.Context, domain.Event) error
	ListByProject(context.Context, string, int) ([]domain.Event, error)
	ListPending(context.Context, string) ([]domain.WorkItem, error)
	UpsertWorkItem(context.Context, domain.WorkItem) error
	GetWorkItem(context.Context, string, string) (domain.WorkItem, error)
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
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
	Edit            edittool.Executor
	Glob            globtool.Executor
	Grep            greptool.Executor
	Read            readtool.Executor
	Write           writetool.Executor
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
	Input            json.RawMessage        `json:"input,omitempty"`
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

type toolRunResult struct {
	Observation      string
	ResultCode       string
	WorkingDirectory string
	Metadata         map[string]string
}

func (a *Activities) LoadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	return a.loadContext(ctx, event)
}

func (a *Activities) loadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	var activeWorkItems []domain.WorkItem
	if a.Store != nil {
		var err error
		activeWorkItems, err = a.Store.ListPending(ctx, event.ProjectID)
		if err != nil {
			return agent.Context{}, err
		}
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

func (a *Activities) activityLogger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

func (a *Activities) logActivityStep(activity, step string, attrs ...any) {
	base := []any{
		slog.String("activity", activity),
		slog.String("step", step),
	}
	a.activityLogger().Info("runtime activity trace", append(base, attrs...)...)
}

func (a *Activities) NextAction(ctx context.Context, request NextActionRequest) (NextActionResult, error) {
	a.logActivityStep("NextAction", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("event_id", request.Event.ID),
		slog.Int("execution_cycle", request.ExecutionCycle),
		slog.Bool("force_final", request.ForceFinal),
		slog.Bool("resumed_from_pause", request.ResumedFromPause),
		slog.Bool("has_last_result", request.LastResult != nil),
		slog.Int("observation_history_len", len(request.ObservationHistory)),
	)
	if a.Engine == nil {
		a.logActivityStep("NextAction", "missing_engine")
		return NextActionResult{}, fmt.Errorf("decision engine is not configured")
	}

	projectID := strings.TrimSpace(request.ProjectID)
	event := request.Event
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	if projectID == "" {
		a.logActivityStep("NextAction", "missing_project_id", slog.String("event_project_id", event.ProjectID))
		return NextActionResult{}, fmt.Errorf("project_id is required")
	}
	event.ProjectID = projectID
	if a.Store != nil && request.ExecutionCycle <= 1 && !request.ResumedFromPause {
		a.logActivityStep("NextAction", "append_event_begin",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		if err := a.Store.Append(ctx, event); err != nil {
			a.logActivityStep("NextAction", "append_event_error",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
		a.logActivityStep("NextAction", "append_event_done",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
	}

	a.logActivityStep("NextAction", "load_context_begin",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
	)
	loaded, err := a.loadContext(ctx, event)
	if err != nil {
		a.logActivityStep("NextAction", "load_context_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "load_context_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("active_work_items", len(loaded.ActiveWorkItems)),
	)

	now := time.Now().UTC()
	decision := request.Decision
	if err := ensureNextActionDecision(&decision, projectID, event, now); err != nil {
		a.logActivityStep("NextAction", "ensure_decision_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "ensure_decision_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(decision.WorkItems)),
	)

	history := append([]agent.ExecutionFeedback(nil), request.ObservationHistory...)
	var observation *agent.ExecutionFeedback
	if request.LastResult != nil {
		a.logActivityStep("NextAction", "apply_last_result_begin",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("last_result_tool_call_id", request.LastResult.ToolCallID),
			slog.String("last_result_status", string(request.LastResult.Status)),
		)
		feedback := executionFeedback(*request.LastResult)
		observation = &feedback
		history = append(history, feedback)
		decision.ToolChoice = agent.ToolChoice{}
		if err := applyObservationToDecision(&decision, feedback, now); err != nil {
			a.logActivityStep("NextAction", "apply_last_result_error",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.String("work_item_id", feedback.WorkItemID),
				slog.String("tool_call_id", feedback.ToolCallID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
		a.logActivityStep("NextAction", "apply_last_result_done",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("work_item_id", feedback.WorkItemID),
			slog.String("tool_call_id", feedback.ToolCallID),
			slog.Int("history_len", len(history)),
		)
	}

	a.logActivityStep("NextAction", "engine_next_action_begin",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("execution_cycle", request.ExecutionCycle),
		slog.Int("history_len", len(history)),
	)
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
		a.logActivityStep("NextAction", "engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
		slog.Bool("has_tool_choice", engineOutput.ToolChoice != nil),
		slog.String("work_item_id", strings.TrimSpace(engineOutput.WorkItemID)),
	)
	if len(engineOutput.Decision.WorkItems) > 0 || !engineOutput.Decision.ToolChoice.IsZero() || strings.TrimSpace(engineOutput.Decision.ResponseMessage) != "" {
		decision = engineOutput.Decision
	}
	if err := ensureNextActionDecision(&decision, projectID, event, now); err != nil {
		a.logActivityStep("NextAction", "ensure_engine_decision_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "ensure_engine_decision_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(decision.WorkItems)),
	)

	if request.ForceFinal && engineOutput.Status == NextActionStatusTool {
		a.logActivityStep("NextAction", "force_final_override_tool_status",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		engineOutput.Status = NextActionStatusBlocked
		engineOutput.FinalAnswer = cycleLimitFinalAnswer(history)
		engineOutput.ToolChoice = nil
	}

	a.logActivityStep("NextAction", "dispatch_status",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
	)
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
	a.logActivityStep("NextAction", "prepare_tool_begin",
		slog.Int("execution_cycle", cycle),
		slog.Bool("has_observation", observation != nil),
		slog.Bool("has_tool_choice", output.ToolChoice != nil),
		slog.String("output_work_item_id", strings.TrimSpace(output.WorkItemID)),
	)
	if output.ToolChoice == nil {
		a.logActivityStep("NextAction", "prepare_tool_missing_choice")
		return NextActionResult{}, fmt.Errorf("%w: tool status requires exactly one tool choice", agent.ErrInvalidToolChoice)
	}

	choice := *output.ToolChoice
	workItemID := nextActionToolWorkItemID(output, choice, decision)
	if strings.TrimSpace(workItemID) == "" {
		a.logActivityStep("NextAction", "prepare_tool_missing_work_item_id",
			slog.String("tool_call_id", choice.ToolCallID),
		)
		return NextActionResult{}, fmt.Errorf("%w: tool choice is missing work item id", agent.ErrInvalidToolChoice)
	}
	a.logActivityStep("NextAction", "prepare_tool_work_item_resolved",
		slog.String("work_item_id", workItemID),
		slog.String("tool_type", string(choice.Type)),
		slog.String("tool_call_id", choice.ToolCallID),
	)
	if err := ensureToolWorkItem(&decision, workItemID, now); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_ensure_work_item_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	if err := completePreviousWorkItemForNextAction(&decision, workItemID, observation, now); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_complete_previous_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "prepare_tool_work_items_ready",
		slog.String("work_item_id", workItemID),
		slog.Int("work_items", len(decision.WorkItems)),
	)

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

	a.logActivityStep("NextAction", "prepare_tool_persist_decision_begin",
		slog.String("work_item_id", workItemID),
		slog.String("tool_call_id", choice.ToolCallID),
	)
	if err := a.persistDecision(ctx, decision); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_persist_decision_error",
			slog.String("work_item_id", workItemID),
			slog.String("tool_call_id", choice.ToolCallID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "prepare_tool_done",
		slog.String("work_item_id", workItemID),
		slog.String("tool_call_id", choice.ToolCallID),
		slog.String("tool_type", string(choice.Type)),
	)

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
	a.logActivityStep("NextAction", "finish_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
		slog.Bool("has_observation", observation != nil),
	)
	message := strings.TrimSpace(output.FinalAnswer)
	if message == "" {
		message = strings.TrimSpace(output.Decision.ResponseMessage)
	}
	if message == "" {
		a.logActivityStep("NextAction", "finish_missing_final_answer",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", output.Status),
		)
		return NextActionResult{}, fmt.Errorf("%w: terminal next action is missing final answer", agent.ErrInvalidNextAction)
	}

	decision.ToolChoice = agent.ToolChoice{}
	decision.ResponseMessage = message
	markFinalDecisionWorkItems(&decision, terminalWorkItemStatus(output.Status), observation, now)

	a.logActivityStep("NextAction", "finish_persist_decision_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
	)
	if err := a.persistDecision(ctx, decision); err != nil {
		a.logActivityStep("NextAction", "finish_persist_decision_error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	if output.Status != NextActionStatusIgnored {
		if a.Reporter != nil {
			a.logActivityStep("NextAction", "finish_report_begin",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("status", output.Status),
			)
			if err := a.Reporter.Report(ctx, event, message); err != nil {
				a.logActivityStep("NextAction", "finish_report_error",
					slog.String("project_id", event.ProjectID),
					slog.String("event_id", event.ID),
					slog.String("error", err.Error()),
				)
				return NextActionResult{}, err
			}
			a.logActivityStep("NextAction", "finish_report_done",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("status", output.Status),
			)
		}
	}
	a.logActivityStep("NextAction", "finish_done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
	)

	return NextActionResult{
		Decision:      decision,
		Observation:   observation,
		FinalAnswer:   message,
		Status:        output.Status,
		AssistantText: output.AssistantText,
	}, nil
}

func (a *Activities) persistDecision(ctx context.Context, decision agent.DecisionOutput) error {
	if a.Store == nil {
		a.logActivityStep("NextAction", "persist_decision_skip_no_store",
			slog.Int("work_items", len(decision.WorkItems)),
		)
		return nil
	}
	for _, item := range decision.WorkItems {
		if item.ID == "" {
			a.logActivityStep("NextAction", "persist_decision_skip_empty_work_item_id")
			continue
		}
		a.logActivityStep("NextAction", "persist_decision_upsert_work_item_begin",
			slog.String("work_item_id", item.ID),
			slog.String("status", string(item.Status)),
		)
		if err := a.Store.UpsertWorkItem(ctx, item); err != nil {
			a.logActivityStep("NextAction", "persist_decision_upsert_work_item_error",
				slog.String("work_item_id", item.ID),
				slog.String("status", string(item.Status)),
				slog.String("error", err.Error()),
			)
			return err
		}
		a.logActivityStep("NextAction", "persist_decision_upsert_work_item_done",
			slog.String("work_item_id", item.ID),
			slog.String("status", string(item.Status)),
		)
	}
	return nil
}

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	a.logActivityStep("ExecuteTool", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("tool_call_id", strings.TrimSpace(request.ToolChoice.ToolCallID)),
		slog.String("command", request.ToolChoice.Command),
		slog.Any("args", request.ToolChoice.Args),
		slog.Int("input_bytes", len(request.ToolChoice.Input)),
	)
	execution, err := newToolExecutionContext(request)
	if err != nil {
		a.logActivityStep("ExecuteTool", "new_execution_context_error",
			slog.String("project_id", strings.TrimSpace(request.ProjectID)),
			slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
			slog.String("error", err.Error()),
		)
		return ExecuteToolResult{}, err
	}
	a.logActivityStep("ExecuteTool", "new_execution_context_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.Int("cycle", execution.Cycle),
		slog.Duration("timeout", execution.Timeout),
		slog.Any("fallback_candidates", execution.FallbackCandidates),
	)

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
	if a.Store != nil {
		a.logActivityStep("ExecuteTool", "upsert_execution_attempt_begin",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("attempt_id", attempt.ID),
		)
		if err := a.Store.UpsertExecutionAttempt(ctx, attempt); err != nil {
			a.logActivityStep("ExecuteTool", "upsert_execution_attempt_error",
				slog.String("project_id", execution.ProjectID),
				slog.String("work_item_id", execution.WorkItemID),
				slog.String("tool_call_id", execution.ToolCallID),
				slog.String("attempt_id", attempt.ID),
				slog.String("error", err.Error()),
			)
			return ExecuteToolResult{}, err
		}
		a.logActivityStep("ExecuteTool", "upsert_execution_attempt_done",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("attempt_id", attempt.ID),
		)
	}

	a.logActivityStep("ExecuteTool", "tool_run_begin",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
	)
	toolResult, runErr := a.runChosenTool(ctx, request.ToolChoice, execution)
	a.logActivityStep("ExecuteTool", "tool_run_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("result_code", toolResult.ResultCode),
		slog.Bool("tool_error", runErr != nil),
	)

	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	metadata := map[string]string{
		"started_at":   execution.StartedAt.UTC().Format(time.RFC3339Nano),
		"completed_at": completedAt.UTC().Format(time.RFC3339Nano),
		"tool_call_id": execution.ToolCallID,
	}
	for key, value := range request.ToolChoice.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	for key, value := range toolResult.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if request.ToolChoice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(request.ToolChoice.TimeoutMs)
	}
	if request.ToolChoice.Destructive {
		metadata["destructive"] = "true"
	}

	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   firstNonEmpty(toolResult.WorkingDirectory, request.ToolChoice.WorkingDir),
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		OutputSummary:      toolResult.Observation,
		ResultCode:         firstNonEmpty(toolResult.ResultCode, "0"),
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}

	var errorMessage string
	if runErr != nil {
		a.logActivityStep("ExecuteTool", "tool_run_result_error",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("error", runErr.Error()),
		)
		attempt.Status = domain.ExecutionStatusFailed
		attempt.OutputSummary = firstNonEmpty(toolResult.Observation, "Tool execution failed.")
		invocation.ErrorDetails = runErr.Error()
		invocation.OutputSummary = attempt.OutputSummary
		if invocation.ResultCode == "" || invocation.ResultCode == "0" {
			invocation.ResultCode = "1"
		}
		errorMessage = runErr.Error()
	} else {
		a.logActivityStep("ExecuteTool", "tool_run_result_success",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
		)
		attempt.Status = domain.ExecutionStatusSucceeded
		attempt.OutputSummary = firstNonEmpty(toolResult.Observation, "Execution completed.")
		invocation.OutputSummary = attempt.OutputSummary
	}

	if a.Store != nil {
		a.logActivityStep("ExecuteTool", "persist_execution_records_begin",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("attempt_status", string(attempt.Status)),
		)
		if persistErr := a.Store.UpsertExecutionAttempt(ctx, attempt); persistErr != nil {
			a.logActivityStep("ExecuteTool", "persist_execution_attempt_error",
				slog.String("project_id", execution.ProjectID),
				slog.String("work_item_id", execution.WorkItemID),
				slog.String("tool_call_id", execution.ToolCallID),
				slog.String("error", persistErr.Error()),
			)
			return ExecuteToolResult{}, persistErr
		}
		if persistErr := a.Store.UpsertToolInvocation(ctx, invocation); persistErr != nil {
			a.logActivityStep("ExecuteTool", "persist_tool_invocation_error",
				slog.String("project_id", execution.ProjectID),
				slog.String("work_item_id", execution.WorkItemID),
				slog.String("tool_call_id", execution.ToolCallID),
				slog.String("error", persistErr.Error()),
			)
			return ExecuteToolResult{}, persistErr
		}
		a.logActivityStep("ExecuteTool", "persist_execution_records_done",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("attempt_status", string(attempt.Status)),
		)
	}
	a.logActivityStep("ExecuteTool", "done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("attempt_status", string(attempt.Status)),
		slog.String("result_code", invocation.ResultCode),
	)

	return ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           attempt.Status,
		RequestedAction:  request.ToolChoice.Intent,
		Command:          request.ToolChoice.Command,
		Args:             request.ToolChoice.Args,
		Input:            cloneRawMessage(request.ToolChoice.Input),
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       invocation.ResultCode,
		Metadata:         invocation.Metadata,
	}, nil
}

func (a *Activities) runChosenTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeShell:
		return a.runShellTool(ctx, choice, execution)
	case domain.ToolTypeRead:
		return a.runReadTool(ctx, choice)
	case domain.ToolTypeEdit:
		return a.runEditTool(ctx, choice)
	case domain.ToolTypeWrite:
		return a.runWriteTool(ctx, choice, execution)
	case domain.ToolTypeGlob:
		return a.runGlobTool(ctx, choice, execution)
	case domain.ToolTypeGrep:
		return a.runGrepTool(ctx, choice, execution)
	default:
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("unsupported tool type %q", choice.Type)
	}
}

func (a *Activities) runShellTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	if a.Shell == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("shell executor is not configured")
	}
	result, err := a.Shell.Run(ctx, shell.Request{
		ProjectID:          execution.ProjectID,
		Intent:             choice.Intent,
		Command:            choice.Command,
		Args:               choice.Args,
		WorkingDir:         choice.WorkingDir,
		WorkspaceRoot:      a.WorkspaceRoot,
		Timeout:            execution.Timeout,
		FallbackCandidates: execution.FallbackCandidates,
	})
	metadata := map[string]string{
		"shell_exit_status": strconv.Itoa(result.ExitCode),
	}
	if !result.StartedAt.IsZero() {
		metadata["tool_started_at"] = result.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !result.CompletedAt.IsZero() {
		metadata["tool_completed_at"] = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      fullObservation(result.Stdout, result.Stderr, err),
		ResultCode:       strconv.Itoa(result.ExitCode),
		WorkingDirectory: choice.WorkingDir,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runReadTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req readtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Read
	if executor == nil {
		executor = readtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: readObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"file_path":   result.FilePath,
			"lines_read":  strconv.Itoa(result.LinesRead),
			"total_lines": strconv.Itoa(result.TotalLines),
			"bytes_read":  strconv.Itoa(result.BytesRead),
			"truncated":   strconv.FormatBool(result.Truncated),
		},
	}, err
}

func (a *Activities) runEditTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req edittool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Edit
	if executor == nil {
		executor = edittool.NewTool()
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: editObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"file_path":     result.FilePath,
			"replacements":  strconv.Itoa(result.Replacements),
			"bytes_written": strconv.Itoa(result.BytesWritten),
		},
	}, err
}

func (a *Activities) runWriteTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req writetool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.WorkspaceRoot = a.WorkspaceRoot

	executor := a.Write
	if executor == nil {
		executor = writetool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"file_path":     result.FilePath,
		"bytes_written": strconv.Itoa(result.BytesWritten),
		"overwritten":   strconv.FormatBool(result.Overwritten),
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation: writeObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runGlobTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req globtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.Path = resolveRelativeToolPath(req.Path, firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot))
	if strings.TrimSpace(req.Path) == "" {
		req.Path = firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot)
	}
	req.Timeout = execution.Timeout

	executor := a.Glob
	if executor == nil {
		executor = globtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"pattern":     result.Pattern,
		"path":        result.Root,
		"match_count": strconv.Itoa(len(result.Matches)),
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation: globObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runGrepTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req greptool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.WorkingDir = choice.WorkingDir
	req.WorkspaceRoot = a.WorkspaceRoot
	req.Timeout = execution.Timeout
	req.FallbackCandidates = execution.FallbackCandidates

	executor := a.Grep
	if executor == nil {
		executor = greptool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	code := strconv.Itoa(result.ExitCode)
	if err != nil && result.ExitCode == 0 && result.StartedAt.IsZero() {
		code = "1"
	}
	metadata := map[string]string{
		"grep_exit_status": strconv.Itoa(result.ExitCode),
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      grepObservation(result, err),
		ResultCode:       code,
		WorkingDirectory: choice.WorkingDir,
		Metadata:         metadata,
	}, err
}

func decodeChoiceInput(choice agent.ToolChoice, target any) error {
	if len(strings.TrimSpace(string(choice.Input))) == 0 {
		return fmt.Errorf("%s tool input is required", choice.Type)
	}
	decoder := json.NewDecoder(strings.NewReader(string(choice.Input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return fmt.Errorf("tool input contains multiple JSON values")
	case io.EOF:
		return nil
	default:
		return err
	}
}

func resultCodeForError(err error) string {
	if err != nil {
		return "1"
	}
	return "0"
}

func readObservation(result readtool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "file: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		result.FilePath,
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	if result.Content != "" {
		builder.WriteString("\ncontent:\n")
		builder.WriteString(result.Content)
	}
	return builder.String()
}

func editObservation(result edittool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("edited: %s\nreplacements: %d\nbytes_written: %d", result.FilePath, result.Replacements, result.BytesWritten)
}

func writeObservation(result writetool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("wrote: %s\nbytes_written: %d\noverwritten: %t", result.FilePath, result.BytesWritten, result.Overwritten)
}

func globObservation(result globtool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	if len(result.Matches) == 0 {
		return fmt.Sprintf("pattern: %s\npath: %s\nmatches: 0", result.Pattern, result.Root)
	}
	return fmt.Sprintf("pattern: %s\npath: %s\nmatches: %d\n%s",
		result.Pattern,
		result.Root,
		len(result.Matches),
		strings.Join(result.Matches, "\n"),
	)
}

func grepObservation(result greptool.Result, err error) string {
	if err != nil {
		return fullObservation(result.Stdout, result.Stderr, err)
	}
	if strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" && result.ExitCode == 1 {
		return "No matches found."
	}
	return fullObservation(result.Stdout, result.Stderr, nil)
}

func resolveRelativeToolPath(path, base string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(base) == "" {
		return path
	}
	return filepath.Join(base, path)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
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
		Input:           cloneRawMessage(result.Input),
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

func cycleLimitFinalAnswer(history []agent.ExecutionFeedback) string {
	if len(history) == 0 {
		return "Stopped after reaching the execution cycle limit before a final answer was produced."
	}

	var builder strings.Builder
	builder.WriteString("Stopped after reaching the execution cycle limit. Full execution history:")
	for _, feedback := range history {
		builder.WriteString("\n\n")
		builder.WriteString(fmt.Sprintf("cycle: %d", feedback.Cycle))
		if feedback.Tool != "" {
			builder.WriteString(fmt.Sprintf("\ntool: %s", feedback.Tool))
		}
		if feedback.Status != "" {
			builder.WriteString(fmt.Sprintf("\nstatus: %s", feedback.Status))
		}
		if feedback.RequestedAction != "" {
			builder.WriteString(fmt.Sprintf("\nrequested_action: %s", feedback.RequestedAction))
		}
		if feedback.Command != "" {
			builder.WriteString(fmt.Sprintf("\ncommand: %s", feedback.Command))
		}
		if len(feedback.Args) > 0 {
			builder.WriteString(fmt.Sprintf("\nargs: %s", strings.Join(feedback.Args, " ")))
		}
		if text := strings.TrimSpace(feedback.Observation); text != "" {
			builder.WriteString("\nobservation:\n")
			builder.WriteString(text)
		}
		if text := strings.TrimSpace(feedback.Error); text != "" {
			builder.WriteString("\nerror:\n")
			builder.WriteString(text)
		}
	}
	return builder.String()
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

func fullObservation(stdout, stderr string, err error) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	var parts []string
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if err != nil {
		parts = append(parts, "error:\n"+err.Error())
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return "Execution completed."
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
