package workflows_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	sdkworkflow "go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/signals"
	"github.com/opencto/opencto/internal/runtime/workflows"
)

func TestProjectWorkflowStoresPausedTaskOnApproval(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)

	event := domain.Event{
		ID:        "event-1",
		ProjectID: "project-1",
		Body:      "deploy this change",
	}
	decision := agent.DecisionOutput{
		Plan: domain.Plan{
			ID:        "plan-1",
			ProjectID: "project-1",
			EventID:   event.ID,
		},
	}
	approval := domain.ApprovalRequest{
		ID:         "approval-1",
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		Status:     domain.ApprovalStatusPending,
	}
	env.RegisterWorkflowWithOptions(func(_ sdkworkflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		if input.ResumedFromPause {
			return workflows.TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}
		return workflows.TaskWorkflowResult{
			AwaitingApproval: true,
			ApprovalRequest:  &approval,
			Decision:         decision,
		}, nil
	}, sdkworkflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, signals.EnqueueEventSignal{Event: event})
	}, 0)
	env.RegisterDelayedCallback(func() {
		value, err := env.QueryWorkflow(workflows.QueryProjectState)
		if err != nil {
			t.Fatalf("query workflow: %v", err)
		}
		var state workflows.ProjectWorkflowState
		if err := value.Get(&state); err != nil {
			t.Fatalf("decode query result: %v", err)
		}
		if len(state.PausedByApproval) != 1 {
			t.Fatalf("expected 1 paused task, got %d", len(state.PausedByApproval))
		}
		if state.ActiveTaskID != "" {
			t.Fatalf("expected mutex to be released, active task was %q", state.ActiveTaskID)
		}
		env.CancelWorkflow()
	}, time.Second)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{
		ProjectID:                "project-1",
		ContinueAsNewAfterEvents: 10,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
}

func TestProjectWorkflowReportsTaskFailureAndKeepsRunning(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-err",
		ProjectID: "project-1",
		Body:      "find nexus on this laptop",
	}
	rootErr := errors.New("API returned unexpected status code: 400: Invalid schema for function 'Shell'")
	taskErr := fmt.Errorf("child workflow execution error: %w", rootErr)

	env.RegisterWorkflowWithOptions(func(_ sdkworkflow.Context, _ workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		return workflows.TaskWorkflowResult{}, taskErr
	}, sdkworkflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, mock.MatchedBy(func(message string) bool {
		return strings.Contains(message, "I couldn't complete this request because the agent failed:") &&
			strings.Contains(message, rootErr.Error()) &&
			!strings.Contains(message, "child workflow execution error")
	})).Return(nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, mock.MatchedBy(func(message string) bool {
		return strings.Contains(message, "I couldn't complete this request because the agent failed:") &&
			strings.Contains(message, rootErr.Error()) &&
			!strings.Contains(message, "child workflow execution error")
	})).Return(nil)

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, signals.EnqueueEventSignal{Event: event})
	}, 0)
	env.RegisterDelayedCallback(func() {
		value, err := env.QueryWorkflow(workflows.QueryProjectState)
		if err != nil {
			t.Fatalf("query workflow: %v", err)
		}
		var state workflows.ProjectWorkflowState
		if err := value.Get(&state); err != nil {
			t.Fatalf("decode query result: %v", err)
		}
		if state.ProcessedEvents != 1 {
			t.Fatalf("expected failed event to count as processed, got %d", state.ProcessedEvents)
		}
		if state.ActiveTaskID != "" {
			t.Fatalf("expected active task to be cleared, got %q", state.ActiveTaskID)
		}
		env.CancelWorkflow()
	}, time.Second)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{
		ProjectID:                "project-1",
		ContinueAsNewAfterEvents: 10,
	})

	if !env.IsWorkflowCompleted() {
		t.Fatalf("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
}

func TestTaskWorkflowResumesAfterApproval(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	decision := agent.DecisionOutput{
		Plan: domain.Plan{
			ID:        "plan-1",
			ProjectID: "project-1",
			Summary:   "Run one command.",
		},
		ToolChoice: agent.ToolChoice{
			Type:         domain.ToolTypeShell,
			Intent:       "run test command",
			InputSummary: "run test command",
		},
		WorkItems: []domain.WorkItem{
			{
				ID:        "work-item-1",
				ProjectID: "project-1",
				RiskTier:  domain.RiskTierConsequential,
			},
		},
	}
	event := domain.Event{
		ID:        "event-1",
		ProjectID: "project-1",
		Body:      "run test command",
	}

	env.OnActivity("Activities.RevalidateApproval", mock.Anything, "project-1", "approval-1").Return(domain.ApprovalRequest{
		ID:        "approval-1",
		ProjectID: "project-1",
		Status:    domain.ApprovalStatusApproved,
	}, nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(3)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "work-item-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(decision agent.DecisionOutput) bool {
		return decision.ToolChoice.Metadata["execution_cycle"] == "1" &&
			decision.ToolChoice.Metadata["work_item_id"] == "work-item-1" &&
			len(decision.WorkItems) == 1 &&
			decision.WorkItems[0].Status == domain.WorkItemStatusRunning
	})).Return(activities.ExecuteToolResult{
		Observation: activities.ToolObservation{
			Status:  string(domain.ExecutionStatusSucceeded),
			Summary: "done",
		},
		Invocation: domain.ToolInvocation{
			ChosenTool:      domain.ToolTypeShell,
			RequestedIntent: "run test command",
			Metadata: map[string]string{
				"work_item_id":    "work-item-1",
				"execution_cycle": "1",
			},
		},
		Attempt: domain.ExecutionAttempt{
			Status:     domain.ExecutionStatusSucceeded,
			Attempt:    1,
			WorkItemID: "work-item-1",
		},
	}, nil)
	replyDecision := decision
	replyDecision.Plan.Status = domain.PlanStatusReady
	replyDecision.WorkItems = []domain.WorkItem{{
		ID:        "work-item-1",
		ProjectID: "project-1",
		RiskTier:  domain.RiskTierConsequential,
		Status:    domain.WorkItemStatusRunning,
	}}
	replyDecision.ToolChoice = agent.ToolChoice{ResponseMessage: "done"}
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, mock.Anything, mock.MatchedBy(func(feedback *agent.ExecutionFeedback) bool {
		return feedback != nil &&
			feedback.Cycle == 1 &&
			feedback.WorkItemID == "work-item-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "done"
	}), 2, mock.Anything).Return(replyDecision, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, "done").Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", "done", []string{"Run one command."}).Return(domain.ADR{}, nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, "done").Return(nil)

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID:        "project-1",
		Event:            event,
		ResumedFromPause: true,
		ApprovalID:       "approval-1",
		Decision:         &decision,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}

	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected task workflow to complete")
	}
}

func TestTaskWorkflowRequestsApprovalWhenPolicyRequiresIt(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-1",
		ProjectID: "project-1",
		Body:      "install dependency",
	}
	decision := agent.DecisionOutput{
		Classification: agent.Classification{Intent: agent.ClassificationIntentActionRequest, RoutedTo: agent.ClassificationRoutePlan},
		Plan: domain.Plan{
			ID:        "plan-1",
			ProjectID: "project-1",
			EventID:   event.ID,
		},
		WorkItems: []domain.WorkItem{{ID: "work-item-1", ProjectID: "project-1"}},
		ToolChoice: agent.ToolChoice{
			Type:   domain.ToolTypeShell,
			Intent: "install dependency",
		},
	}
	approval := domain.ApprovalRequest{
		ID:        "approval-1",
		ProjectID: "project-1",
		Status:    domain.ApprovalStatusPending,
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}, nil)
	env.OnActivity("Activities.Plan", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}).Return(decision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, decision, (*agent.ExecutionFeedback)(nil), 1, mock.Anything).Return(decision, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "work-item-1"
	})).Return(policy.Result{
		Allowed:          true,
		RequiresApproval: true,
		Tier:             domain.RiskTierConsequential,
		Reasons:          []string{"network egress requested"},
	}, nil)
	env.OnActivity("Activities.CreateApprovalRequest", mock.Anything, mock.Anything, policy.Result{
		Allowed:          true,
		RequiresApproval: true,
		Tier:             domain.RiskTierConsequential,
		Reasons:          []string{"network egress requested"},
	}).Return(approval, nil)

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
	if !result.AwaitingApproval || result.ApprovalRequest == nil {
		t.Fatalf("expected approval request")
	}
}

func TestTaskWorkflowContinuesAcrossExecutionCycles(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-loop",
		ProjectID: "project-1",
		Body:      "create a very simple landing page in something",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "Create a simple landing page.",
	}
	plannedDecision := agent.DecisionOutput{
		Classification: classification,
		Plan: domain.Plan{
			ID:        "plan-loop",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Create the landing page in iterative steps.",
		},
		WorkItems: []domain.WorkItem{
			{ID: "wi-1", ProjectID: "project-1", Title: "Inspect folder", Status: domain.WorkItemStatusReady},
			{ID: "wi-2", ProjectID: "project-1", Title: "Create page", Status: domain.WorkItemStatusReady},
		},
	}
	firstToolDecision := plannedDecision
	firstToolDecision.WorkItems = []domain.WorkItem{
		{ID: "wi-1", ProjectID: "project-1", Title: "Inspect folder", Status: domain.WorkItemStatusReady},
		{ID: "wi-2", ProjectID: "project-1", Title: "Create page", Status: domain.WorkItemStatusReady},
	}
	firstToolDecision.ToolChoice = agent.ToolChoice{
		Type:   domain.ToolTypeShell,
		Intent: "inspect the repository structure",
	}
	secondToolDecision := plannedDecision
	secondToolDecision.WorkItems = []domain.WorkItem{
		{ID: "wi-1", ProjectID: "project-1", Title: "Inspect folder", Status: domain.WorkItemStatusCompleted},
		{ID: "wi-2", ProjectID: "project-1", Title: "Create page", Status: domain.WorkItemStatusReady},
	}
	secondToolDecision.ToolChoice = agent.ToolChoice{
		Type:   domain.ToolTypeShell,
		Intent: "create the landing page files",
	}
	finalMessage := "Created the minimal OpenCTO landing page in `something` and verified the single hero content."
	replyDecision := plannedDecision
	replyDecision.WorkItems = []domain.WorkItem{
		{ID: "wi-1", ProjectID: "project-1", Title: "Inspect folder", Status: domain.WorkItemStatusCompleted},
		{ID: "wi-2", ProjectID: "project-1", Title: "Create page", Status: domain.WorkItemStatusRunning},
	}
	replyDecision.ToolChoice = agent.ToolChoice{
		ResponseMessage: finalMessage,
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, classification).Return(plannedDecision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, plannedDecision, (*agent.ExecutionFeedback)(nil), 1, mock.Anything).Return(firstToolDecision, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(6)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "wi-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(decision agent.DecisionOutput) bool {
		return decision.ToolChoice.Metadata["execution_cycle"] == "1" &&
			decision.ToolChoice.Metadata["work_item_id"] == "wi-1" &&
			len(decision.WorkItems) == 2 &&
			decision.WorkItems[0].Status == domain.WorkItemStatusRunning
	})).Return(activities.ExecuteToolResult{
		Observation: activities.ToolObservation{
			Status:  string(domain.ExecutionStatusSucceeded),
			Summary: "located the target folder and the page entry point",
		},
		Invocation: domain.ToolInvocation{
			ChosenTool:      domain.ToolTypeShell,
			RequestedIntent: "inspect the repository structure",
		},
		Attempt: domain.ExecutionAttempt{
			Status:     domain.ExecutionStatusSucceeded,
			Attempt:    1,
			WorkItemID: "wi-1",
		},
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, mock.Anything, mock.MatchedBy(func(feedback *agent.ExecutionFeedback) bool {
		return feedback != nil &&
			feedback.Cycle == 1 &&
			feedback.WorkItemID == "wi-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "located the target folder and the page entry point"
	}), 2, mock.Anything).Return(secondToolDecision, nil)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "2" && choice.Metadata["work_item_id"] == "wi-2"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(decision agent.DecisionOutput) bool {
		return decision.ToolChoice.Metadata["execution_cycle"] == "2" &&
			decision.ToolChoice.Metadata["work_item_id"] == "wi-2" &&
			len(decision.WorkItems) == 2 &&
			decision.WorkItems[0].Status == domain.WorkItemStatusCompleted &&
			decision.WorkItems[1].Status == domain.WorkItemStatusRunning
	})).Return(activities.ExecuteToolResult{
		Observation: activities.ToolObservation{
			Status:  string(domain.ExecutionStatusSucceeded),
			Summary: "landing page files created",
		},
		Invocation: domain.ToolInvocation{
			ChosenTool:      domain.ToolTypeShell,
			RequestedIntent: "create the landing page files",
		},
		Attempt: domain.ExecutionAttempt{
			Status:     domain.ExecutionStatusSucceeded,
			Attempt:    2,
			WorkItemID: "wi-2",
		},
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, mock.Anything, mock.MatchedBy(func(feedback *agent.ExecutionFeedback) bool {
		return feedback != nil &&
			feedback.Cycle == 2 &&
			feedback.WorkItemID == "wi-2" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "landing page files created"
	}), 3, mock.Anything).Return(replyDecision, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, finalMessage).Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", finalMessage, []string{"Create the landing page in iterative steps."}).Return(domain.ADR{}, nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, finalMessage).Return(nil)

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
		t.Fatalf("expected workflow to complete after multiple execution cycles")
	}
}

func TestTaskWorkflowCanAskForClarificationAfterInspectionCycle(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-clarify",
		ProjectID: "project-1",
		Body:      "create a landing page in something using codex cli",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "Create the landing page.",
	}
	plannedDecision := agent.DecisionOutput{
		Classification: classification,
		Plan: domain.Plan{
			ID:        "plan-clarify",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Inspect the repo, then create the landing page.",
		},
		WorkItems: []domain.WorkItem{
			{ID: "wi-1", ProjectID: "project-1", Title: "Inspect repo", Status: domain.WorkItemStatusReady},
		},
	}
	firstToolDecision := plannedDecision
	firstToolDecision.WorkItems = []domain.WorkItem{
		{ID: "wi-1", ProjectID: "project-1", Title: "Inspect repo", Status: domain.WorkItemStatusReady},
	}
	firstToolDecision.ToolChoice = agent.ToolChoice{
		Type:   domain.ToolTypeShell,
		Intent: "inspect the repository structure",
	}
	question := "I found the folder `something`, but there is no frontend entry point in this repo. Do you want a static HTML page or should I create a small web app first?"
	replyDecision := plannedDecision
	replyDecision.WorkItems = []domain.WorkItem{
		{ID: "wi-1", ProjectID: "project-1", Title: "Inspect repo", Status: domain.WorkItemStatusRunning},
	}
	replyDecision.ToolChoice = agent.ToolChoice{
		ResponseMessage: question,
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, classification).Return(plannedDecision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, plannedDecision, (*agent.ExecutionFeedback)(nil), 1, mock.Anything).Return(firstToolDecision, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(3)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "wi-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(decision agent.DecisionOutput) bool {
		return decision.ToolChoice.Metadata["execution_cycle"] == "1" &&
			decision.ToolChoice.Metadata["work_item_id"] == "wi-1" &&
			len(decision.WorkItems) == 1 &&
			decision.WorkItems[0].Status == domain.WorkItemStatusRunning
	})).Return(activities.ExecuteToolResult{
		Observation: activities.ToolObservation{
			Status:  string(domain.ExecutionStatusSucceeded),
			Summary: "folder something exists but no frontend entry point was found",
		},
		Invocation: domain.ToolInvocation{
			ChosenTool:      domain.ToolTypeShell,
			RequestedIntent: "inspect the repository structure",
		},
		Attempt: domain.ExecutionAttempt{
			Status:     domain.ExecutionStatusSucceeded,
			Attempt:    1,
			WorkItemID: "wi-1",
		},
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, mock.Anything, mock.MatchedBy(func(feedback *agent.ExecutionFeedback) bool {
		return feedback != nil &&
			feedback.Cycle == 1 &&
			feedback.WorkItemID == "wi-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "folder something exists but no frontend entry point was found"
	}), 2, mock.Anything).Return(replyDecision, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, question).Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", question, []string{"Inspect the repo, then create the landing page."}).Return(domain.ADR{}, nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, question).Return(nil)

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
		t.Fatalf("expected workflow to complete after asking for clarification")
	}
	if got := result.Decision.WorkItems[0].Status; got != domain.WorkItemStatusBlocked {
		t.Fatalf("expected work item to stay blocked while waiting for user input, got %q", got)
	}
}

func TestTaskWorkflowReportsDirectReplyWithoutExecution(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-2",
		ProjectID: "project-1",
		Body:      "scan all cameras in my room",
	}
	decision := agent.DecisionOutput{
		Classification: agent.Classification{Intent: agent.ClassificationIntentActionRequest, RoutedTo: agent.ClassificationRoutePlan},
		Plan: domain.Plan{
			ID:        "plan-2",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "I can't help with active camera discovery or surveillance-related device enumeration.",
		},
		ToolChoice: agent.ToolChoice{
			ResponseMessage: "I can't help with active camera discovery or surveillance-related device enumeration.",
		},
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}, nil)
	env.OnActivity("Activities.Plan", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}).Return(decision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, decision, (*agent.ExecutionFeedback)(nil), 1, mock.Anything).Return(decision, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(1)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, decision.ToolChoice.ResponseMessage).Return(nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, decision.ToolChoice.ResponseMessage).Return(nil)

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
		t.Fatalf("expected direct reply workflow to complete")
	}
}

func TestTaskWorkflowIgnoresNeutralMessages(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-3",
		ProjectID: "project-1",
		Body:      "hey",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentNeutral,
		RoutedTo: agent.ClassificationRouteIgnore,
		Summary:  "Greeting with no task.",
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(classification, nil)

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
		t.Fatalf("expected ignore workflow to complete")
	}
	if result.Decision.Classification.RoutedTo != agent.ClassificationRouteIgnore {
		t.Fatalf("expected ignored classification, got %q", result.Decision.Classification.RoutedTo)
	}
}

func TestTaskWorkflowIngestsContextUpdates(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-4",
		ProjectID: "project-1",
		Body:      "FYI we use PostgreSQL now",
	}
	classification := agent.Classification{
		Intent:            agent.ClassificationIntentContextUpdate,
		RoutedTo:          agent.ClassificationRouteIngest,
		ContradictionRisk: true,
		Summary:           "Database choice changed.",
	}
	decision := agent.DecisionOutput{
		Classification: classification,
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.LoadContext", mock.Anything, event).Return(agent.Context{Event: event}, nil)
	env.OnActivity("Activities.Classify", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}).Return(classification, nil)
	env.OnActivity("Activities.Ingest", mock.Anything, agent.DecisionInput{ProjectID: "project-1", Context: agent.Context{Event: event}}, classification).Return(decision, nil)

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
		t.Fatalf("expected ingest workflow to complete")
	}
	if result.Decision.Classification.RoutedTo != agent.ClassificationRouteIngest {
		t.Fatalf("expected ingest classification, got %q", result.Decision.Classification.RoutedTo)
	}
}
