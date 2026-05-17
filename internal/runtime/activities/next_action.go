package activities

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	exectool "github.com/opencto/opencto/internal/tools/exec"
)

func (a *Activities) NextAction(ctx context.Context, request NextActionRequest) (NextActionResult, error) {
	if request.Completion != nil {
		return a.completeTask(ctx, request.ProjectID, request.Event, agent.NextAction{}, *request.Completion, false, false)
	}
	a.logActivityStep(
		"NextAction", "start",
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

	a.logActivityStep(
		"NextAction", "load_context_begin",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
	)
	var loaded agent.Context
	var err error
	if request.SubAgent != nil {
		loaded, err = a.loadSubAgentContext(ctx, event, request.AdditionalEvents, *request.SubAgent, request.ToolAllowlist)
	} else {
		conversationEvent := latestConversationContextEvent(event, request.AdditionalEvents)
		loaded, err = a.loadContext(ctx, event, conversationEvent, request.AdditionalEvents)
	}
	if err != nil {
		a.logActivityStep(
			"NextAction", "load_context_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	loaded.AdditionalEvents = append([]domain.Event(nil), request.AdditionalEvents...)
	nextActionEvent := event
	if request.SubAgent != nil {
		nextActionEvent = loaded.Event
	}
	a.logActivityStep(
		"NextAction", "load_context_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("active_work_items", len(loaded.ActiveWorkItems)),
	)

	now := time.Now().UTC()
	nextAction := request.NextAction
	if err := ensureNextAction(&nextAction, projectID, nextActionEvent, now); err != nil {
		a.logActivityStep(
			"NextAction", "ensure_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "ensure_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	history := append([]agent.ExecutionFeedback(nil), request.ObservationHistory...)
	var observations []agent.ExecutionFeedback
	if len(request.LastResults) > 0 || request.LastResult != nil {
		a.logActivityStep(
			"NextAction", "apply_last_result_begin",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.Int("last_results", len(request.LastResults)),
		)
		results := request.LastResults
		if len(results) == 0 && request.LastResult != nil {
			results = []ExecuteToolResult{*request.LastResult}
		}
		nextAction.ToolChoice = agent.ToolChoice{}
		for _, result := range results {
			feedback := executionFeedback(result)
			observations = append(observations, feedback)
			history = append(history, feedback)
			if err := applyObservationToNextAction(&nextAction, feedback, now); err != nil {
				a.logActivityStep(
					"NextAction", "apply_last_result_error",
					slog.String("project_id", projectID),
					slog.String("event_id", event.ID),
					slog.String("work_item_id", feedback.WorkItemID),
					slog.String("tool_call_id", feedback.ToolCallID),
					slog.String("error", err.Error()),
				)
				return NextActionResult{}, err
			}
		}
		a.logActivityStep(
			"NextAction", "apply_last_result_done",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.Int("applied_results", len(observations)),
			slog.Int("history_len", len(history)),
		)
	}

	a.logActivityStep(
		"NextAction", "engine_next_action_begin",
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
		LastObservation:    lastObservation(observations),
		ObservationHistory: history,
		ChannelType:        event.ChannelType,
		SubAgent:           request.SubAgent,
		ToolAllowlist:      request.ToolAllowlist,
		RestrictTools:      request.RestrictTools,
	})
	if err != nil {
		a.logActivityStep(
			"NextAction", "engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
		slog.Bool("has_tool_choice", engineOutput.ToolChoice != nil),
		slog.String("work_item_id", strings.TrimSpace(engineOutput.WorkItemID)),
	)
	if len(engineOutput.NextAction.WorkItems) > 0 || !engineOutput.NextAction.ToolChoice.IsZero() || strings.TrimSpace(engineOutput.NextAction.ResponseMessage) != "" || len(engineOutput.NextAction.ResponseAttachments) > 0 {
		nextAction = engineOutput.NextAction
	}
	if err := ensureNextAction(&nextAction, projectID, nextActionEvent, now); err != nil {
		a.logActivityStep(
			"NextAction", "ensure_engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "ensure_engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	if request.ForceFinal {
		a.logActivityStep(
			"NextAction", "force_final_override_status",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		engineOutput.Status = NextActionStatusBlocked
		engineOutput.ToolChoice = nil
		if strings.TrimSpace(engineOutput.NextAction.ResponseMessage) == "" {
			engineOutput.NextAction.ResponseMessage = cycleLimitResponseMessage(history)
		}
	}

	a.logActivityStep(
		"NextAction", "dispatch_status",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
	)
	switch engineOutput.Status {
	case NextActionStatusTool:
		return a.prepareToolNextAction(ctx, nextAction, observations, engineOutput, request.ExecutionCycle, now)
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return a.finishNextAction(ctx, event, nextAction, lastObservation(observations), observations, engineOutput, request.Processes, now)
	default:
		return NextActionResult{}, fmt.Errorf("unsupported next action status %q", engineOutput.Status)
	}
}

func latestConversationContextEvent(base domain.Event, additional []domain.Event) domain.Event {
	target := base
	for _, event := range additional {
		if strings.TrimSpace(event.ChannelID) == "" && strings.TrimSpace(event.ThreadID) == "" {
			continue
		}
		target = event
		if strings.TrimSpace(target.ProjectID) == "" {
			target.ProjectID = strings.TrimSpace(base.ProjectID)
		}
		if strings.TrimSpace(string(target.ChannelType)) == "" {
			target.ChannelType = base.ChannelType
		}
		target = inferDiscordThreadContext(target)
	}
	return target
}

func (a *Activities) prepareToolNextAction(ctx context.Context, nextAction agent.NextAction, observations []agent.ExecutionFeedback, output agent.NextActionOutput, cycle int, now time.Time) (NextActionResult, error) {
	a.logActivityStep(
		"NextAction", "prepare_tool_begin",
		slog.Int("execution_cycle", cycle),
		slog.Int("observations", len(observations)),
		slog.Bool("has_tool_choice", output.ToolChoice != nil),
		slog.Int("tool_choices", len(output.ToolChoices)),
		slog.String("output_work_item_id", strings.TrimSpace(output.WorkItemID)),
	)
	choices := append([]agent.ToolChoice(nil), output.ToolChoices...)
	if len(choices) == 0 && output.ToolChoice != nil {
		choices = []agent.ToolChoice{*output.ToolChoice}
	}
	if len(choices) == 0 {
		a.logActivityStep("NextAction", "prepare_tool_missing_choice")
		return NextActionResult{}, fmt.Errorf("%w: tool status requires at least one tool choice", agent.ErrInvalidToolChoice)
	}

	observation := lastObservation(observations)
	choice := choices[0]
	workItemID := nextActionToolWorkItemID(nextAction, observation)
	if strings.TrimSpace(workItemID) == "" {
		a.logActivityStep(
			"NextAction", "prepare_tool_missing_work_item_id",
			slog.String("tool_call_id", choice.ToolCallID),
		)
		return NextActionResult{}, fmt.Errorf("%w: tool choice is missing work item id", agent.ErrInvalidToolChoice)
	}
	a.logActivityStep(
		"NextAction", "prepare_tool_work_item_resolved",
		slog.String("work_item_id", workItemID),
		slog.String("tool_type", string(choice.Type)),
		slog.String("tool_call_id", choice.ToolCallID),
	)
	if err := ensureToolWorkItem(&nextAction, workItemID, now); err != nil {
		a.logActivityStep(
			"NextAction", "prepare_tool_ensure_work_item_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	if err := completePreviousWorkItemForNextAction(&nextAction, workItemID, observation, now); err != nil {
		a.logActivityStep(
			"NextAction", "prepare_tool_complete_previous_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "prepare_tool_work_items_ready",
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
	toolCallIDs := make([]string, 0, len(choices))
	for _, choice := range choices {
		toolCallIDs = append(toolCallIDs, strings.TrimSpace(choice.ToolCallID))
	}
	for index := range choices {
		ensureToolChoiceMetadata(&choices[index], workItemID, cycle, assistantText)
		choices[index].Metadata["tool_call_ids"] = strings.Join(toolCallIDs, ",")
	}
	choice = choices[0]
	nextAction.ToolChoice = choice
	nextAction.ResponseMessage = ""

	a.logActivityStep(
		"NextAction", "prepare_tool_done",
		slog.String("work_item_id", workItemID),
		slog.String("tool_call_id", choice.ToolCallID),
		slog.String("tool_type", string(choice.Type)),
	)

	return NextActionResult{
		NextAction:   nextAction,
		ToolChoice:   &choice,
		ToolChoices:  choices,
		WorkItemID:   workItemID,
		Observation:  observation,
		Observations: observations,
		Status:       NextActionStatusTool,
	}, nil
}

func (a *Activities) finishNextAction(ctx context.Context, event domain.Event, nextAction agent.NextAction, observation *agent.ExecutionFeedback, observations []agent.ExecutionFeedback, output agent.NextActionOutput, processes []domain.ProcessReference, now time.Time) (NextActionResult, error) {
	a.logActivityStep(
		"NextAction", "finish_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
		slog.Bool("has_observation", observation != nil),
	)
	message := strings.TrimSpace(output.NextAction.ResponseMessage)
	attachments := append([]domain.ReportAttachment(nil), output.NextAction.ResponseAttachments...)
	if message == "" && len(attachments) == 0 {
		a.logActivityStep(
			"NextAction", "finish_missing_response_message",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", output.Status),
		)
		return NextActionResult{}, fmt.Errorf("%w: terminal next action is missing response message", agent.ErrInvalidNextAction)
	}

	nextAction.ToolChoice = agent.ToolChoice{}
	nextAction.ResponseMessage = message
	nextAction.ResponseAttachments = attachments
	markFinalNextActionWorkItems(&nextAction, terminalWorkItemStatus(output.Status), observation, now)
	result, err := a.completeTask(ctx, event.ProjectID, event, nextAction, TaskCompletionRequest{
		Status:    output.Status,
		Processes: processes,
	}, false, false)
	if err != nil {
		return NextActionResult{}, err
	}
	result.Observation = observation
	result.Observations = observations
	a.logActivityStep(
		"NextAction", "finish_done",
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
	a.logActivityStep(
		"NextAction", "complete_task_begin",
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
	attachments := append([]domain.ReportAttachment(nil), nextAction.ResponseAttachments...)
	if persist {
		if err := a.persistNextAction(ctx, nextAction); err != nil {
			a.logActivityStep(
				"NextAction", "complete_task_persist_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
	}
	reportMessage := domain.ReportMessage{Text: message, Attachments: attachments}
	if report && status != NextActionStatusIgnored && a.Reporter != nil && !reportMessage.Empty() {
		a.logActivityStep(
			"NextAction", "complete_task_report_begin",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
		if _, err := a.Reporter.Report(ctx, event, reportMessage); err != nil {
			a.logActivityStep(
				"NextAction", "complete_task_report_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
		a.logActivityStep(
			"NextAction", "complete_task_report_done",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
	}
	a.logActivityStep(
		"NextAction", "complete_task_done",
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
	manager := exectool.NewProcessManager(a.activityLogger())
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
