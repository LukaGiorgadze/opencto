package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

func ProjectWorkflow(ctx workflow.Context, input ProjectWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	state := ProjectWorkflowState{
		ProjectID: input.ProjectID,
	}
	if input.Snapshot != nil {
		state = *input.Snapshot
	}

	if err := workflow.SetQueryHandler(ctx, QueryProjectState, func() (ProjectWorkflowState, error) {
		return state, nil
	}); err != nil {
		return err
	}

	eventSignal := workflow.GetSignalChannel(ctx, SignalEnqueueEvent)

	for {
		state.UpdatedAtUnixNano = workflow.Now(ctx).UnixNano()
		if input.ContinueAsNewAfterEvents > 0 && state.ProcessedEvents >= input.ContinueAsNewAfterEvents {
			snapshot := state
			return workflow.NewContinueAsNewError(ctx, ProjectWorkflow, ProjectWorkflowInput{
				ProjectID:                input.ProjectID,
				ContinueAsNewAfterEvents: input.ContinueAsNewAfterEvents,
				Snapshot:                 &snapshot,
			})
		}

		if state.ActiveTaskID == "" && len(state.Queue) > 0 {
			event := state.Queue[0]
			state.Queue = state.Queue[1:]
			state.ActiveTaskID = event.ID
			var result TaskWorkflowResult
			err := workflow.ExecuteChildWorkflow(ctx, TaskWorkflowName, TaskWorkflowInput{
				ProjectID: input.ProjectID,
				Event:     event,
			}).Get(ctx, &result)
			state.ActiveTaskID = ""
			state.ProcessedEvents++
			if err != nil {
				continue
			}
			continue
		}

		selector := workflow.NewSelector(ctx)
		selector.AddReceive(eventSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal EnqueueEventSignal
			c.Receive(ctx, &signal)
			state.Queue = append(state.Queue, signal.Event)
			state.UpdatedAtUnixNano = workflow.Now(ctx).UnixNano()
		})
		selector.Select(ctx)
	}
}
