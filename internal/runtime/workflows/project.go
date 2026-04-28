package workflows

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
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
	active := map[string]workflow.ChildWorkflowFuture{}

	for {
		state.UpdatedAtUnixNano = workflow.Now(ctx).UnixNano()
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
				state.ProcessedEvents++
				continue
			}
			active[event.ID] = future
			state.ActiveTasks[event.ID] = workflowID
			state.ActiveTaskID = event.ID
			continue
		}

		selector := workflow.NewSelector(ctx)
		selector.AddReceive(eventSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal EnqueueEventSignal
			c.Receive(ctx, &signal)
			state.UpdatedAtUnixNano = workflow.Now(ctx).UnixNano()
			handleProjectEventSignal(ctx, &state, active, input.ProjectID, signal.Event)
		})
		for eventID, future := range active {
			eventID := eventID
			selector.AddFuture(future, func(f workflow.Future) {
				var result TaskWorkflowResult
				_ = f.Get(ctx, &result)
				delete(active, eventID)
				delete(state.ActiveTasks, eventID)
				if state.ActiveTaskID == eventID {
					state.ActiveTaskID = ""
				}
				state.ProcessedEvents++
				state.UpdatedAtUnixNano = workflow.Now(ctx).UnixNano()
			})
		}
		selector.Select(ctx)
	}
}

func handleProjectEventSignal(ctx workflow.Context, state *ProjectWorkflowState, active map[string]workflow.ChildWorkflowFuture, projectID string, event domain.Event) {
	eventID := strings.TrimSpace(event.ID)
	if eventID != "" && state.SeenEventIDs[eventID] {
		return
	}
	if eventID != "" {
		state.SeenEventIDs[eventID] = true
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
