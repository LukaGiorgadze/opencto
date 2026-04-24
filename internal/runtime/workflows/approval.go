package workflows

import (
	"time"

	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
)

func ApprovalWorkflow(ctx workflow.Context, signal ApprovalDecisionSignal) (domain.ApprovalRequest, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	var approval domain.ApprovalRequest
	err := workflow.ExecuteActivity(ctx, "Activities.ResolveApproval", signal).Get(ctx, &approval)
	return approval, err
}
