package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"
)

func ContradictionWorkflow(ctx workflow.Context, signal ContradictionResolutionSignal) (bool, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return true, workflow.ExecuteActivity(ctx, "Activities.ResolveContradiction", signal).Get(ctx, nil)
}
