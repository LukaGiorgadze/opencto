package workflows_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/workflows"
)

func registerTaskWorkflowActivities(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions((&activities.Activities{}).NextAction, activity.RegisterOptions{Name: "Activities.NextAction"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ExecuteTool, activity.RegisterOptions{Name: "Activities.ExecuteTool"})
}

func TestTaskWorkflowAlternatesNextActionAndExecuteTool(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	decision := agent.DecisionOutput{
		WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}},
	}
	toolChoice := agent.ToolChoice{
		ToolCallID: "toolu_1",
		Type:       domain.ToolTypeShell,
		Intent:     "print working directory",
		Command:    "pwd",
		Metadata: map[string]string{
			"tool_call_id": "toolu_1",
			"work_item_id": "wi-1",
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 &&
			request.LastResult == nil &&
			len(request.ObservationHistory) == 0
	})).Return(activities.NextActionResult{
		Decision:   decision,
		ToolChoice: &toolChoice,
		WorkItemID: "wi-1",
		Status:     activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-1" &&
			request.ToolChoice.ToolCallID == "toolu_1" &&
			request.ToolChoice.Metadata["tool_call_id"] == "toolu_1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_1",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "print working directory",
		Command:         "pwd",
		Observation:     "/tmp/opencto",
		Metadata: map[string]string{
			"tool_call_id": "toolu_1",
		},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			request.LastResult != nil &&
			request.LastResult.ToolCallID == "toolu_1" &&
			request.LastResult.Observation == "/tmp/opencto" &&
			len(request.ObservationHistory) == 0
	})).Return(activities.NextActionResult{
		Decision:    decision,
		Observation: &agent.ExecutionFeedback{WorkItemID: "wi-1", ToolCallID: "toolu_1", Status: string(domain.ExecutionStatusSucceeded), Observation: "/tmp/opencto"},
		FinalAnswer: "done",
		Status:      activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected workflow to complete")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowRejectsNonTerminalNextActionWithoutTool(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusTool,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "without a tool choice") {
		t.Fatalf("expected missing tool choice error, got %v", err)
	}
}

func TestProjectWorkflowKeepsRunningAfterTaskFailure(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	taskErr := errors.New("task failed")
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, _ workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		return workflows.TaskWorkflowResult{}, taskErr
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: event})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 0)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
}
