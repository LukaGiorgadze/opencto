package workflows

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

const (
	reportResponseActivityTimeout = time.Minute
	recentProjectEventIDLimit     = 1000
)

func ProjectWorkflow(ctx workflow.Context, input ProjectWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	state := ProjectWorkflowState{
		ProjectID: input.ProjectID,
	}
	if input.Snapshot != nil {
		state = *input.Snapshot
		trimRecentProjectEventIDs(&state)
	}

	eventSignal := workflow.GetSignalChannel(ctx, SignalEnqueueEvent)
	active := map[string]activeTask{}
	var pendingReports []TaskWorkflowResult

	for {
		for len(pendingReports) > 0 {
			report := pendingReports[0]
			pendingReports = pendingReports[1:]
			reportTaskResult(ctx, report)
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
			active[event.ID] = activeTask{Future: future, Event: event, WorkflowID: workflowID}
			continue
		}

		selector := workflow.NewSelector(ctx)
		selector.AddReceive(eventSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal EnqueueEventSignal
			c.Receive(ctx, &signal)
			handleProjectEventSignal(ctx, &state, active, signal.Event)
		})
		for eventID, task := range active {
			selector.AddFuture(task.Future, func(f workflow.Future) {
				var result TaskWorkflowResult
				if err := f.Get(ctx, &result); err != nil {
					result = failedTaskWorkflowResult(task.Event, err)
				}
				if result.Report {
					pendingReports = append(pendingReports, result)
				}
				delete(active, eventID)
				state.ProcessedEvents++
			})
		}
		selector.Select(ctx)
	}
}

type activeTask struct {
	Future     workflow.ChildWorkflowFuture
	Event      domain.Event
	WorkflowID string
}

func reportTaskResult(ctx workflow.Context, result TaskWorkflowResult) {
	report := domain.ReportMessage{
		Text:        result.ResponseMessage,
		Attachments: result.ResponseAttachments,
	}
	if !result.Report || report.Empty() {
		return
	}
	reportCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: reportResponseActivityTimeout,
		StartToCloseTimeout:    reportResponseActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})
	err := workflow.ExecuteActivity(reportCtx, "Activities.ReportResponse", activities.ReportResponseRequest{
		Event:       result.Event,
		Message:     report.Text,
		Attachments: report.Attachments,
	}).Get(reportCtx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Error(
			"report response activity failed",
			"project_id", result.Event.ProjectID,
			"event_id", result.Event.ID,
			"error", err.Error(),
		)
	}
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

func handleProjectEventSignal(ctx workflow.Context, state *ProjectWorkflowState, active map[string]activeTask, event domain.Event) {
	if rememberProjectEventID(state, event.ID) {
		return
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

	targetWorkflowID := firstActiveTaskWorkflowID(active)
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

func firstActiveTaskWorkflowID(active map[string]activeTask) string {
	for _, task := range active {
		return task.WorkflowID
	}
	return ""
}

func rememberProjectEventID(state *ProjectWorkflowState, eventID string) bool {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false
	}
	for _, recentID := range state.RecentEventIDs {
		if recentID == eventID {
			return true
		}
	}
	state.RecentEventIDs = append(state.RecentEventIDs, eventID)
	trimRecentProjectEventIDs(state)
	return false
}

func trimRecentProjectEventIDs(state *ProjectWorkflowState) {
	if len(state.RecentEventIDs) <= recentProjectEventIDLimit {
		return
	}
	start := len(state.RecentEventIDs) - recentProjectEventIDLimit
	state.RecentEventIDs = append([]string(nil), state.RecentEventIDs[start:]...)
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
