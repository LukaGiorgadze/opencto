# Execution Summary

Date: 2026-04-24T07:34:28Z

## Summary
Stopped after 20 execution cycles. Last observation: package workflows

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
	"github.com/opencto/opencto/internal/runtime/activities"
)

const maxExecutionCycles = 20

func TaskWorkflow(ctx workflow.Context, input TaskWorkflowInput) (TaskWorkflowResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	if !input.ResumedFromPause {
		if err := workflow.ExecuteActivity(ctx, "Activities.PersistEvent", input.Event).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}

		var loaded agent.Context
		if err := workflow.ExecuteActivity(ctx, "Activities.LoadContext", input.Event).Get(ctx, &loaded); err != nil {
			return TaskWorkflowResult{}, err
		}

		decisionInput := agent.DecisionInput{
			ProjectID: input.ProjectID,
			Context:   loaded,
		}

		var classification agent.Classification
		if err := workflow.ExecuteActivity(ctx, "Activities.Classify", decisionInput).Get(ctx, &classification); err != nil {
			return TaskWorkflowResult{}, err
		}

		if classification.RoutedTo == agent.ClassificationRouteIgnore {
			return TaskWorkflowResult{
				Completed: true,
...[output truncated]

## Details
- The request appears to be an incomplete draft, so the safest plan is to first inspect the repository and task sources to determine what the intended work actually is before making any changes. This avoids implementing the wrong thing from a truncated request.
