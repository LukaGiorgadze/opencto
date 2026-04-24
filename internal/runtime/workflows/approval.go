package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

func ApprovalWorkflow(ctx workflow.Context, signal ApprovalDecisionSignal) (bool, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return true, workflow.ExecuteActivity(ctx, "Activities.ResolveApproval", signal).Get(ctx, nil)
}
