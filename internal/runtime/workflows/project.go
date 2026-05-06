package workflows

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

func ProjectWorkflow(ctx workflow.Context, input ProjectWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	state := ProjectWorkflowState{
		ProjectID:    input.ProjectID,
		ActiveTasks:  map[string]string{},
		SeenEventIDs: map[string]bool{},
	}
	if input.Snapshot != nil {
		state = *input.Snapshot
		if state.ActiveTasks == nil {
			state.ActiveTasks = map[string]string{}
		}
		if state.SeenEventIDs == nil {
			state.SeenEventIDs = map[string]bool{}
		}
	}

	if err := workflow.SetQueryHandler(ctx, QueryProjectState, func() (ProjectWorkflowState, error) {
		return state, nil
	}); err != nil {
		return err
	}

	eventSignal := workflow.GetSignalChannel(ctx, SignalEnqueueEvent)
	active := map[string]activeTask{}
	var pendingReports []TaskWorkflowResult

	for {
		for len(pendingReports) > 0 {
			report := pendingReports[0]
			pendingReports = pendingReports[1:]
			if err := reportTaskResult(ctx, report); err != nil {
				return err
			}
		}

		if input.ContinueAsNewAfterEvents > 0 && state.ProcessedEvents >= input.ContinueAsNewAfterEvents && len(active) == 0 {
			snapshot := state
			return workflow.NewContinueAsNewError(ctx, ProjectWorkflow, ProjectWorkflowInput{
				ProjectID:                input.ProjectID,
				ContinueAsNewAfterEvents: input.ContinueAsNewAfterEvents,
				Snapshot:                 &snapshot,
			})
		}

		if len(active) == 0 && len(state.Queue) > 0 {
			event := state.Queue[0]
			state.Queue = state.Queue[1:]
			workflowID := taskWorkflowID(input.ProjectID, event.ID)
			childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
				WorkflowID: workflowID,
			})
			future := workflow.ExecuteChildWorkflow(childCtx, TaskWorkflowName, TaskWorkflowInput{
				ProjectID: input.ProjectID,
				Event:     event,
			})
			if err := future.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
				pendingReports = append(pendingReports, failedTaskWorkflowResult(event, err))
				state.ProcessedEvents++
				continue
			}
			active[event.ID] = activeTask{Future: future, Event: event}
			state.ActiveTasks[event.ID] = workflowID
			continue
		}

		selector := workflow.NewSelector(ctx)
		selector.AddReceive(eventSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal EnqueueEventSignal
			c.Receive(ctx, &signal)
			handleProjectEventSignal(ctx, &state, active, input.ProjectID, signal.Event)
		})
		for eventID, task := range active {
			eventID := eventID
			task := task
			selector.AddFuture(task.Future, func(f workflow.Future) {
				var result TaskWorkflowResult
				if err := f.Get(ctx, &result); err != nil {
					result = failedTaskWorkflowResult(task.Event, err)
				}
				if result.Report {
					pendingReports = append(pendingReports, result)
				}
				delete(active, eventID)
				delete(state.ActiveTasks, eventID)
				state.ProcessedEvents++
			})
		}
		selector.Select(ctx)
	}
}

type activeTask struct {
	Future workflow.ChildWorkflowFuture
	Event  domain.Event
}

func reportTaskResult(ctx workflow.Context, result TaskWorkflowResult) error {
	report := domain.ReportMessage{
		Text:        result.ResponseMessage,
		Attachments: result.ResponseAttachments,
	}
	if !result.Report || report.Empty() {
		return nil
	}
	return workflow.ExecuteActivity(ctx, "Activities.ReportResponse", activities.ReportResponseRequest{
		Event:       result.Event,
		Message:     report.Text,
		Attachments: report.Attachments,
	}).Get(ctx, nil)
}

func failedTaskWorkflowResult(event domain.Event, err error) TaskWorkflowResult {
	return TaskWorkflowResult{
		Completed:       false,
		Status:          activities.NextActionStatusFailed,
		Event:           event,
		ResponseMessage: taskWorkflowFailureMessage(err),
		Report:          true,
	}
}

func taskWorkflowFailureMessage(err error) string {
	detail := taskWorkflowFailureDetail(err)
	if detail == "" {
		return "I couldn't complete the task because the task workflow failed."
	}
	return "I couldn't complete the task: " + detail
}

func taskWorkflowFailureDetail(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "contextwindowexceeded") || strings.Contains(lower, "context window") || strings.Contains(lower, "input tokens exceed") {
		return "the model context window was exceeded. Please reduce the amount of context and try again."
	}
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxDetailLength = 500
	if len(text) > maxDetailLength {
		text = text[:maxDetailLength] + "..."
	}
	return "the task workflow failed (" + text + ")."
}

func handleProjectEventSignal(ctx workflow.Context, state *ProjectWorkflowState, active map[string]activeTask, projectID string, event domain.Event) {
	eventID := strings.TrimSpace(event.ID)
	if eventID != "" && state.SeenEventIDs[eventID] {
		return
	}
	if eventID != "" {
		state.SeenEventIDs[eventID] = true
	}
	if scheduled.IsScheduledTaskEvent(event) {
		if scheduledTaskOverlaps(state, active, event) {
			return
		}
		state.Queue = append(state.Queue, event)
		return
	}
	if len(active) == 0 {
		state.Queue = append(state.Queue, event)
		return
	}

	_, targetWorkflowID := firstActiveTask(state.ActiveTasks)
	if targetWorkflowID == "" {
		state.Queue = append(state.Queue, event)
		return
	}
	switch projectControlAction(event) {
	case "cancel":
		_ = workflow.SignalExternalWorkflow(ctx, targetWorkflowID, "", SignalTaskCancel, TaskControlSignal{Event: event, Reason: "project message requested cancel"}).Get(ctx, nil)
	case "interrupt":
		_ = workflow.SignalExternalWorkflow(ctx, targetWorkflowID, "", SignalTaskInterrupt, TaskControlSignal{Event: event, Reason: "project message requested interrupt"}).Get(ctx, nil)
	default:
		_ = workflow.SignalExternalWorkflow(ctx, targetWorkflowID, "", SignalTaskAdditionalContext, AdditionalContextSignal{Event: event}).Get(ctx, nil)
	}
}

func scheduledTaskOverlaps(state *ProjectWorkflowState, active map[string]activeTask, event domain.Event) bool {
	scheduleID := scheduled.ScheduleID(event)
	if scheduleID == "" {
		return false
	}
	for _, queued := range state.Queue {
		if scheduled.ScheduleID(queued) == scheduleID {
			return true
		}
	}
	for _, task := range active {
		if scheduled.ScheduleID(task.Event) == scheduleID {
			return true
		}
	}
	return false
}

func taskWorkflowID(projectID, eventID string) string {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		eventID = "event"
	}
	return fmt.Sprintf("%s:task:%s", projectID, eventID)
}

func firstActiveTask(active map[string]string) (string, string) {
	for eventID, workflowID := range active {
		return eventID, workflowID
	}
	return "", ""
}

func projectControlAction(event domain.Event) string {
	if event.Metadata != nil {
		switch strings.ToLower(strings.TrimSpace(event.Metadata["control"])) {
		case "cancel", "stop":
			return "cancel"
		case "interrupt":
			return "interrupt"
		}
	}
	switch strings.ToLower(strings.TrimSpace(event.Body)) {
	case "cancel", "/cancel", "stop", "/stop":
		return "cancel"
	case "interrupt", "/interrupt":
		return "interrupt"
	default:
		return ""
	}
}
