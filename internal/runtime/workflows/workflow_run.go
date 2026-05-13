package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func WorkflowRunWorkflow(ctx workflow.Context, input workflowrun.Input) error {
	if input.ScheduledAt.IsZero() {
		input.ScheduledAt = workflow.Now(ctx)
	}
	prepareCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})

	var prepared workflowrun.PrepareResult
	temporalWorkflowID := workflow.GetInfo(ctx).WorkflowExecution.ID
	if err := workflow.ExecuteActivity(prepareCtx, workflowrun.PrepareActivityName, workflowrun.PrepareRequest{
		Input:              input,
		TemporalWorkflowID: temporalWorkflowID,
	}).Get(ctx, &prepared); err != nil {
		return err
	}

	for _, step := range prepared.Manifest.Steps {
		stepCtx, err := workflowStepContext(ctx, step)
		if err != nil {
			return completeAndNotifyWorkflowFailure(ctx, input, prepared.RunID, prepared.RunPath, err.Error(), prepared.Manifest.NotificationPolicy.OnFailure)
		}
		var result workflowrun.ExecuteStepResult
		err = workflow.ExecuteActivity(stepCtx, workflowrun.ExecuteStepName, workflowrun.ExecuteStepRequest{
			ProjectID:  input.ProjectID,
			WorkflowID: input.WorkflowID,
			CommitHash: input.CommitHash,
			RunID:      prepared.RunID,
			RunPath:    prepared.RunPath,
			Step:       step,
			Env:        prepared.Manifest.Env,
		}).Get(ctx, &result)
		if err != nil {
			return completeAndNotifyWorkflowFailure(ctx, input, prepared.RunID, prepared.RunPath, err.Error(), prepared.Manifest.NotificationPolicy.OnFailure)
		}
	}

	return completeWorkflowRun(ctx, workflowrun.CompleteRequest{
		ProjectID:  input.ProjectID,
		WorkflowID: input.WorkflowID,
		RunID:      prepared.RunID,
		RunPath:    prepared.RunPath,
		Status:     domain.ExecutionStatusSucceeded,
	})
}

func workflowStepContext(ctx workflow.Context, step workflowbundle.Step) (workflow.Context, error) {
	startToClose, err := workflowbundle.ParseRequiredDuration("start_to_close_timeout", step.StartToCloseTimeout)
	if err != nil {
		return nil, err
	}
	scheduleToClose, err := workflowbundle.ParseOptionalDuration("schedule_to_close_timeout", step.ScheduleToCloseTimeout)
	if err != nil {
		return nil, err
	}
	initialInterval, err := workflowbundle.ParseOptionalDuration("retry_policy.initial_interval", step.RetryPolicy.InitialInterval)
	if err != nil {
		return nil, err
	}
	maximumInterval, err := workflowbundle.ParseOptionalDuration("retry_policy.maximum_interval", step.RetryPolicy.MaximumInterval)
	if err != nil {
		return nil, err
	}
	retryPolicy := &temporal.RetryPolicy{
		InitialInterval:        initialInterval,
		BackoffCoefficient:     step.RetryPolicy.BackoffCoefficient,
		MaximumInterval:        maximumInterval,
		MaximumAttempts:        step.RetryPolicy.MaximumAttempts,
		NonRetryableErrorTypes: step.RetryPolicy.NonRetryableErrorTypes,
	}
	if retryPolicy.InitialInterval <= 0 {
		retryPolicy.InitialInterval = time.Second
	}
	if retryPolicy.BackoffCoefficient <= 0 {
		retryPolicy.BackoffCoefficient = 2
	}
	options := workflow.ActivityOptions{
		StartToCloseTimeout: startToClose,
		RetryPolicy:         retryPolicy,
	}
	if scheduleToClose > 0 {
		options.ScheduleToCloseTimeout = scheduleToClose
	}
	return workflow.WithActivityOptions(ctx, options), nil
}

func completeAndNotifyWorkflowFailure(ctx workflow.Context, input workflowrun.Input, runID, runPath, failure string, notify bool) error {
	disconnected, _ := workflow.NewDisconnectedContext(ctx)
	_ = completeWorkflowRun(disconnected, workflowrun.CompleteRequest{
		ProjectID:      input.ProjectID,
		WorkflowID:     input.WorkflowID,
		RunID:          runID,
		RunPath:        runPath,
		Status:         domain.ExecutionStatusFailed,
		FailureSummary: failure,
	})
	if notify {
		notifyCtx := workflow.WithActivityOptions(disconnected, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		_ = workflow.ExecuteActivity(notifyCtx, workflowrun.NotifyFailureName, workflowrun.NotifyFailureRequest{
			Input:          input,
			RunID:          runID,
			FailureSummary: failure,
		}).Get(notifyCtx, nil)
	}
	return temporal.NewApplicationError(failure, "WorkflowRunFailed")
}

func completeWorkflowRun(ctx workflow.Context, request workflowrun.CompleteRequest) error {
	completeCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	return workflow.ExecuteActivity(completeCtx, workflowrun.CompleteActivityName, request).Get(completeCtx, nil)
}
