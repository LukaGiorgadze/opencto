package workflows_test

import (
	"errors"
	"strings"
	"testing"
	"time"

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
	env.RegisterActivityWithOptions((&activities.Activities{}).ResponseSession, activity.RegisterOptions{Name: "Activities.ResponseSession"})
}

func TestTaskWorkflowAlternatesNextActionAndExecuteTool(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	nextAction := agent.NextAction{
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
		NextAction: nextAction,
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
		NextAction:  nextAction,
		Observation: &agent.ExecutionFeedback{WorkItemID: "wi-1", ToolCallID: "toolu_1", Status: string(domain.ExecutionStatusSucceeded), Observation: "/tmp/opencto"},
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

func TestTaskWorkflowStartsResponseSessionForChannelEvent(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		Body:        "inspect workspace",
	}
	env.OnActivity("Activities.ResponseSession", mock.Anything, mock.MatchedBy(func(request activities.ResponseSessionRequest) bool {
		return request.ProjectID == "project-1" &&
			request.Event.ID == "event-1" &&
			request.Event.ChannelID == "channel-1"
	})).Return(nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPassesProcessesReturnedByExecuteToolToNextAction(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run dev server"}
	choice := agent.ToolChoice{
		ToolCallID:  "toolu_bg",
		Type:        domain.ToolTypeShell,
		Intent:      "start dev server",
		Command:     "npm",
		Args:        []string{"run", "dev"},
		RunMode:     domain.ToolRunModeStartBackground,
		Idempotency: domain.ToolIdempotencyNonIdempotent,
		Metadata: map[string]string{
			"tool_call_id":    "toolu_bg",
			"work_item_id":    "wi-1",
			"execution_cycle": "1",
		},
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoice: &choice,
		WorkItemID: "wi-1",
		Status:     activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.ToolCallID == "toolu_bg" && request.ToolChoice.RunMode == domain.ToolRunModeStartBackground
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start dev server",
		Command:         "npm",
		Args:            []string{"run", "dev"},
		Observation:     "Started background process.",
		Metadata: map[string]string{
			"tool_call_id":  "toolu_bg",
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeTask),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start dev server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeTask,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.LastResult != nil &&
			request.LastResult.Metadata["process_id"] == "proc-1" &&
			len(request.Processes) == 1 &&
			request.Processes[0].ID == "proc-1"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start dev server",
			Status:      domain.ProcessStatusStopped,
			Scope:       domain.ProcessScopeTask,
		}},
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

func TestTaskWorkflowKeepsProjectScopedBackgroundProcessRunning(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run persistent server"}
	choice := agent.ToolChoice{
		ToolCallID:   "toolu_bg",
		Type:         domain.ToolTypeShell,
		Intent:       "start persistent server",
		Command:      "server",
		RunMode:      domain.ToolRunModeStartBackground,
		Idempotency:  domain.ToolIdempotencyNonIdempotent,
		ProcessScope: domain.ProcessScopeProject,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoice: &choice,
		WorkItemID: "wi-1",
		Status:     activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start persistent server",
		Command:         "server",
		Observation:     "Started background process.",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeProject),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.Processes) == 1 &&
			request.Processes[0].ID == "proc-1" &&
			request.Processes[0].Scope == domain.ProcessScopeProject
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
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

func TestTaskWorkflowMarksIncompleteWhenTaskProcessCleanupFails(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run server"}
	choice := agent.ToolChoice{
		ToolCallID:  "toolu_bg",
		Type:        domain.ToolTypeShell,
		Intent:      "start task server",
		Command:     "server",
		RunMode:     domain.ToolRunModeStartBackground,
		Idempotency: domain.ToolIdempotencyNonIdempotent,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{},
		ToolChoice: &choice,
		WorkItemID: "wi-1",
		Status:     activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start task server",
		Command:         "server",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeTask),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start task server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeTask,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusFailed,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start task server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeTask,
		}},
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
	if result.Completed {
		t.Fatalf("expected incomplete result")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPreservesProjectProcessAfterNextActionError(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run persistent server"}
	choice := agent.ToolChoice{
		ToolCallID:   "toolu_bg",
		Type:         domain.ToolTypeShell,
		Intent:       "start persistent server",
		Command:      "server",
		RunMode:      domain.ToolRunModeStartBackground,
		Idempotency:  domain.ToolIdempotencyNonIdempotent,
		ProcessScope: domain.ProcessScopeProject,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{},
		ToolChoice: &choice,
		WorkItemID: "wi-1",
		Status:     activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start persistent server",
		Command:         "server",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeProject),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{}, errors.New("next action failed")).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.Completion != nil &&
			len(request.Completion.Processes) == 1 &&
			request.Completion.Processes[0].ID == "proc-1"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusFailed,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
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
	if result.Completed {
		t.Fatalf("expected incomplete result")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPassesAdditionalContextSignalToNextAction(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}
	additional := domain.Event{ID: "event-2", ProjectID: "project-1", Body: "also check tests"}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalTaskAdditionalContext, workflows.AdditionalContextSignal{Event: additional})
	}, 0)
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.AdditionalEvents) == 1 && request.AdditionalEvents[0].ID == "event-2"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowSignalsActiveTaskWithAdditionalContext(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		var signal workflows.AdditionalContextSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
		received = signal.Event.ID == "event-2"
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{ID: "event-2", ProjectID: "project-1", Body: "extra context"}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !received {
		t.Fatalf("expected active child task to receive additional context signal")
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
