package activities

import (
	"context"

	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

func (a *Activities) CleanupWorkflowAuthoring(ctx context.Context, plan scheduletool.AuthoringPlan) error {
	executor, err := a.workflowAuthoringExecutor()
	if err != nil {
		return err
	}
	return executor.CleanupAuthoring(ctx, plan)
}
