package workflows

import (
	"errors"
	"fmt"
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
)

func ProjectWorkflow(ctx workflow.Context, input ProjectWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	state := ProjectWorkflowState{
		ProjectID:        input.ProjectID,
		PausedByApproval: map[string]PausedTaskState{},
	}
	if input.Snapshot != nil {
		state = *input.Snapshot
		if state.PausedByApproval == nil {
			state.PausedByApproval = map[string]PausedTaskState{}
		}
	}

	if err := workflow.SetQueryHandler(ctx, QueryProjectState, func() (ProjectWorkflowState, error) {
		return state, nil
	}); err != nil {
		return err
	}

	eventSignal := workflow.GetSignalChannel(ctx, SignalEnqueueEvent)
	approvalSignal := workflow.GetSignalChannel(ctx, SignalApprovalDecision)
	contradictionSignal := workflow.GetSignalChannel(ctx, SignalContradictionResolved)

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

		if state.ActiveTaskID == "" && len(state.ResumeQueue) > 0 {
			paused := state.ResumeQueue[0]
			state.ResumeQueue = state.ResumeQueue[1:]
			state.ActiveTaskID = paused.Event.ID
			var result TaskWorkflowResult
			err := workflow.ExecuteChildWorkflow(ctx, TaskWorkflowName, TaskWorkflowInput{
				ProjectID:        input.ProjectID,
				Event:            paused.Event,
				ResumedFromPause: true,
				ApprovalID:       paused.ApprovalID,
				ApprovalRequest:  paused.ApprovalRequest,
				Decision:         &paused.Decision,
			}).Get(ctx, &result)
			state.ActiveTaskID = ""
			state.ProcessedEvents++
			if err != nil {
				reportTaskWorkflowFailure(ctx, paused.Event, err)
				continue
			}
			continue
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
			if result.AwaitingApproval && result.ApprovalRequest != nil {
				state.PausedByApproval[result.ApprovalRequest.ID] = PausedTaskState{
					ApprovalID:      result.ApprovalRequest.ID,
					ApprovalRequest: result.ApprovalRequest,
					Event:           event,
					Decision:        result.Decision,
				}
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
		selector.AddReceive(approvalSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal ApprovalDecisionSignal
			c.Receive(ctx, &signal)
			paused, ok := state.PausedByApproval[signal.ApprovalID]
			var approval domain.ApprovalRequest
			if err := workflow.ExecuteChildWorkflow(ctx, ApprovalWorkflowName, signal).Get(ctx, &approval); err != nil {
				if ok {
					message := fmt.Sprintf("I couldn't record approval `%s`: %s", signal.ApprovalID, rootCauseErrorMessage(err))
					_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", paused.Event, message).Get(ctx, nil)
				}
				return
			}
			if !signal.Approved {
				if ok {
					message := fmt.Sprintf("Approval `%s` was rejected by %s.", signal.ApprovalID, signal.ActorName)
					if signal.Comment != "" {
						message += " Comment: " + signal.Comment
					}
					_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", paused.Event, message).Get(ctx, nil)
				}
				delete(state.PausedByApproval, signal.ApprovalID)
				return
			}
			if !ok {
				return
			}
			delete(state.PausedByApproval, signal.ApprovalID)
			paused.ApprovalRequest = &approval
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", paused.Event, fmt.Sprintf("Approval `%s` was accepted by %s. Resuming task.", signal.ApprovalID, signal.ActorName)).Get(ctx, nil)
			state.ResumeQueue = append(state.ResumeQueue, paused)
		})
		selector.AddReceive(contradictionSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal ContradictionResolutionSignal
			c.Receive(ctx, &signal)
			var ignored bool
			_ = workflow.ExecuteChildWorkflow(ctx, ContradictionWorkflowName, signal).Get(ctx, &ignored)
		})
		selector.Select(ctx)
	}
}

func reportTaskWorkflowFailure(ctx workflow.Context, event domain.Event, err error) {
	if err == nil {
		return
	}
	message := fmt.Sprintf("I couldn't complete this request because the agent failed: %s", rootCauseErrorMessage(err))
	_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, message).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", event, message).Get(ctx, nil)
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
