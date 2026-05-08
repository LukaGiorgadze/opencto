package workflows

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
)

const (
	maxExecutionCycles           = 20
	nextActionActivityTimeout    = 2 * time.Minute
	toolActivityTimeout          = 10 * time.Minute
	toolActivityHeartbeatTimeout = 2 * time.Minute
	responseSessionGracePeriod   = 5 * time.Minute
	responseSessionMaxDuration   = maxExecutionCycles*(nextActionActivityTimeout+toolActivityTimeout) + nextActionActivityTimeout + responseSessionGracePeriod
	responseSessionHeartbeatGap  = 30 * time.Second
	responseSessionRetryInitial  = responseSessionHeartbeatGap + 5*time.Second
)

func TaskWorkflow(ctx workflow.Context, input TaskWorkflowInput) (TaskWorkflowResult, error) {
	nextActionAO := workflow.ActivityOptions{
		StartToCloseTimeout: nextActionActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
	toolAO := workflow.ActivityOptions{
		StartToCloseTimeout: toolActivityTimeout,
		HeartbeatTimeout:    toolActivityHeartbeatTimeout,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	persistenceAO := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
		},
	}
	memoryExtractionAO := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	sessionAO := workflow.ActivityOptions{
		StartToCloseTimeout: responseSessionMaxDuration,
		HeartbeatTimeout:    responseSessionHeartbeatGap,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval: responseSessionRetryInitial,
			MaximumAttempts: 3,
		},
	}
	nextActionCtx := workflow.WithActivityOptions(ctx, nextActionAO)
	toolCtx := workflow.WithActivityOptions(ctx, toolAO)
	persistenceCtx := workflow.WithActivityOptions(ctx, persistenceAO)
	memoryExtractionCtx := workflow.WithActivityOptions(ctx, memoryExtractionAO)
	sessionCtx := workflow.WithActivityOptions(ctx, sessionAO)
	session := startResponseSession(ctx, sessionCtx, input.ProjectID, input.Event)
	defer func() {
		stopResponseSession(ctx, session)
	}()

	var currentAction agent.NextAction
	if input.NextAction != nil {
		currentAction = *input.NextAction
	}
	additionalEvents := append([]domain.Event(nil), input.AdditionalEvents...)
	var observationHistory []agent.ExecutionFeedback
	var lastResults []activities.ExecuteToolResult
	var processes []domain.ProcessReference

	if !input.ResumedFromPause {
		if err := persistEvent(persistenceCtx, activities.PersistEventRequest{Event: input.Event}); err != nil {
			return TaskWorkflowResult{}, err
		}
		if err := extractMemory(memoryExtractionCtx, activities.ExtractMemoryRequest{Event: input.Event}); err != nil {
			workflow.GetLogger(ctx).Warn("memory extraction failed", "error", err)
		}
	}

	for cycle := 1; cycle <= maxExecutionCycles; cycle++ {
		if err := persistTaskSignalEvents(persistenceCtx, drainTaskSignals(ctx, &additionalEvents)); err != nil {
			return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
		}
		next, err := nextAction(nextActionCtx, activities.NextActionRequest{
			ProjectID:          input.ProjectID,
			Event:              input.Event,
			AdditionalEvents:   additionalEvents,
			NextAction:         currentAction,
			LastResults:        lastResults,
			ObservationHistory: observationHistory,
			Processes:          processes,
			ExecutionCycle:     cycle,
			ResumedFromPause:   input.ResumedFromPause,
		})
		if err != nil {
			return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
		}

		currentAction = next.NextAction
		if len(next.Observations) > 0 {
			observationHistory = append(observationHistory, next.Observations...)
		} else if next.Observation != nil {
			observationHistory = append(observationHistory, *next.Observation)
		}
		if err := persistNextAction(persistenceCtx, activities.PersistNextActionRequest{
			Event:      input.Event,
			NextAction: next.NextAction,
			Status:     next.Status,
		}); err != nil {
			return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
		}
		if next.Status == activities.NextActionStatusWaiting {
			stopResponseSession(ctx, session)
			session = nil
			if err := persistTaskSignalEvents(persistenceCtx, drainTaskSignals(ctx, &additionalEvents)); err != nil {
				return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
			}
			waitingEvent := reportTargetEvent(input.Event, additionalEvents)
			notifyProjectTaskWaiting(ctx, input.ProjectID, waitingEvent, next)
			receipts := reportWaitingNextAction(ctx, waitingEvent, next)
			notifyProjectTaskOutput(ctx, waitingEvent, strings.TrimSpace(next.WaitingKind), receipts)
			signalEvents, canceled := waitForPlanningAnswer(ctx, strings.TrimSpace(next.WaitingKind), &additionalEvents)
			if err := persistTaskSignalEvents(persistenceCtx, signalEvents); err != nil {
				return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
			}
			if canceled {
				return TaskWorkflowResult{
					Completed: false,
					Status:    activities.NextActionStatusBlocked,
					Event:     input.Event,
				}, nil
			}
			session = startResponseSession(ctx, sessionCtx, input.ProjectID, reportTargetEvent(input.Event, additionalEvents))
			continue
		}
		if next.IsTerminal() {
			return resultFromNextAction(reportTargetEvent(input.Event, additionalEvents), next), nil
		}
		toolChoices := append([]agent.ToolChoice(nil), next.ToolChoices...)
		if len(toolChoices) == 0 && next.ToolChoice != nil {
			toolChoices = []agent.ToolChoice{*next.ToolChoice}
		}
		if len(toolChoices) == 0 {
			return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned non-terminal status %q without a tool choice", next.Status))
		}
		if strings.TrimSpace(next.WorkItemID) == "" {
			return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned a tool choice without a work item id"))
		}

		lastResults = nil
		for _, choice := range toolChoices {
			execResult, canceled, interrupted, signalEvents, err := executeToolStep(ctx, toolCtx, persistenceCtx, input.ProjectID, next.WorkItemID, input.Event, choice, cycle, &additionalEvents)
			mergeTaskProcesses(&processes, execResult.Processes)
			if err := persistTaskSignalEvents(persistenceCtx, signalEvents); err != nil {
				return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
			}
			if canceled {
				return completeIncompleteTask(nextActionCtx, input.ProjectID, input.Event, processes)
			}
			if err != nil {
				execResult = failedExecutionActivityResult(choice, next.WorkItemID, cycle, err)
			}
			if err := persistToolResult(persistenceCtx, activities.PersistToolResultRequest{
				Event:  input.Event,
				Result: execResult,
			}); err != nil {
				return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
			}
			lastResults = append(lastResults, execResult)
			if interrupted {
				break
			}
		}
	}

	final, err := nextAction(nextActionCtx, activities.NextActionRequest{
		ProjectID:          input.ProjectID,
		Event:              input.Event,
		AdditionalEvents:   additionalEvents,
		NextAction:         currentAction,
		LastResults:        lastResults,
		ObservationHistory: observationHistory,
		Processes:          processes,
		ExecutionCycle:     maxExecutionCycles + 1,
		ForceFinal:         true,
		ResumedFromPause:   input.ResumedFromPause,
	})
	if err != nil {
		return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
	}
	if len(final.Observations) > 0 {
		observationHistory = append(observationHistory, final.Observations...)
	} else if final.Observation != nil {
		observationHistory = append(observationHistory, *final.Observation)
	}
	if err := persistNextAction(persistenceCtx, activities.PersistNextActionRequest{
		Event:      input.Event,
		NextAction: final.NextAction,
		Status:     final.Status,
	}); err != nil {
		return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, err)
	}
	if !final.IsTerminal() {
		return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned non-terminal status %q for force-final request", final.Status))
	}
	return resultFromNextAction(reportTargetEvent(input.Event, additionalEvents), final), nil
}

func nextAction(ctx workflow.Context, request activities.NextActionRequest) (activities.NextActionResult, error) {
	var result activities.NextActionResult
	err := workflow.ExecuteActivity(ctx, "Activities.NextAction", request).Get(ctx, &result)
	return result, err
}

func persistEvent(ctx workflow.Context, request activities.PersistEventRequest) error {
	return workflow.ExecuteActivity(ctx, "Activities.PersistEvent", request).Get(ctx, nil)
}

func extractMemory(ctx workflow.Context, request activities.ExtractMemoryRequest) error {
	return workflow.ExecuteActivity(ctx, "Activities.ExtractMemory", request).Get(ctx, nil)
}

func persistNextAction(ctx workflow.Context, request activities.PersistNextActionRequest) error {
	return workflow.ExecuteActivity(ctx, "Activities.PersistNextAction", request).Get(ctx, nil)
}

func persistToolResult(ctx workflow.Context, request activities.PersistToolResultRequest) error {
	return workflow.ExecuteActivity(ctx, "Activities.PersistToolResult", request).Get(ctx, nil)
}

type taskSignalEvent struct {
	Event   domain.Event
	Control string
}

func persistTaskSignalEvents(ctx workflow.Context, events []taskSignalEvent) error {
	for _, item := range events {
		event := eventWithControlMetadata(item.Event, item.Control)
		if strings.TrimSpace(event.ID) == "" {
			continue
		}
		if err := persistEvent(ctx, activities.PersistEventRequest{Event: event}); err != nil {
			return err
		}
	}
	return nil
}

func eventWithControlMetadata(event domain.Event, control string) domain.Event {
	control = strings.TrimSpace(control)
	if control == "" {
		return event
	}
	metadata := domain.Metadata{}
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	metadata[domain.MetadataKeyControl] = control
	event.Metadata = metadata
	return event
}

func executeToolStep(ctx workflow.Context, toolCtx workflow.Context, persistenceCtx workflow.Context, projectID, workItemID string, event domain.Event, choice agent.ToolChoice, cycle int, additionalEvents *[]domain.Event) (activities.ExecuteToolResult, bool, bool, []taskSignalEvent, error) {
	activityBaseCtx := toolCtx
	activityName := "Activities.ExecuteTool"
	if isMemoryTool(choice.Type) {
		activityBaseCtx = persistenceCtx
		activityName = "Activities.ExecuteMemoryTool"
	}
	activityCtx, cancelActivity := workflow.WithCancel(activityBaseCtx)
	defer cancelActivity()
	future := workflow.ExecuteActivity(activityCtx, activityName, activities.ExecuteToolRequest{
		ProjectID:  projectID,
		WorkItemID: workItemID,
		Event:      event,
		ToolChoice: choice,
	})

	var result activities.ExecuteToolResult
	interrupted := false
	canceled := false
	var signalEvents []taskSignalEvent
	for {
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(future, func(f workflow.Future) {
			err := f.Get(ctx, &result)
			if err != nil {
				result = failedExecutionActivityResult(choice, workItemID, cycle, err)
			}
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskCancel), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "cancel"})
			canceled = true
			cancelActivity()
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskInterrupt), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "interrupt"})
			if strings.TrimSpace(signal.Event.Body) != "" {
				*additionalEvents = append(*additionalEvents, signal.Event)
			}
			interrupted = true
			cancelActivity()
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext), func(c workflow.ReceiveChannel, more bool) {
			var signal AdditionalContextSignal
			c.Receive(ctx, &signal)
			*additionalEvents = append(*additionalEvents, signal.Event)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event})
		})
		selector.Select(ctx)
		if result.ToolCallID != "" || result.Status != "" {
			if canceled {
				return result, true, false, signalEvents, nil
			}
			if interrupted {
				result.Status = domain.ExecutionStatusCanceled
				result.Error = "interrupted by user message"
			}
			return result, false, interrupted, signalEvents, nil
		}
		if interrupted {
			result = failedExecutionActivityResult(choice, workItemID, cycle, fmt.Errorf("interrupted by user message"))
			result.Status = domain.ExecutionStatusCanceled
			return result, false, true, signalEvents, nil
		}
	}
}

func isMemoryTool(toolType domain.ToolType) bool {
	switch toolType {
	case domain.ToolTypeMemoryProposeAdd, domain.ToolTypeMemorySearch, domain.ToolTypeMemoryList, domain.ToolTypeMemoryProposeUpdate, domain.ToolTypeMemoryProposeForget:
		return true
	default:
		return false
	}
}

func notifyProjectTaskWaiting(ctx workflow.Context, projectID string, event domain.Event, next activities.NextActionResult) {
	token := normalizePlanningToken(next.WaitingToken)
	kind := strings.TrimSpace(next.WaitingKind)
	if kind == "" {
		return
	}
	info := workflow.GetInfo(ctx)
	if info == nil || info.ParentWorkflowExecution == nil {
		return
	}
	signal := PlanningWaitSignal{
		WorkflowID: info.WorkflowExecution.ID,
		EventID:    event.ID,
		Token:      token,
		Kind:       kind,
		Event:      event,
	}
	if strings.TrimSpace(signal.Event.ProjectID) == "" {
		signal.Event.ProjectID = strings.TrimSpace(projectID)
	}
	err := workflow.SignalExternalWorkflow(ctx, info.ParentWorkflowExecution.ID, info.ParentWorkflowExecution.RunID, SignalProjectTaskWaiting, signal).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Warn("signal project task waiting failed", "waiting_kind", kind, "error", err.Error())
	}
}

func notifyProjectTaskOutput(ctx workflow.Context, event domain.Event, waitingKind string, receipts []domain.ReportReceipt) {
	if len(receipts) == 0 {
		return
	}
	info := workflow.GetInfo(ctx)
	if info == nil || info.ParentWorkflowExecution == nil {
		return
	}
	signal := TaskOutputSignal{
		WorkflowID:  info.WorkflowExecution.ID,
		EventID:     event.ID,
		WaitingKind: strings.TrimSpace(waitingKind),
		Receipts:    append([]domain.ReportReceipt(nil), receipts...),
	}
	err := workflow.SignalExternalWorkflow(ctx, info.ParentWorkflowExecution.ID, info.ParentWorkflowExecution.RunID, SignalProjectTaskOutput, signal).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Warn("signal project task output failed", "event_id", event.ID, "error", err.Error())
	}
}

func reportWaitingNextAction(ctx workflow.Context, event domain.Event, next activities.NextActionResult) []domain.ReportReceipt {
	message := strings.TrimSpace(next.NextAction.ResponseMessage)
	if message == "" {
		return nil
	}
	reportCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: reportResponseActivityTimeout,
		StartToCloseTimeout:    reportResponseActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})
	var result activities.ReportResponseResult
	err := workflow.ExecuteActivity(reportCtx, "Activities.ReportResponse", activities.ReportResponseRequest{
		Event:       event,
		Message:     message,
		Attachments: append([]domain.ReportAttachment(nil), next.NextAction.ResponseAttachments...),
		ReplyTo:     reportReplyForEvent(event),
		WaitingKind: strings.TrimSpace(next.WaitingKind),
	}).Get(reportCtx, &result)
	if err != nil {
		workflow.GetLogger(ctx).Error("report waiting response activity failed", "project_id", event.ProjectID, "event_id", event.ID, "error", err.Error())
		return nil
	}
	return result.Receipts
}

func reportReplyForEvent(event domain.Event) *domain.ReportReply {
	reply := domain.ReportReply{
		MessageID: firstNonEmptyWorkflowString(
			event.Metadata[domain.MetadataKeyReplyToMessageID],
			event.Provenance.SourceID,
			eventPayloadString(event, "message_id"),
		),
		ChannelID: firstNonEmptyWorkflowString(
			event.Metadata[domain.MetadataKeyReplyToChannelID],
			event.ChannelID,
		),
		ContextID: firstNonEmptyWorkflowString(
			event.Metadata[domain.MetadataKeyReplyToContextID],
			eventPayloadString(event, "guild_id"),
		),
	}
	if reply.Empty() {
		return nil
	}
	return &reply
}

func firstNonEmptyWorkflowString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func eventPayloadString(event domain.Event, key string) string {
	if event.Payload == nil {
		return ""
	}
	value, ok := event.Payload[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func eventWithPlanningSignalMetadata(event domain.Event, waitingKind string, decision string) domain.Event {
	metadata := domain.Metadata{}
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	metadata[domain.MetadataKeyControl] = domain.MetadataControlPlanningAnswer
	if value := strings.TrimSpace(waitingKind); value != "" {
		metadata[domain.MetadataKeyWaitingKind] = value
	}
	if value := strings.TrimSpace(decision); value != "" {
		metadata[domain.MetadataKeyApprovalDecision] = value
	}
	event.Metadata = metadata
	return event
}

func waitForPlanningAnswer(ctx workflow.Context, waitingKind string, additionalEvents *[]domain.Event) ([]taskSignalEvent, bool) {
	waitingKind = strings.TrimSpace(waitingKind)
	var signalEvents []taskSignalEvent
	for {
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskPlanningAnswer), func(c workflow.ReceiveChannel, more bool) {
			var signal PlanningAnswerSignal
			c.Receive(ctx, &signal)
			event := eventWithPlanningSignalMetadata(signal.Event, firstNonEmptyWorkflowString(signal.WaitingKind, waitingKind), signal.Decision)
			*additionalEvents = append(*additionalEvents, event)
			signalEvents = append(signalEvents, taskSignalEvent{Event: event, Control: domain.MetadataControlPlanningAnswer})
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext), func(c workflow.ReceiveChannel, more bool) {
			var signal AdditionalContextSignal
			c.Receive(ctx, &signal)
			event := eventWithPlanningSignalMetadata(signal.Event, waitingKind, "")
			*additionalEvents = append(*additionalEvents, event)
			signalEvents = append(signalEvents, taskSignalEvent{Event: event, Control: domain.MetadataControlPlanningAnswer})
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskCancel), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "cancel"})
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskInterrupt), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "interrupt"})
			if strings.TrimSpace(signal.Event.Body) != "" {
				*additionalEvents = append(*additionalEvents, signal.Event)
			}
		})
		selector.Select(ctx)
		for _, signalEvent := range signalEvents {
			if signalEvent.Control == domain.MetadataControlPlanningAnswer {
				return signalEvents, false
			}
			if signalEvent.Control == "cancel" {
				return signalEvents, true
			}
			if signalEvent.Control == "interrupt" {
				return signalEvents, false
			}
		}
	}
}

type responseSessionHandle struct {
	future workflow.Future
	cancel workflow.CancelFunc
}

func startResponseSession(ctx workflow.Context, sessionCtx workflow.Context, projectID string, event domain.Event) *responseSessionHandle {
	if !shouldStartResponseSession(event) {
		return nil
	}
	activityCtx, cancel := workflow.WithCancel(sessionCtx)
	return &responseSessionHandle{
		future: workflow.ExecuteActivity(activityCtx, "Activities.ResponseSession", activities.ResponseSessionRequest{
			ProjectID: projectID,
			Event:     event,
		}),
		cancel: cancel,
	}
}

func stopResponseSession(ctx workflow.Context, session *responseSessionHandle) {
	if session == nil {
		return
	}
	session.cancel()
	_ = session.future.Get(ctx, nil)
}

func shouldStartResponseSession(event domain.Event) bool {
	return strings.TrimSpace(event.ChannelID) != ""
}

func drainTaskSignals(ctx workflow.Context, additionalEvents *[]domain.Event) []taskSignalEvent {
	var signalEvents []taskSignalEvent
	for {
		var signal AdditionalContextSignal
		if !workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext).ReceiveAsync(&signal) {
			return signalEvents
		}
		*additionalEvents = append(*additionalEvents, signal.Event)
		signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event})
	}
}

func resultFromNextAction(event domain.Event, next activities.NextActionResult) TaskWorkflowResult {
	message := strings.TrimSpace(next.NextAction.ResponseMessage)
	attachments := append([]domain.ReportAttachment(nil), next.NextAction.ResponseAttachments...)
	return TaskWorkflowResult{
		Completed:           next.Status == activities.NextActionStatusCompleted || next.Status == activities.NextActionStatusIgnored,
		Status:              next.Status,
		Event:               event,
		ResponseMessage:     message,
		ResponseAttachments: attachments,
		Report:              next.Status != activities.NextActionStatusIgnored && (message != "" || len(attachments) > 0),
	}
}

func reportTargetEvent(base domain.Event, additionalEvents []domain.Event) domain.Event {
	target := base
	for _, event := range additionalEvents {
		channelID := strings.TrimSpace(event.ChannelID)
		if channelID == "" {
			continue
		}
		target.ChannelID = channelID
		target.ThreadID = strings.TrimSpace(event.ThreadID)
		if strings.TrimSpace(string(event.ChannelType)) != "" {
			target.ChannelType = event.ChannelType
		}
		target.Metadata = reportTargetMetadata(target.Metadata, event.Metadata)
	}
	return target
}

func reportTargetMetadata(base domain.Metadata, update domain.Metadata) domain.Metadata {
	if len(base) == 0 && len(update) == 0 {
		return nil
	}
	metadata := domain.Metadata{}
	for key, value := range base {
		metadata[key] = value
	}
	for _, key := range []string{
		domain.MetadataKeyReplyToMessageID,
		domain.MetadataKeyReplyToChannelID,
		domain.MetadataKeyReplyToContextID,
	} {
		if value := strings.TrimSpace(update[key]); value != "" {
			metadata[key] = value
		}
	}
	return metadata
}

func completeTaskAfterProcessStart(ctx workflow.Context, projectID string, event domain.Event, processes []domain.ProcessReference, err error) (TaskWorkflowResult, error) {
	if len(processes) == 0 {
		return TaskWorkflowResult{}, err
	}
	return completeIncompleteTask(ctx, projectID, event, processes)
}

func completeIncompleteTask(ctx workflow.Context, projectID string, event domain.Event, processes []domain.ProcessReference) (TaskWorkflowResult, error) {
	if len(processes) == 0 {
		return TaskWorkflowResult{Completed: false}, nil
	}
	cleanupCtx, _ := workflow.NewDisconnectedContext(ctx)
	var next activities.NextActionResult
	err := workflow.ExecuteActivity(cleanupCtx, "Activities.NextAction", activities.NextActionRequest{
		ProjectID: projectID,
		Event:     event,
		Completion: &activities.TaskCompletionRequest{
			Status:    activities.NextActionStatusFailed,
			Processes: processes,
		},
	}).Get(cleanupCtx, &next)
	if err != nil {
		return TaskWorkflowResult{}, err
	}
	return resultFromNextAction(event, next), nil
}

func mergeTaskProcesses(processes *[]domain.ProcessReference, updates []domain.ProcessReference) {
	for _, update := range updates {
		if strings.TrimSpace(update.ID) == "" {
			continue
		}
		replaced := false
		for index := range *processes {
			if (*processes)[index].ID == update.ID {
				(*processes)[index] = update
				replaced = true
				break
			}
		}
		if !replaced {
			*processes = append(*processes, update)
		}
	}
}

func failedExecutionActivityResult(choice agent.ToolChoice, workItemID string, cycle int, err error) activities.ExecuteToolResult {
	message := "tool execution activity failed"
	if err != nil {
		message = err.Error()
	}
	return activities.ExecuteToolResult{
		Cycle:            cycle,
		WorkItemID:       workItemID,
		ToolCallID:       toolCallID(choice),
		Tool:             choice.Type,
		Status:           domain.ExecutionStatusFailed,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Input:            choice.Input,
		Observation:      "Tool execution failed before producing a structured result.",
		Error:            message,
		WorkingDirectory: choice.WorkingDir,
		ResultCode:       "activity_error",
		Metadata:         choice.Metadata,
	}
}

func toolCallID(choice agent.ToolChoice) string {
	if strings.TrimSpace(choice.ToolCallID) != "" {
		return strings.TrimSpace(choice.ToolCallID)
	}
	if choice.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(choice.Metadata["tool_call_id"])
}
