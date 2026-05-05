package activities

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	toolregistry "github.com/opencto/opencto/internal/tools"
	browsertool "github.com/opencto/opencto/internal/tools/browser"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	readtool "github.com/opencto/opencto/internal/tools/read"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	writetool "github.com/opencto/opencto/internal/tools/write"
	"github.com/opencto/opencto/internal/workspace"
)

type ProjectStore interface {
	Append(context.Context, domain.Event) error
	ListPending(context.Context, string) ([]domain.WorkItem, error)
	UpsertWorkItem(context.Context, domain.WorkItem) error
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
}

type Reporter interface {
	Report(context.Context, domain.Event, string) error
}

type TypingReporter interface {
	NotifyTyping(context.Context, domain.Event) error
}

type Activities struct {
	Store         ProjectStore
	Engine        agent.Engine
	Shell         shelltool.Executor
	Browser       browsertool.Executor
	Edit          edittool.Executor
	Glob          globtool.Executor
	Grep          greptool.Executor
	Read          readtool.Executor
	Skill         skilltool.Executor
	Write         writetool.Executor
	Reporter      Reporter
	Project       domain.Project
	WorkspaceRoot string
	OpenCTORoot   string
	SkillsRoot    string
	StateDir      string
	Logger        *slog.Logger
}

type NextActionRequest struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	AdditionalEvents   []domain.Event            `json:"additional_events,omitempty"`
	NextAction         agent.NextAction          `json:"next_action"`
	LastResult         *ExecuteToolResult        `json:"last_result,omitempty"`
	ObservationHistory []agent.ExecutionFeedback `json:"observation_history,omitempty"`
	Processes          []domain.ProcessReference `json:"processes,omitempty"`
	ExecutionCycle     int                       `json:"execution_cycle"`
	ForceFinal         bool                      `json:"force_final,omitempty"`
	ResumedFromPause   bool                      `json:"resumed_from_pause,omitempty"`
	Completion         *TaskCompletionRequest    `json:"completion,omitempty"`
}

type NextActionResult struct {
	NextAction  agent.NextAction          `json:"next_action"`
	ToolChoice  *agent.ToolChoice         `json:"tool_choice,omitempty"`
	WorkItemID  string                    `json:"work_item_id,omitempty"`
	Observation *agent.ExecutionFeedback  `json:"observation,omitempty"`
	Status      string                    `json:"status"`
	Processes   []domain.ProcessReference `json:"processes,omitempty"`
}

type ExecuteToolRequest struct {
	ProjectID  string           `json:"project_id"`
	WorkItemID string           `json:"work_item_id"`
	ToolChoice agent.ToolChoice `json:"tool_choice"`
}

type TaskCompletionRequest struct {
	Status    string                    `json:"status"`
	Processes []domain.ProcessReference `json:"processes,omitempty"`
}

type ReportResponseRequest struct {
	Event   domain.Event `json:"event"`
	Message string       `json:"message"`
}

type ResponseSessionRequest struct {
	ProjectID              string       `json:"project_id"`
	Event                  domain.Event `json:"event"`
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds,omitempty"`
	MaxDurationSeconds     int          `json:"max_duration_seconds,omitempty"`
}

type ExecuteToolResult struct {
	Cycle            int                       `json:"cycle"`
	WorkItemID       string                    `json:"work_item_id,omitempty"`
	ToolCallID       string                    `json:"tool_call_id,omitempty"`
	Tool             domain.ToolType           `json:"tool,omitempty"`
	Status           domain.ExecutionStatus    `json:"status"`
	RequestedAction  string                    `json:"requested_action,omitempty"`
	Command          string                    `json:"command,omitempty"`
	Args             []string                  `json:"args,omitempty"`
	Input            json.RawMessage           `json:"input,omitempty"`
	Observation      string                    `json:"observation,omitempty"`
	Error            string                    `json:"error,omitempty"`
	WorkingDirectory string                    `json:"working_directory,omitempty"`
	ResultCode       string                    `json:"result_code,omitempty"`
	Metadata         map[string]string         `json:"metadata,omitempty"`
	Processes        []domain.ProcessReference `json:"processes,omitempty"`
}

const (
	NextActionStatusTool      = "tool"
	NextActionStatusCompleted = "completed"
	NextActionStatusBlocked   = "blocked"
	NextActionStatusFailed    = "failed"
	NextActionStatusIgnored   = "ignored"

	defaultResponseSessionRefresh = 4 * time.Second
	defaultResponseSessionMaxAge  = 30 * time.Minute
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
	availableSkills, err := skillcatalog.Discover(ctx, a.skillsRoots()...)
	if err != nil {
		return agent.Context{}, err
	}
	return agent.Context{
		Event:           event,
		Project:         project,
		ActiveWorkItems: activeWorkItems,
		Skills:          availableSkills,
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

func (a *Activities) ResponseSession(ctx context.Context, request ResponseSessionRequest) error {
	event := request.Event
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	a.logActivityStep("ResponseSession", "start",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("channel_type", string(event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(event.ChannelID)),
	)
	reporter, ok := a.Reporter.(TypingReporter)
	if !ok || reporter == nil {
		a.logActivityStep("ResponseSession", "skip_no_indicator_reporter",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		return nil
	}

	interval := defaultResponseSessionRefresh
	if request.RefreshIntervalSeconds > 0 {
		interval = time.Duration(request.RefreshIntervalSeconds) * time.Second
	}
	maxAge := defaultResponseSessionMaxAge
	if request.MaxDurationSeconds > 0 {
		maxAge = time.Duration(request.MaxDurationSeconds) * time.Second
	}

	refresh := func() {
		recordResponseSessionHeartbeat(ctx, map[string]string{
			"project_id":   projectID,
			"event_id":     event.ID,
			"channel_type": string(event.ChannelType),
		})
		if err := reporter.NotifyTyping(ctx, event); err != nil && ctx.Err() == nil {
			a.logActivityStep("ResponseSession", "indicator_error",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.NewTimer(maxAge)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logActivityStep("ResponseSession", "canceled",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
			)
			return nil
		case <-deadline.C:
			a.logActivityStep("ResponseSession", "expired",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.Duration("max_age", maxAge),
			)
			return nil
		case <-ticker.C:
			refresh()
		}
	}
}

func (a *Activities) ReportResponse(ctx context.Context, request ReportResponseRequest) error {
	message := strings.TrimSpace(request.Message)
	if message == "" || a.Reporter == nil {
		return nil
	}
	a.logActivityStep("ReportResponse", "start",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
		slog.String("channel_type", string(request.Event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(request.Event.ChannelID)),
	)
	if err := a.Reporter.Report(ctx, request.Event, message); err != nil {
		a.logActivityStep("ReportResponse", "error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	a.logActivityStep("ReportResponse", "done",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
	)
	return nil
}

func recordResponseSessionHeartbeat(ctx context.Context, details any) {
	defer func() {
		_ = recover()
	}()
	activity.RecordHeartbeat(ctx, details)
}

func (a *Activities) NextAction(ctx context.Context, request NextActionRequest) (NextActionResult, error) {
	if request.Completion != nil {
		return a.completeTask(ctx, request.ProjectID, request.Event, agent.NextAction{}, *request.Completion, false, false)
	}
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
		return NextActionResult{}, fmt.Errorf("next action engine is not configured")
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
	loaded.AdditionalEvents = append([]domain.Event(nil), request.AdditionalEvents...)
	a.logActivityStep("NextAction", "load_context_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("active_work_items", len(loaded.ActiveWorkItems)),
	)

	now := time.Now().UTC()
	nextAction := request.NextAction
	if err := ensureNextAction(&nextAction, projectID, event, now); err != nil {
		a.logActivityStep("NextAction", "ensure_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "ensure_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
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
		nextAction.ToolChoice = agent.ToolChoice{}
		if err := applyObservationToNextAction(&nextAction, feedback, now); err != nil {
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
		NextAction:         nextAction,
		Runtime:            buildRuntimeContext(a.WorkspaceRoot, a.OpenCTORoot),
		ExecutionCycle:     request.ExecutionCycle,
		ForceFinal:         request.ForceFinal,
		ResumedFromPause:   request.ResumedFromPause,
		LastObservation:    observation,
		ObservationHistory: history,
		ChannelType:        event.ChannelType,
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
	if len(engineOutput.NextAction.WorkItems) > 0 || !engineOutput.NextAction.ToolChoice.IsZero() || strings.TrimSpace(engineOutput.NextAction.ResponseMessage) != "" {
		nextAction = engineOutput.NextAction
	}
	if err := ensureNextAction(&nextAction, projectID, event, now); err != nil {
		a.logActivityStep("NextAction", "ensure_engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "ensure_engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	if request.ForceFinal {
		a.logActivityStep("NextAction", "force_final_override_status",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		engineOutput.Status = NextActionStatusBlocked
		engineOutput.ToolChoice = nil
		if strings.TrimSpace(engineOutput.NextAction.ResponseMessage) == "" {
			engineOutput.NextAction.ResponseMessage = cycleLimitResponseMessage(history)
		}
	}

	a.logActivityStep("NextAction", "dispatch_status",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
	)
	switch engineOutput.Status {
	case NextActionStatusTool:
		return a.prepareToolNextAction(ctx, nextAction, observation, engineOutput, request.ExecutionCycle, now)
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return a.finishNextAction(ctx, event, nextAction, observation, engineOutput, request.Processes, now)
	default:
		return NextActionResult{}, fmt.Errorf("unsupported next action status %q", engineOutput.Status)
	}
}

func (a *Activities) prepareToolNextAction(ctx context.Context, nextAction agent.NextAction, observation *agent.ExecutionFeedback, output agent.NextActionOutput, cycle int, now time.Time) (NextActionResult, error) {
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
	workItemID := nextActionToolWorkItemID(nextAction, observation)
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
	if err := ensureToolWorkItem(&nextAction, workItemID, now); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_ensure_work_item_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	if err := completePreviousWorkItemForNextAction(&nextAction, workItemID, observation, now); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_complete_previous_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep("NextAction", "prepare_tool_work_items_ready",
		slog.String("work_item_id", workItemID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	index := nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID)
	if index >= 0 {
		nextAction.WorkItems[index].Status = domain.WorkItemStatusRunning
		nextAction.WorkItems[index].UpdatedAt = now
	}

	assistantText := strings.TrimSpace(output.AssistantText)
	if assistantText == "" {
		assistantText = strings.TrimSpace(choice.Intent)
	}
	ensureToolChoiceMetadata(&choice, workItemID, cycle, assistantText)
	nextAction.ToolChoice = choice
	nextAction.ResponseMessage = ""

	a.logActivityStep("NextAction", "prepare_tool_persist_next_action_begin",
		slog.String("work_item_id", workItemID),
		slog.String("tool_call_id", choice.ToolCallID),
	)
	if err := a.persistNextAction(ctx, nextAction); err != nil {
		a.logActivityStep("NextAction", "prepare_tool_persist_next_action_error",
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
		NextAction:  nextAction,
		ToolChoice:  &choice,
		WorkItemID:  workItemID,
		Observation: observation,
		Status:      NextActionStatusTool,
	}, nil
}

func (a *Activities) finishNextAction(ctx context.Context, event domain.Event, nextAction agent.NextAction, observation *agent.ExecutionFeedback, output agent.NextActionOutput, processes []domain.ProcessReference, now time.Time) (NextActionResult, error) {
	a.logActivityStep("NextAction", "finish_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
		slog.Bool("has_observation", observation != nil),
	)
	message := strings.TrimSpace(output.NextAction.ResponseMessage)
	if message == "" {
		a.logActivityStep("NextAction", "finish_missing_response_message",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", output.Status),
		)
		return NextActionResult{}, fmt.Errorf("%w: terminal next action is missing response message", agent.ErrInvalidNextAction)
	}

	nextAction.ToolChoice = agent.ToolChoice{}
	nextAction.ResponseMessage = message
	markFinalNextActionWorkItems(&nextAction, terminalWorkItemStatus(output.Status), observation, now)
	result, err := a.completeTask(ctx, event.ProjectID, event, nextAction, TaskCompletionRequest{
		Status:    output.Status,
		Processes: processes,
	}, true, false)
	if err != nil {
		return NextActionResult{}, err
	}
	result.Observation = observation
	a.logActivityStep("NextAction", "finish_done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", result.Status),
	)
	return result, nil
}

func (a *Activities) completeTask(ctx context.Context, projectID string, event domain.Event, nextAction agent.NextAction, request TaskCompletionRequest, persist bool, report bool) (NextActionResult, error) {
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(projectID)
	}
	status := strings.TrimSpace(request.Status)
	if status == "" {
		status = NextActionStatusFailed
	}
	a.logActivityStep("NextAction", "complete_task_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", status),
		slog.Int("processes", len(request.Processes)),
		slog.Bool("persist", persist),
		slog.Bool("report", report),
	)

	cleaned, stopped, cleanupFailed := a.cleanupTaskProcesses(ctx, event.ProjectID, request.Processes)
	if cleanupFailed {
		status = NextActionStatusFailed
		markFinalNextActionWorkItems(&nextAction, domain.WorkItemStatusFailed, nil, time.Now().UTC())
	}
	if stopped || cleanupFailed {
		nextAction.ResponseMessage = appendProcessCleanupNotice(nextAction.ResponseMessage, cleaned, stopped, cleanupFailed)
	}

	message := strings.TrimSpace(nextAction.ResponseMessage)
	if persist {
		if err := a.persistNextAction(ctx, nextAction); err != nil {
			a.logActivityStep("NextAction", "complete_task_persist_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
	}
	if report && status != NextActionStatusIgnored && a.Reporter != nil && message != "" {
		a.logActivityStep("NextAction", "complete_task_report_begin",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
		if err := a.Reporter.Report(ctx, event, message); err != nil {
			a.logActivityStep("NextAction", "complete_task_report_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
		a.logActivityStep("NextAction", "complete_task_report_done",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
	}
	a.logActivityStep("NextAction", "complete_task_done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", status),
		slog.Int("processes", len(cleaned)),
	)
	return NextActionResult{
		NextAction: nextAction,
		Status:     status,
		Processes:  cleaned,
	}, nil
}

func (a *Activities) cleanupTaskProcesses(ctx context.Context, projectID string, processes []domain.ProcessReference) ([]domain.ProcessReference, bool, bool) {
	if len(processes) == 0 {
		return nil, false, false
	}
	updated := append([]domain.ProcessReference(nil), processes...)
	failed := false
	stoppedAny := false
	manager := shelltool.NewProcessManager(a.activityLogger())
	for index := range updated {
		process := &updated[index]
		if process.Scope == domain.ProcessScopeProject || process.Status != domain.ProcessStatusRunning {
			continue
		}
		stopped, err := manager.Stop(ctx, a.runtimeStateDir(), process.ID)
		if err != nil {
			failed = true
			continue
		}
		if stopped.Status == domain.ProcessStatusRunning {
			failed = true
			continue
		}
		process.Status = stopped.Status
		stoppedAny = true
	}
	return updated, stoppedAny, failed
}

func appendProcessCleanupNotice(message string, processes []domain.ProcessReference, stopped bool, failed bool) string {
	var notes []string
	if stopped {
		notes = append(notes, "OpenCTO stopped stop-on-finish background process(es) at task completion: "+processRefs(processes, false))
	}
	if failed {
		notes = append(notes, "OpenCTO could not stop one or more stop-on-finish background process(es); they may still be running: "+processRefs(processes, true))
	}
	note := strings.Join(notes, " ")
	if note == "" {
		return strings.TrimSpace(message)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return note
	}
	return message + " · " + note
}

func processRefs(processes []domain.ProcessReference, running bool) string {
	var refs []string
	for _, process := range processes {
		if process.Scope == domain.ProcessScopeProject {
			continue
		}
		if running && process.Status != domain.ProcessStatusRunning {
			continue
		}
		if !running && process.Status == domain.ProcessStatusRunning {
			continue
		}
		label := strings.TrimSpace(process.Description)
		if label == "" {
			label = process.ID
		}
		if process.ID != "" && label != process.ID {
			label += " (" + process.ID + ")"
		}
		refs = append(refs, label)
		if len(refs) == 3 {
			break
		}
	}
	if len(refs) == 0 {
		return "unknown"
	}
	return strings.Join(refs, ", ")
}

func (a *Activities) persistNextAction(ctx context.Context, nextAction agent.NextAction) error {
	if a.Store == nil {
		a.logActivityStep("NextAction", "persist_next_action_skip_no_store",
			slog.Int("work_items", len(nextAction.WorkItems)),
		)
		return nil
	}
	for _, item := range nextAction.WorkItems {
		if item.ID == "" {
			a.logActivityStep("NextAction", "persist_next_action_skip_empty_work_item_id")
			continue
		}
		a.logActivityStep("NextAction", "persist_next_action_upsert_work_item_begin",
			slog.String("work_item_id", item.ID),
			slog.String("status", string(item.Status)),
		)
		if err := a.Store.UpsertWorkItem(ctx, item); err != nil {
			a.logActivityStep("NextAction", "persist_next_action_upsert_work_item_error",
				slog.String("work_item_id", item.ID),
				slog.String("status", string(item.Status)),
				slog.String("error", err.Error()),
			)
			return err
		}
		a.logActivityStep("NextAction", "persist_next_action_upsert_work_item_done",
			slog.String("work_item_id", item.ID),
			slog.String("status", string(item.Status)),
		)
	}
	return nil
}

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	if request.ToolChoice.Type == domain.ToolTypeShell && request.ToolChoice.RunMode == domain.ToolRunModeStartBackground {
		return a.startShellProcess(ctx, request)
	}
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

func (a *Activities) startShellProcess(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, err
	}
	choice := request.ToolChoice
	if choice.Type != domain.ToolTypeShell {
		return ExecuteToolResult{}, fmt.Errorf("start background process requires shell tool, got %q", choice.Type)
	}
	processScope := toolProcessScope(choice.ProcessScope)
	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       choice.Type,
		Summary:    choice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
			"run_mode":        string(domain.ToolRunModeStartBackground),
			"process_scope":   string(processScope),
		},
	}
	if a.Store != nil {
		if err := a.Store.UpsertExecutionAttempt(ctx, attempt); err != nil {
			return ExecuteToolResult{}, err
		}
	}
	processID := stableActivityID("managed-process", execution.ProjectID, execution.WorkItemID, execution.ToolCallID)
	stateDir := a.runtimeStateDir()
	manager := shelltool.NewProcessManager(a.activityLogger())
	process, runErr := manager.Start(ctx, shelltool.StartProcessRequest{
		ProcessID:    processID,
		ProjectID:    execution.ProjectID,
		WorkItemID:   execution.WorkItemID,
		ToolCallID:   execution.ToolCallID,
		Intent:       choice.Intent,
		ProcessScope: processScope,
		Command:      choice.Command,
		Args:         choice.Args,
		WorkingDir:   resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		StateDir:     stateDir,
		Timeout:      execution.Timeout,
		Environment:  workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
	})
	metadata := map[string]string{
		"tool_call_id":                  execution.ToolCallID,
		"work_item_id":                  execution.WorkItemID,
		"execution_cycle":               strconv.Itoa(execution.Cycle),
		"run_mode":                      string(domain.ToolRunModeStartBackground),
		"idempotency":                   string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":                 string(processScope),
		"process_id":                    processID,
		"possible_long_running_process": "true",
	}
	if choice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
	if process.PID > 0 {
		metadata["pid"] = strconv.Itoa(process.PID)
	}
	if process.PGID > 0 {
		metadata["pgid"] = strconv.Itoa(process.PGID)
	}
	if process.StdoutLogPath != "" {
		metadata["stdout_log_path"] = process.StdoutLogPath
	}
	if process.StderrLogPath != "" {
		metadata["stderr_log_path"] = process.StderrLogPath
	}
	status := domain.ExecutionStatusSucceeded
	resultCode := "0"
	observation := processStartObservation(process)
	var errorMessage string
	if runErr != nil {
		status = domain.ExecutionStatusFailed
		resultCode = "1"
		errorMessage = runErr.Error()
		observation = backgroundStartFailureObservation(ctx, manager, stateDir, process, runErr)
	}
	completedAt := time.Now().UTC()
	attempt.Status = status
	attempt.OutputSummary = observation
	attempt.CompletedAt = &completedAt
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    choice.Intent,
		ChosenTool:         choice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   process.WorkingDirectory,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       choice.InputSummary,
		OutputSummary:      observation,
		ResultCode:         resultCode,
		ErrorDetails:       errorMessage,
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}
	if a.Store != nil {
		if err := a.Store.UpsertExecutionAttempt(ctx, attempt); err != nil {
			return ExecuteToolResult{}, err
		}
		if err := a.Store.UpsertToolInvocation(ctx, invocation); err != nil {
			return ExecuteToolResult{}, err
		}
	}
	processes := []domain.ProcessReference(nil)
	if status == domain.ExecutionStatusSucceeded && strings.TrimSpace(process.ID) != "" {
		processes = []domain.ProcessReference{{
			ID:          process.ID,
			Description: firstNonEmpty(choice.Intent, choice.Command),
			Status:      process.Status,
			Scope:       processScope,
		}}
	}
	return ExecuteToolResult{
		Cycle:            execution.Cycle,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             choice.Type,
		Status:           status,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Input:            cloneRawMessage(choice.Input),
		Observation:      observation,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       resultCode,
		Metadata:         metadata,
		Processes:        processes,
	}, nil
}

func (a *Activities) runChosenTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeShell:
		return a.runShellTool(ctx, choice, execution)
	case domain.ToolTypeBrowser:
		return a.runBrowserTool(ctx, choice, execution)
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
	case domain.ToolTypeSkill:
		return a.runSkillTool(ctx, choice)
	default:
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("unsupported tool type %q", choice.Type)
	}
}

func (a *Activities) runBrowserTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req browsertool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.WorkItemID = execution.WorkItemID
	req.Intent = choice.Intent
	req.WorkingDir = firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot)
	req.WorkspaceRoot = a.WorkspaceRoot
	req.Timeout = execution.Timeout
	req.Environment = workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot)

	executor := a.Browser
	if executor == nil {
		executor = browsertool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	code := strconv.Itoa(result.ExitCode)
	if err != nil && result.ExitCode == 0 && result.StartedAt.IsZero() {
		code = "1"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		code = "timeout"
	}
	metadata := map[string]string{
		"browser_exit_status": strconv.Itoa(result.ExitCode),
		"browser_session":     result.Session,
		"browser_command":     result.Command,
		"browser_executable":  result.Executable,
	}
	if len(result.HoistedArgs) > 0 {
		metadata["browser_hoisted_args"] = strings.Join(result.HoistedArgs, "\n")
	}
	if len(result.Args) > 0 {
		metadata["browser_args"] = strings.Join(result.Args, "\n")
	}
	if len(result.ArtifactPaths) > 0 {
		metadata["artifact_count"] = strconv.Itoa(len(result.ArtifactPaths))
		metadata["artifact_paths"] = strings.Join(result.ArtifactPaths, "\n")
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      browserObservation(result, err),
		ResultCode:       code,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runShellTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	if a.Shell == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("shell executor is not configured")
	}
	req := shelltool.Request{
		ProjectID:          execution.ProjectID,
		Intent:             choice.Intent,
		Command:            choice.Command,
		Args:               choice.Args,
		WorkingDir:         resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		Timeout:            execution.Timeout,
		Environment:        workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
		FallbackCandidates: execution.FallbackCandidates,
	}
	if choice.Metadata["multi_action"] == "true" {
		input, err := shelltool.DecodeBatchInput(choice.Input)
		if err != nil {
			return toolRunResult{ResultCode: "1"}, fmt.Errorf("decode shell batch input: %w", err)
		}
		for index := range input.Actions {
			input.Actions[index].WorkingDir = resolveRelativeToolPath(firstNonEmpty(input.Actions[index].WorkingDir, req.WorkingDir), a.WorkspaceRoot)
		}
		req.Command = ""
		req.Args = nil
		req.Actions = input.Actions
	}
	result, err := a.runShellWithHeartbeats(ctx, req)
	metadata := map[string]string{
		"shell_exit_status": strconv.Itoa(result.ExitCode),
		"run_mode":          string(firstNonEmpty(string(choice.RunMode), string(domain.ToolRunModeWaitForExit))),
		"idempotency":       string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":     string(toolProcessScope(choice.ProcessScope)),
	}
	resultCode := strconv.Itoa(result.ExitCode)
	if errors.Is(err, context.DeadlineExceeded) {
		resultCode = "timeout"
		metadata["possible_long_running_process"] = "true"
		metadata["timeout"] = "true"
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
		Observation:      shellObservation(result, err),
		ResultCode:       resultCode,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runShellWithHeartbeats(ctx context.Context, req shelltool.Request) (shelltool.Result, error) {
	type shellRun struct {
		result shelltool.Result
		err    error
	}
	done := make(chan shellRun, 1)
	go func() {
		result, err := a.Shell.Run(ctx, req)
		done <- shellRun{result: result, err: err}
	}()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case run := <-done:
			return run.result, run.err
		case <-ticker.C:
			if activity.IsActivity(ctx) {
				activity.RecordHeartbeat(ctx, map[string]string{
					"command": req.Command,
					"intent":  req.Intent,
				})
			}
		case <-ctx.Done():
			run := <-done
			if run.err != nil {
				return run.result, run.err
			}
			return run.result, ctx.Err()
		}
	}
}

func shellObservation(result shelltool.Result, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		observation := fullObservation(result.Stdout, result.Stderr, err)
		return observation + "\n\nresult_code: timeout\npossible_long_running_process: true\nsuggestion: retry this command with run_mode=start_background if it is expected to keep running."
	}
	return fullObservation(result.Stdout, result.Stderr, err)
}

func browserObservation(result browsertool.Result, err error) string {
	observation := fullObservation(result.Stdout, result.Stderr, err)
	if len(result.ArtifactPaths) == 0 {
		return observation
	}
	return observation + "\n\nartifacts:\n" + strings.Join(result.ArtifactPaths, "\n")
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
	metadata := map[string]string{
		"file_path":   result.FilePath,
		"lines_read":  strconv.Itoa(result.LinesRead),
		"total_lines": strconv.Itoa(result.TotalLines),
		"bytes_read":  strconv.Itoa(result.BytesRead),
		"truncated":   strconv.FormatBool(result.Truncated),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	return toolRunResult{
		Observation: readObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
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
	req.Cwd = resolveRelativeToolPath(firstNonEmpty(req.Cwd, choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot)
	req.Path = resolveRelativeToolPath(req.Path, req.Cwd)
	for index := range req.Actions {
		req.Actions[index].Cwd = resolveRelativeToolPath(firstNonEmpty(req.Actions[index].Cwd, req.Cwd), a.WorkspaceRoot)
		req.Actions[index].Path = resolveRelativeToolPath(req.Actions[index].Path, req.Actions[index].Cwd)
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
		"cwd":         req.Cwd,
		"match_count": strconv.Itoa(len(result.Matches)),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      globObservation(result, err),
		ResultCode:       resultCodeForError(err),
		WorkingDirectory: req.Cwd,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runGrepTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req greptool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.WorkingDir = firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot)
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
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      grepObservation(result, err),
		ResultCode:       code,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runSkillTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req skilltool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Skill
	if executor == nil {
		executor = skilltool.NewSafeExecutor(a.skillsRoots()...)
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: skillObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"skill_id":   result.SkillID,
			"skill_path": result.Path,
			"bytes_read": strconv.Itoa(result.BytesRead),
		},
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
	if len(result.Actions) > 0 {
		return readBatchObservation(result, err)
	}
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

func readBatchObservation(result readtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "files: %d\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		len(result.Actions),
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(&builder, "\n\nfile: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
			action.FilePath,
			action.LinesRead,
			action.TotalLines,
			action.BytesRead,
			action.Truncated,
		)
		if action.Content != "" {
			builder.WriteString("\ncontent:\n")
			builder.WriteString(action.Content)
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
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
	if len(result.Actions) > 0 {
		return globBatchObservation(result, err)
	}
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

func globBatchObservation(result globtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "patterns: %d\nmatches: %d", len(result.Actions), len(result.Matches))
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(&builder, "\n\npattern: %s\npath: %s\nmatches: %d",
			action.Pattern,
			action.Root,
			len(action.Matches),
		)
		if len(action.Matches) > 0 {
			builder.WriteString("\n")
			builder.WriteString(strings.Join(action.Matches, "\n"))
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
	}
	return builder.String()
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

func skillObservation(result skilltool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("<skill_content name=%q>\n%s\n\nSkill directory: %s\nRelative paths in this skill are relative to the skill directory.\n</skill_content>",
		result.SkillID,
		strings.TrimSpace(result.Content),
		filepath.Dir(result.Path),
	)
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

func ensureNextAction(nextAction *agent.NextAction, projectID string, event domain.Event, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	summary := firstNonEmpty(strings.TrimSpace(event.Body), "Handle inbound request.")
	if len(nextAction.WorkItems) == 0 {
		workItemID := stableActivityID("work-item", projectID, event.ID, "1")
		nextAction.WorkItems = []domain.WorkItem{{
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
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ProjectID == "" {
			nextAction.WorkItems[index].ProjectID = projectID
		}
		if nextAction.WorkItems[index].Status == "" {
			nextAction.WorkItems[index].Status = domain.WorkItemStatusReady
		}
		if nextAction.WorkItems[index].CreatedAt.IsZero() {
			nextAction.WorkItems[index].CreatedAt = now
		}
		if nextAction.WorkItems[index].UpdatedAt.IsZero() {
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	return nil
}

func ensureToolWorkItem(nextAction *agent.NextAction, workItemID string, now time.Time) error {
	if nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID) >= 0 {
		return nil
	}
	if strings.TrimSpace(workItemID) == "" {
		return fmt.Errorf("work item id is required")
	}
	item := domain.WorkItem{
		ID:          workItemID,
		ProjectID:   nextActionProjectID(*nextAction),
		Title:       "Handle request",
		Description: "Handle inbound request.",
		Status:      domain.WorkItemStatusReady,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	nextAction.WorkItems = append(nextAction.WorkItems, item)
	return nil
}

func nextActionProjectID(nextAction agent.NextAction) string {
	for _, item := range nextAction.WorkItems {
		if projectID := strings.TrimSpace(item.ProjectID); projectID != "" {
			return projectID
		}
	}
	return ""
}

func currentNextActionWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	if observation != nil {
		workItemID := strings.TrimSpace(observation.WorkItemID)
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			return workItemID
		}
	}
	for _, item := range nextAction.WorkItems {
		if item.Status != domain.WorkItemStatusCompleted {
			return item.ID
		}
	}
	return ""
}

func nextActionToolWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	return currentNextActionWorkItemID(nextAction, observation)
}

func completePreviousWorkItemForNextAction(nextAction *agent.NextAction, nextWorkItemID string, observation *agent.ExecutionFeedback, now time.Time) error {
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
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, previousWorkItemID)
	if index < 0 {
		return fmt.Errorf("work item %q not found for status update", previousWorkItemID)
	}
	nextAction.WorkItems[index].Status = domain.WorkItemStatusCompleted
	nextAction.WorkItems[index].UpdatedAt = now
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
	if choice.RunMode == "" {
		choice.RunMode = domain.ToolRunModeWaitForExit
	}
	if choice.Idempotency == "" {
		choice.Idempotency = domain.ToolIdempotencyUnknown
	}
	if choice.ProcessScope == "" {
		choice.ProcessScope = domain.ProcessScopeStopOnFinish
	}
	choice.Metadata["run_mode"] = string(choice.RunMode)
	choice.Metadata["idempotency"] = string(choice.Idempotency)
	choice.Metadata["process_scope"] = string(choice.ProcessScope)
}

func toolProcessScope(scope domain.ProcessScope) domain.ProcessScope {
	if scope == domain.ProcessScopeProject {
		return domain.ProcessScopeProject
	}
	return domain.ProcessScopeStopOnFinish
}

func markFinalNextActionWorkItems(nextAction *agent.NextAction, status domain.WorkItemStatus, observation *agent.ExecutionFeedback, now time.Time) {
	if status == "" {
		status = domain.WorkItemStatusCompleted
	}
	if observation != nil && strings.TrimSpace(observation.WorkItemID) != "" {
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].Status == domain.WorkItemStatusCompleted {
			continue
		}
		nextAction.WorkItems[index].Status = status
		nextAction.WorkItems[index].UpdatedAt = now
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

func cycleLimitResponseMessage(history []agent.ExecutionFeedback) string {
	if len(history) == 0 {
		return "Stopped after reaching the execution cycle limit before a response was produced."
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

func applyObservationToNextAction(nextAction *agent.NextAction, observation agent.ExecutionFeedback, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	if observation.WorkItemID == "" {
		return nil
	}
	status := domain.WorkItemStatusReady
	if observation.Status == string(domain.ExecutionStatusCanceled) {
		status = domain.WorkItemStatusBlocked
	}
	if err := setNextActionWorkItemStatus(nextAction, observation.WorkItemID, status, now); err != nil {
		return err
	}
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
	if index >= 0 {
		if nextAction.WorkItems[index].Metadata == nil {
			nextAction.WorkItems[index].Metadata = map[string]string{}
		}
		nextAction.WorkItems[index].Metadata["last_execution_status"] = observation.Status
		if code := strings.TrimSpace(observation.Metadata["result_code"]); code != "" {
			nextAction.WorkItems[index].Metadata["last_result_code"] = code
		}
	}
	return nil
}

func setNextActionWorkItemStatus(nextAction *agent.NextAction, workItemID string, status domain.WorkItemStatus, now time.Time) error {
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ID == workItemID {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("work item %q not found for status update", workItemID)
}

func nextActionWorkItemIndexByID(items []domain.WorkItem, workItemID string) int {
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

func (a *Activities) runtimeStateDir() string {
	stateDir, err := workspace.ResolveStateDir(a.StateDir, a.WorkspaceRoot)
	if err != nil {
		return ""
	}
	return stateDir
}

func (a *Activities) skillsRoots() []string {
	if strings.TrimSpace(a.SkillsRoot) != "" {
		return []string{a.SkillsRoot}
	}
	return skillcatalog.DefaultRoots()
}

func processStartObservation(process domain.ManagedProcess) string {
	if strings.TrimSpace(process.ID) == "" {
		return "Background process did not start."
	}
	var builder strings.Builder
	builder.WriteString("Started background process.")
	builder.WriteString("\nprocess_id: ")
	builder.WriteString(process.ID)
	if process.PID > 0 {
		builder.WriteString("\npid: ")
		builder.WriteString(strconv.Itoa(process.PID))
	}
	if process.PGID > 0 {
		builder.WriteString("\npgid: ")
		builder.WriteString(strconv.Itoa(process.PGID))
	}
	if process.StdoutLogPath != "" {
		builder.WriteString("\nstdout_log: ")
		builder.WriteString(process.StdoutLogPath)
	}
	if process.StderrLogPath != "" {
		builder.WriteString("\nstderr_log: ")
		builder.WriteString(process.StderrLogPath)
	}
	return builder.String()
}

func backgroundStartFailureObservation(ctx context.Context, manager *shelltool.ProcessManager, stateDir string, process domain.ManagedProcess, runErr error) string {
	if manager == nil || strings.TrimSpace(process.ID) == "" {
		return fullObservation("", "", runErr)
	}
	logs, err := manager.Logs(ctx, stateDir, process.ID, 0)
	if err != nil {
		return fullObservation("", "", runErr)
	}
	return fullObservation(logs.StdoutTail, logs.StderrTail, runErr)
}

func buildRuntimeContext(workspaceRoot, openCTORoot string) agent.RuntimeContext {
	shellPath := strings.TrimSpace(os.Getenv("SHELL"))
	return agent.RuntimeContext{
		OS:            goruntime.GOOS,
		Arch:          goruntime.GOARCH,
		Shell:         shellPath,
		Path:          os.Getenv("PATH"),
		WorkspaceRoot: workspaceRoot,
		OpenCTORoot:   openCTORoot,
	}
}

func workspaceEnvironment(workspaceRoot, openCTORoot string) map[string]string {
	env := map[string]string{}
	if workspaceRoot = strings.TrimSpace(workspaceRoot); workspaceRoot != "" {
		env[config.EnvOpenCTOWorkspace] = workspaceRoot
	}
	if openCTORoot = strings.TrimSpace(openCTORoot); openCTORoot != "" {
		env["OPENCTO_ROOT"] = openCTORoot
	}
	if len(env) == 0 {
		return nil
	}
	return env
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
