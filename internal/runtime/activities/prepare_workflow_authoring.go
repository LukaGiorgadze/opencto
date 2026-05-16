package activities

import (
	"context"

	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

func (a *Activities) PrepareWorkflowAuthoring(ctx context.Context, request scheduletool.AuthoringRequest) (scheduletool.AuthoringPlan, error) {
	executor, err := a.workflowAuthoringExecutor()
	if err != nil {
		return scheduletool.AuthoringPlan{}, err
	}
	return executor.PrepareAuthoring(ctx, request)
}
