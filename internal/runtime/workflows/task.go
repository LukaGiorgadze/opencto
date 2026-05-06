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
	sessionAO := workflow.ActivityOptions{
		StartToCloseTimeout: responseSessionMaxDuration,
		HeartbeatTimeout:    responseSessionHeartbeatGap,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	nextActionCtx := workflow.WithActivityOptions(ctx, nextActionAO)
	toolCtx := workflow.WithActivityOptions(ctx, toolAO)
	sessionCtx := workflow.WithActivityOptions(ctx, sessionAO)
	session := startResponseSession(ctx, sessionCtx, input.ProjectID, input.Event)
	defer stopResponseSession(ctx, session)

	var currentAction agent.NextAction
	if input.NextAction != nil {
		currentAction = *input.NextAction
	}
	additionalEvents := append([]domain.Event(nil), input.AdditionalEvents...)
	var observationHistory []agent.ExecutionFeedback
	var lastResults []activities.ExecuteToolResult
	var processes []domain.ProcessReference

	for cycle := 1; cycle <= maxExecutionCycles; cycle++ {
		drainTaskSignals(ctx, &additionalEvents)
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
		if next.IsTerminal() {
			return resultFromNextAction(input.Event, next), nil
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
			execResult, canceled, interrupted, err := executeToolStep(ctx, toolCtx, input.ProjectID, next.WorkItemID, input.Event, choice, cycle, &additionalEvents)
			mergeTaskProcesses(&processes, execResult.Processes)
			if canceled {
				return completeIncompleteTask(nextActionCtx, input.ProjectID, input.Event, processes)
			}
			if err != nil {
				execResult = failedExecutionActivityResult(choice, next.WorkItemID, cycle, err)
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
	if !final.IsTerminal() {
		return completeTaskAfterProcessStart(nextActionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned non-terminal status %q for force-final request", final.Status))
	}
	return resultFromNextAction(input.Event, final), nil
}

func nextAction(ctx workflow.Context, request activities.NextActionRequest) (activities.NextActionResult, error) {
	var result activities.NextActionResult
	err := workflow.ExecuteActivity(ctx, "Activities.NextAction", request).Get(ctx, &result)
	return result, err
}

func executeToolStep(ctx workflow.Context, toolCtx workflow.Context, projectID, workItemID string, event domain.Event, choice agent.ToolChoice, cycle int, additionalEvents *[]domain.Event) (activities.ExecuteToolResult, bool, bool, error) {
	activityCtx, cancelActivity := workflow.WithCancel(toolCtx)
	defer cancelActivity()
	future := workflow.ExecuteActivity(activityCtx, "Activities.ExecuteTool", activities.ExecuteToolRequest{
		ProjectID:  projectID,
		WorkItemID: workItemID,
		Event:      event,
		ToolChoice: choice,
	})

	var result activities.ExecuteToolResult
	interrupted := false
	canceled := false
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
			canceled = true
			cancelActivity()
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskInterrupt), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
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
		})
		selector.Select(ctx)
		if result.ToolCallID != "" || result.Status != "" {
			if canceled {
				return result, true, false, nil
			}
			if interrupted {
				result.Status = domain.ExecutionStatusCanceled
				result.Error = "interrupted by user message"
			}
			return result, false, interrupted, nil
		}
		if interrupted {
			result = failedExecutionActivityResult(choice, workItemID, cycle, fmt.Errorf("interrupted by user message"))
			result.Status = domain.ExecutionStatusCanceled
			return result, false, true, nil
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

func drainTaskSignals(ctx workflow.Context, additionalEvents *[]domain.Event) {
	for {
		var signal AdditionalContextSignal
		if !workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext).ReceiveAsync(&signal) {
			return
		}
		*additionalEvents = append(*additionalEvents, signal.Event)
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
