package workflows

import (
	"fmt"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
)

const maxExecutionCycles = 20

func TaskWorkflow(ctx workflow.Context, input TaskWorkflowInput) (TaskWorkflowResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	var decision agent.DecisionOutput
	if input.Decision != nil {
		decision = *input.Decision
	}
	var observationHistory []agent.ExecutionFeedback
	var lastResult *activities.ExecuteToolResult

	for cycle := 1; cycle <= maxExecutionCycles; cycle++ {
		next, err := nextAction(ctx, activities.NextActionRequest{
			ProjectID:          input.ProjectID,
			Event:              input.Event,
			Decision:           decision,
			LastResult:         lastResult,
			ObservationHistory: observationHistory,
			ExecutionCycle:     cycle,
			ResumedFromPause:   input.ResumedFromPause,
		})
		if err != nil {
			return TaskWorkflowResult{}, err
		}

		decision = next.Decision
		if next.Observation != nil {
			observationHistory = append(observationHistory, *next.Observation)
		}
		if next.IsTerminal() {
			return resultFromNextAction(next, decision), nil
		}
		if next.ToolChoice == nil {
			return TaskWorkflowResult{}, fmt.Errorf("Activities.NextAction returned non-terminal status %q without a tool choice", next.Status)
		}
		if strings.TrimSpace(next.WorkItemID) == "" {
			return TaskWorkflowResult{}, fmt.Errorf("Activities.NextAction returned a tool choice without a work item id")
		}

		var execResult activities.ExecuteToolResult
		err = workflow.ExecuteActivity(ctx, "Activities.ExecuteTool", activities.ExecuteToolRequest{
			ProjectID:  input.ProjectID,
			WorkItemID: next.WorkItemID,
			ToolChoice: *next.ToolChoice,
		}).Get(ctx, &execResult)
		if err != nil {
			execResult = failedExecutionActivityResult(*next.ToolChoice, next.WorkItemID, cycle, err)
		}
		lastResult = &execResult
	}

	final, err := nextAction(ctx, activities.NextActionRequest{
		ProjectID:          input.ProjectID,
		Event:              input.Event,
		Decision:           decision,
		LastResult:         lastResult,
		ObservationHistory: observationHistory,
		ExecutionCycle:     maxExecutionCycles + 1,
		ForceFinal:         true,
		ResumedFromPause:   input.ResumedFromPause,
	})
	if err != nil {
		return TaskWorkflowResult{}, err
	}
	if final.Observation != nil {
		observationHistory = append(observationHistory, *final.Observation)
	}
	if !final.IsTerminal() {
		return TaskWorkflowResult{}, fmt.Errorf("Activities.NextAction returned non-terminal status %q for force-final request", final.Status)
	}
	return resultFromNextAction(final, final.Decision), nil
}

func nextAction(ctx workflow.Context, request activities.NextActionRequest) (activities.NextActionResult, error) {
	var result activities.NextActionResult
	err := workflow.ExecuteActivity(ctx, "Activities.NextAction", request).Get(ctx, &result)
	return result, err
}

func resultFromNextAction(next activities.NextActionResult, decision agent.DecisionOutput) TaskWorkflowResult {
	return TaskWorkflowResult{
		Completed: next.Status == activities.NextActionStatusCompleted || next.Status == activities.NextActionStatusIgnored,
		Decision:  decision,
	}
}

func failedExecutionActivityResult(choice agent.ToolChoice, workItemID string, cycle int, err error) activities.ExecuteToolResult {
	message := "tool execution activity failed"
	if err != nil {
		message = err.Error()
	}
	return activities.ExecuteToolResult{
		Cycle:            cycle,
		WorkItemID:       workItemID,
		ToolCallID:       toolCallID(choice),
		Tool:             choice.Type,
		Status:           domain.ExecutionStatusFailed,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Input:            choice.Input,
		Observation:      "Tool execution failed before producing a structured result.",
		Error:            message,
		WorkingDirectory: choice.WorkingDir,
		ResultCode:       "activity_error",
		Metadata:         choice.Metadata,
	}
}

func toolCallID(choice agent.ToolChoice) string {
	if strings.TrimSpace(choice.ToolCallID) != "" {
		return strings.TrimSpace(choice.ToolCallID)
	}
	if choice.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(choice.Metadata["tool_call_id"])
}
