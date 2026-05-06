package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/runtime/scheduled"
)

func ScheduledDispatchWorkflow(ctx workflow.Context, input scheduled.DispatchWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	scheduledAt := workflow.Now(ctx)
	workflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	event := scheduled.EventFromDispatch(input, scheduled.EventID(input.ScheduleID, workflowID, scheduledAt), scheduledAt)
	return workflow.ExecuteActivity(ctx, scheduled.EnqueueScheduledEventName, scheduled.EnqueueScheduledEventRequest{
		Event: event,
	}).Get(ctx, nil)
}
