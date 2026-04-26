package workflows

import (
	"errors"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
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
				reportTaskWorkflowFailure(ctx, event, err)
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

func reportTaskWorkflowFailure(ctx workflow.Context, event domain.Event, err error) {
	if err == nil {
		return
	}
	_ = ctx
	_ = event
	_ = rootCauseErrorMessage(err)
}

func rootCauseErrorMessage(err error) string {
	if err == nil {
		return ""
	}

	current := err
	message := err.Error()
	for {
		next := errors.Unwrap(current)
		if next == nil {
			break
		}
		current = next
		if trimmed := current.Error(); trimmed != "" {
			message = trimmed
		}
	}
	return message
}
