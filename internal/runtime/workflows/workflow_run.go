package workflows

import (
	"errors"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

const defaultWorkflowStepMaximumAttempts int32 = 3

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
	workflowExecution := workflow.GetInfo(ctx).WorkflowExecution
	if err := workflow.ExecuteActivity(prepareCtx, workflowrun.PrepareActivityName, workflowrun.PrepareRequest{
		Input:              input,
		TemporalWorkflowID: workflowExecution.ID,
		TemporalRunID:      workflowExecution.RunID,
	}).Get(ctx, &prepared); err != nil {
		return err
	}

	cleanupOldWorkflowRuns(ctx, input.WorkflowID, prepared.RunID)

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
			return completeAndNotifyWorkflowFailure(ctx, input, prepared.RunID, prepared.RunPath, workflowFailureMessage(err), prepared.Manifest.NotificationPolicy.OnFailure)
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
	retryPolicy := workflowStepRetryPolicy(step, initialInterval, maximumInterval)
	options := workflow.ActivityOptions{
		StartToCloseTimeout: startToClose,
		RetryPolicy:         retryPolicy,
	}
	if scheduleToClose > 0 {
		options.ScheduleToCloseTimeout = scheduleToClose
	}
	return workflow.WithActivityOptions(ctx, options), nil
}

func workflowStepRetryPolicy(step workflowbundle.Step, initialInterval, maximumInterval time.Duration) *temporal.RetryPolicy {
	maximumAttempts := step.RetryPolicy.MaximumAttempts
	if maximumAttempts <= 0 {
		maximumAttempts = defaultWorkflowStepMaximumAttempts
	}
	retryPolicy := &temporal.RetryPolicy{
		InitialInterval:        initialInterval,
		BackoffCoefficient:     step.RetryPolicy.BackoffCoefficient,
		MaximumInterval:        maximumInterval,
		MaximumAttempts:        maximumAttempts,
		NonRetryableErrorTypes: step.RetryPolicy.NonRetryableErrorTypes,
	}
	if retryPolicy.InitialInterval <= 0 {
		retryPolicy.InitialInterval = time.Second
	}
	if retryPolicy.BackoffCoefficient <= 0 {
		retryPolicy.BackoffCoefficient = 2
	}
	return retryPolicy
}

func cleanupOldWorkflowRuns(ctx workflow.Context, workflowID, currentRunID string) {
	cleanupCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 3,
		},
	})
	var result workflowrun.CleanupRunsResult
	err := workflow.ExecuteActivity(cleanupCtx, workflowrun.CleanupRunsActivityName, workflowrun.CleanupRunsRequest{
		WorkflowID:   workflowID,
		CurrentRunID: currentRunID,
		KeepLast:     workflowrun.DefaultRunRetention,
	}).Get(cleanupCtx, &result)
	if err != nil {
		workflow.GetLogger(ctx).Warn("workflow run cleanup failed", "error", err)
	}
}

func workflowFailureMessage(err error) string {
	if err == nil {
		return ""
	}
	var applicationErr *temporal.ApplicationError
	if errors.As(err, &applicationErr) {
		if message := strings.TrimSpace(applicationErr.Message()); message != "" {
			return message
		}
	}
	return strings.TrimSpace(err.Error())
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
