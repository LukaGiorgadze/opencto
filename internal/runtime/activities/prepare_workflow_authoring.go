package activities

import (
	"context"

	"go.temporal.io/sdk/temporal"

	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

const workflowAuthoringConfigurationErrorType = "WorkflowAuthoringConfiguration"

func (a *Activities) PrepareWorkflowAuthoring(ctx context.Context, request scheduletool.AuthoringRequest) (scheduletool.AuthoringPlan, error) {
	executor, err := a.workflowAuthoringExecutor()
	if err != nil {
		return scheduletool.AuthoringPlan{}, temporal.NewNonRetryableApplicationError(err.Error(), workflowAuthoringConfigurationErrorType, err)
	}
	plan, err := executor.PrepareAuthoring(ctx, request)
	if err != nil {
		if scheduletool.IsNonRetryableAuthoringError(err) {
			return scheduletool.AuthoringPlan{}, temporal.NewNonRetryableApplicationError(err.Error(), scheduletool.WorkflowAuthoringValidationErrorType, err)
		}
		return scheduletool.AuthoringPlan{}, err
	}
	return plan, nil
}
