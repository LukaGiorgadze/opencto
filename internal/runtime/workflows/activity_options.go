package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
)

const (
	reportActivityStartToClose           = 15 * time.Second
	reportActivityScheduleToClose        = 30 * time.Second
	toolSelectionActivityStartToClose    = 3 * time.Minute
	toolSelectionActivityScheduleToClose = 7 * time.Minute
)

func reportResult(ctx workflow.Context, event domain.Event, message string) {
	reportCtx := workflow.WithActivityOptions(ctx, reportActivityOptions())
	_ = workflow.ExecuteActivity(reportCtx, "Activities.ReportResult", event, message).Get(reportCtx, nil)
}

func reportActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    reportActivityStartToClose,
		ScheduleToCloseTimeout: reportActivityScheduleToClose,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
}

func selectTool(ctx workflow.Context, request activities.ToolSelectionRequest) (activities.ToolSelectionResult, error) {
	selectionCtx := workflow.WithActivityOptions(ctx, toolSelectionActivityOptions())

	var selection activities.ToolSelectionResult
	err := workflow.ExecuteActivity(selectionCtx, "Activities.SelectTool", request).Get(selectionCtx, &selection)
	return selection, err
}

func toolSelectionActivityOptions() workflow.ActivityOptions {
	return workflow.ActivityOptions{
		StartToCloseTimeout:    toolSelectionActivityStartToClose,
		ScheduleToCloseTimeout: toolSelectionActivityScheduleToClose,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
			MaximumAttempts:    2,
		},
	}
}
