package runtime

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/signals"
	"github.com/opencto/opencto/internal/runtime/workflows"
)

type Dispatcher struct {
	client                   client.Client
	taskQueue                string
	continueAsNewAfterEvents int
}

func NewDispatcher(client client.Client, taskQueue string, continueAsNewAfterEvents int) *Dispatcher {
	return &Dispatcher{
		client:                   client,
		taskQueue:                taskQueue,
		continueAsNewAfterEvents: continueAsNewAfterEvents,
	}
}

func WorkflowID(projectID string) string {
	return fmt.Sprintf("%s:project", projectID)
}

func (d *Dispatcher) EnsureProjectWorkflow(ctx context.Context, projectID string) error {
	_, err := d.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID:                    WorkflowID(projectID),
		TaskQueue:             d.taskQueue,
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_ALLOW_DUPLICATE_FAILED_ONLY,
	}, workflows.ProjectWorkflowName, workflows.ProjectWorkflowInput{
		ProjectID:                projectID,
		ContinueAsNewAfterEvents: d.continueAsNewAfterEvents,
	})
	if err == nil {
		return nil
	}
	var alreadyStarted *serviceerror.WorkflowExecutionAlreadyStarted
	if errors.As(err, &alreadyStarted) {
		return nil
	}
	return err
}

func (d *Dispatcher) EnqueueEvent(ctx context.Context, event domain.Event) error {
	if err := d.EnsureProjectWorkflow(ctx, event.ProjectID); err != nil {
		return err
	}
	return d.client.SignalWorkflow(ctx, WorkflowID(event.ProjectID), "", workflows.SignalEnqueueEvent, signals.EnqueueEventSignal{
		Event: event,
	})
}

func (d *Dispatcher) SubmitApprovalDecision(ctx context.Context, signal signals.ApprovalDecisionSignal) error {
	if err := d.EnsureProjectWorkflow(ctx, signal.ProjectID); err != nil {
		return err
	}
	return d.client.SignalWorkflow(ctx, WorkflowID(signal.ProjectID), "", workflows.SignalApprovalDecision, signal)
}

func (d *Dispatcher) SubmitContradictionResolution(ctx context.Context, signal signals.ContradictionResolutionSignal) error {
	if err := d.EnsureProjectWorkflow(ctx, signal.ProjectID); err != nil {
		return err
	}
	return d.client.SignalWorkflow(ctx, WorkflowID(signal.ProjectID), "", workflows.SignalContradictionResolved, signal)
}
