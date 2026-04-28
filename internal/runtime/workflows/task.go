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
	maxExecutionCycles    = 20
	typingRefreshInterval = 5 * time.Second
)

func TaskWorkflow(ctx workflow.Context, input TaskWorkflowInput) (TaskWorkflowResult, error) {
	decisionAO := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
	toolAO := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		HeartbeatTimeout:    10 * time.Second,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	typingAO := workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	decisionCtx := workflow.WithActivityOptions(ctx, decisionAO)
	toolCtx := workflow.WithActivityOptions(ctx, toolAO)
	typingCtx := workflow.WithActivityOptions(ctx, typingAO)

	var decision agent.DecisionOutput
	if input.Decision != nil {
		decision = *input.Decision
	}
	additionalEvents := append([]domain.Event(nil), input.AdditionalEvents...)
	var observationHistory []agent.ExecutionFeedback
	var lastResult *activities.ExecuteToolResult
	var processes []domain.ProcessReference

	for cycle := 1; cycle <= maxExecutionCycles; cycle++ {
		drainTaskSignals(ctx, &additionalEvents)
		next, err := nextAction(ctx, decisionCtx, typingCtx, activities.NextActionRequest{
			ProjectID:          input.ProjectID,
			Event:              input.Event,
			AdditionalEvents:   additionalEvents,
			Decision:           decision,
			LastResult:         lastResult,
			ObservationHistory: observationHistory,
			Processes:          processes,
			ExecutionCycle:     cycle,
			ResumedFromPause:   input.ResumedFromPause,
		})
		if err != nil {
			return completeTaskAfterProcessStart(decisionCtx, input.ProjectID, input.Event, processes, err)
		}

		decision = next.Decision
		if next.Observation != nil {
			observationHistory = append(observationHistory, *next.Observation)
		}
		if next.IsTerminal() {
			return resultFromNextAction(next), nil
		}
		if next.ToolChoice == nil {
			return completeTaskAfterProcessStart(decisionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned non-terminal status %q without a tool choice", next.Status))
		}
		if strings.TrimSpace(next.WorkItemID) == "" {
			return completeTaskAfterProcessStart(decisionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned a tool choice without a work item id"))
		}

		execResult, canceled, err := executeToolStep(ctx, toolCtx, typingCtx, input.Event, input.ProjectID, next.WorkItemID, *next.ToolChoice, cycle, &additionalEvents)
		mergeTaskProcesses(&processes, execResult.Processes)
		if canceled {
			return completeIncompleteTask(decisionCtx, input.ProjectID, input.Event, processes)
		}
		if err != nil {
			execResult = failedExecutionActivityResult(*next.ToolChoice, next.WorkItemID, cycle, err)
		}
		lastResult = &execResult
	}

	final, err := nextAction(ctx, decisionCtx, typingCtx, activities.NextActionRequest{
		ProjectID:          input.ProjectID,
		Event:              input.Event,
		AdditionalEvents:   additionalEvents,
		Decision:           decision,
		LastResult:         lastResult,
		ObservationHistory: observationHistory,
		Processes:          processes,
		ExecutionCycle:     maxExecutionCycles + 1,
		ForceFinal:         true,
		ResumedFromPause:   input.ResumedFromPause,
	})
	if err != nil {
		return completeTaskAfterProcessStart(decisionCtx, input.ProjectID, input.Event, processes, err)
	}
	if final.Observation != nil {
		observationHistory = append(observationHistory, *final.Observation)
	}
	if !final.IsTerminal() {
		return completeTaskAfterProcessStart(decisionCtx, input.ProjectID, input.Event, processes, fmt.Errorf("Activities.NextAction returned non-terminal status %q for force-final request", final.Status))
	}
	return resultFromNextAction(final), nil
}

func nextAction(ctx workflow.Context, activityCtx workflow.Context, typingCtx workflow.Context, request activities.NextActionRequest) (activities.NextActionResult, error) {
	var result activities.NextActionResult
	future := workflow.ExecuteActivity(activityCtx, "Activities.NextAction", request)
	err := waitForActivityWithTyping(ctx, typingCtx, request.Event, future, func(f workflow.Future) error {
		return f.Get(ctx, &result)
	})
	return result, err
}

func executeToolStep(ctx workflow.Context, toolCtx workflow.Context, typingCtx workflow.Context, event domain.Event, projectID, workItemID string, choice agent.ToolChoice, cycle int, additionalEvents *[]domain.Event) (activities.ExecuteToolResult, bool, error) {
	activityCtx, cancelActivity := workflow.WithCancel(toolCtx)
	defer cancelActivity()
	future := workflow.ExecuteActivity(activityCtx, "Activities.ExecuteTool", activities.ExecuteToolRequest{
		ProjectID:  projectID,
		WorkItemID: workItemID,
		ToolChoice: choice,
	})

	var result activities.ExecuteToolResult
	interrupted := false
	canceled := false
	notifyTyping(ctx, typingCtx, event)
	typingTimer := newTypingTimer(ctx, event)
	for {
		selector := workflow.NewSelector(ctx)
		activityDone := false
		typingDue := false
		var additionalTypingEvent *domain.Event
		selector.AddFuture(future, func(f workflow.Future) {
			err := f.Get(ctx, &result)
			if err != nil {
				result = failedExecutionActivityResult(choice, workItemID, cycle, err)
			}
			activityDone = true
		})
		if typingTimer != nil {
			selector.AddFuture(typingTimer.future, func(f workflow.Future) {
				_ = f.Get(ctx, nil)
				typingDue = true
			})
		}
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
			additionalTypingEvent = &signal.Event
		})
		selector.Select(ctx)
		if activityDone || result.ToolCallID != "" || result.Status != "" {
			cancelTypingTimer(typingTimer)
			if canceled {
				return result, true, nil
			}
			if interrupted {
				result.Status = domain.ExecutionStatusCanceled
				result.Error = "interrupted by user message"
			}
			return result, false, nil
		}
		if interrupted {
			cancelTypingTimer(typingTimer)
			result = failedExecutionActivityResult(choice, workItemID, cycle, fmt.Errorf("interrupted by user message"))
			result.Status = domain.ExecutionStatusCanceled
			return result, false, nil
		}
		if additionalTypingEvent != nil {
			notifyTyping(ctx, typingCtx, *additionalTypingEvent)
		}
		if typingDue {
			notifyTyping(ctx, typingCtx, event)
			cancelTypingTimer(typingTimer)
			typingTimer = newTypingTimer(ctx, event)
		}
	}
}

func waitForActivityWithTyping(ctx workflow.Context, typingCtx workflow.Context, event domain.Event, future workflow.Future, get func(workflow.Future) error) error {
	if !shouldNotifyTyping(event) {
		return get(future)
	}
	notifyTyping(ctx, typingCtx, event)
	typingTimer := newTypingTimer(ctx, event)
	for {
		selector := workflow.NewSelector(ctx)
		activityDone := false
		typingDue := false
		var activityErr error
		selector.AddFuture(future, func(f workflow.Future) {
			activityErr = get(f)
			activityDone = true
		})
		if typingTimer != nil {
			selector.AddFuture(typingTimer.future, func(f workflow.Future) {
				_ = f.Get(ctx, nil)
				typingDue = true
			})
		}
		selector.Select(ctx)
		if activityDone {
			cancelTypingTimer(typingTimer)
			return activityErr
		}
		if typingDue {
			notifyTyping(ctx, typingCtx, event)
			cancelTypingTimer(typingTimer)
			typingTimer = newTypingTimer(ctx, event)
		}
	}
}

type typingTimerHandle struct {
	future workflow.Future
	cancel workflow.CancelFunc
}

func newTypingTimer(ctx workflow.Context, event domain.Event) *typingTimerHandle {
	if !shouldNotifyTyping(event) {
		return nil
	}
	timerCtx, cancel := workflow.WithCancel(ctx)
	return &typingTimerHandle{
		future: workflow.NewTimer(timerCtx, typingRefreshInterval),
		cancel: cancel,
	}
}

func cancelTypingTimer(timer *typingTimerHandle) {
	if timer != nil && timer.cancel != nil {
		timer.cancel()
	}
}

func notifyTyping(ctx workflow.Context, typingCtx workflow.Context, event domain.Event) {
	if !shouldNotifyTyping(event) {
		return
	}
	_ = workflow.ExecuteActivity(typingCtx, "Activities.NotifyTyping", event).Get(ctx, nil)
}

func shouldNotifyTyping(event domain.Event) bool {
	return event.ChannelType == domain.ChannelTypeDiscord && strings.TrimSpace(event.ChannelID) != ""
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

func resultFromNextAction(next activities.NextActionResult) TaskWorkflowResult {
	return TaskWorkflowResult{
		Completed: next.Status == activities.NextActionStatusCompleted || next.Status == activities.NextActionStatusIgnored,
		Processes: next.Processes,
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
	return resultFromNextAction(next), nil
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
