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

func TestProjectWorkflowCarriesApprovedRequestIntoResume(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-resume",
		ProjectID: "project-1",
		Body:      "install dependency",
	}
	decision := agent.DecisionOutput{
		Plan: domain.Plan{
			ID:        "plan-resume",
			ProjectID: "project-1",
			EventID:   event.ID,
		},
	}
	pendingApproval := domain.ApprovalRequest{
		ID:         "approval-resume",
		ProjectID:  "project-1",
		WorkItemID: "work-item-resume",
		Status:     domain.ApprovalStatusPending,
	}
	approvedApproval := pendingApproval
	approvedApproval.Status = domain.ApprovalStatusApproved
	var resumedWithApproval bool

	env.RegisterWorkflowWithOptions(func(_ sdkworkflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		if input.ResumedFromPause {
			resumedWithApproval = input.ApprovalRequest != nil &&
				input.ApprovalRequest.ID == approvedApproval.ID &&
				input.ApprovalRequest.Status == domain.ApprovalStatusApproved
			return workflows.TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}
		return workflows.TaskWorkflowResult{
			AwaitingApproval: true,
			ApprovalRequest:  &pendingApproval,
			Decision:         decision,
		}, nil
	}, sdkworkflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.RegisterWorkflowWithOptions(func(_ sdkworkflow.Context, _ workflows.ApprovalDecisionSignal) (domain.ApprovalRequest, error) {
		return approvedApproval, nil
	}, sdkworkflow.RegisterOptions{Name: workflows.ApprovalWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, signals.EnqueueEventSignal{Event: event})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalApprovalDecision, signals.ApprovalDecisionSignal{
			ProjectID:  "project-1",
			ApprovalID: "approval-resume",
			Approved:   true,
			ActorName:  "owner",
			DecidedAt:  time.Now().UTC(),
		})
	}, time.Second)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 2*time.Second)

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
	if !resumedWithApproval {
		t.Fatalf("expected approved request to be carried into resumed task")
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
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "work-item-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.Metadata["execution_cycle"] == "1" &&
			request.ToolChoice.Metadata["work_item_id"] == "work-item-1" &&
			request.WorkItemID == "work-item-1" &&
			request.RiskTier == domain.RiskTierConsequential
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "work-item-1",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "run test command",
		Observation:     "done",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "work-item-1" &&
			feedback != nil &&
			feedback.WorkItemID == "work-item-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "done"
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionComplete,
		WorkItemID:      "work-item-1",
		WorkItemStatus:  domain.WorkItemStatusCompleted,
		ResponseMessage: "done",
	}, nil)
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
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}).Return(decision, nil)
	approvalToolChoice := decision.ToolChoice
	approvalSelectionDecision := decision
	approvalSelectionDecision.ToolChoice = agent.ToolChoice{}
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          approvalSelectionDecision,
		CurrentWorkItemID: "work-item-1",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &approvalToolChoice}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "work-item-1"
	})).Return(policy.Result{
		Allowed:          true,
		RequiresApproval: true,
		Tier:             domain.RiskTierConsequential,
		Reasons:          []string{"destructive or secret-sensitive action"},
	}, nil)
	env.OnActivity("Activities.CreateApprovalRequest", mock.Anything, mock.Anything, policy.Result{
		Allowed:          true,
		RequiresApproval: true,
		Tier:             domain.RiskTierConsequential,
		Reasons:          []string{"destructive or secret-sensitive action"},
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

func TestTaskWorkflowConsumesApprovedApprovalOnResume(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-approved",
		ProjectID: "project-1",
		Body:      "install dependencies",
	}
	decision := agent.DecisionOutput{
		Classification: agent.Classification{Intent: agent.ClassificationIntentActionRequest, RoutedTo: agent.ClassificationRoutePlan},
		Plan: domain.Plan{
			ID:        "plan-approved",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Install dependencies after approval.",
		},
		WorkItems: []domain.WorkItem{
			{
				ID:        "work-item-approved",
				ProjectID: "project-1",
				Title:     "Install dependencies",
				Status:    domain.WorkItemStatusAwaitingApproval,
				RiskTier:  domain.RiskTierConsequential,
			},
		},
		ToolChoice: agent.ToolChoice{
			Type:        domain.ToolTypeShell,
			Intent:      "Install dependencies",
			Command:     "/bin/zsh",
			Args:        []string{"-lc", "npm install"},
			WorkingDir:  ".",
			Destructive: true,
		},
	}
	approval := domain.ApprovalRequest{
		ID:         "approval-approved",
		ProjectID:  "project-1",
		WorkItemID: "work-item-approved",
		Status:     domain.ApprovalStatusApproved,
		RiskTier:   domain.RiskTierConsequential,
	}

	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" &&
			choice.Metadata["work_item_id"] == "work-item-approved"
	})).Return(policy.Result{
		Allowed:          true,
		RequiresApproval: true,
		Tier:             domain.RiskTierConsequential,
		Reasons:          []string{"destructive or secret-sensitive action"},
	}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "work-item-approved" &&
			request.RiskTier == domain.RiskTierConsequential &&
			request.ToolChoice.Metadata["execution_cycle"] == "1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "work-item-approved",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "Install dependencies",
		Observation:     "dependencies installed",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "work-item-approved" &&
			feedback != nil &&
			feedback.WorkItemID == "work-item-approved" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "dependencies installed"
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionComplete,
		WorkItemID:      "work-item-approved",
		WorkItemStatus:  domain.WorkItemStatusCompleted,
		ResponseMessage: "dependencies installed",
	}, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, "dependencies installed").Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", "dependencies installed", []string{"Install dependencies after approval."}).Return(domain.ADR{}, nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, "dependencies installed").Return(nil)

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID:        "project-1",
		Event:            event,
		ResumedFromPause: true,
		ApprovalID:       "approval-approved",
		ApprovalRequest:  &approval,
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
		t.Fatalf("expected resumed workflow to complete")
	}
	if got := result.Decision.WorkItems[0].Status; got != domain.WorkItemStatusCompleted {
		t.Fatalf("expected work item status %q, got %q", domain.WorkItemStatusCompleted, got)
	}
}

func TestTaskWorkflowReportsPolicyDenialWithoutFailing(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-denied",
		ProjectID: "project-1",
		Body:      "create hello world reactjs project in there",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "Create a React project.",
	}
	decision := agent.DecisionOutput{
		Classification: classification,
		Plan: domain.Plan{
			ID:        "plan-denied",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Create a React project outside the project workspace.",
		},
		WorkItems: []domain.WorkItem{
			{
				ID:        "work-item-denied",
				ProjectID: "project-1",
				Title:     "Install React app dependencies",
				Status:    domain.WorkItemStatusReady,
				RiskTier:  domain.RiskTierConsequential,
			},
		},
	}
	toolChoice := agent.ToolChoice{
		Type:         domain.ToolTypeShell,
		Intent:       "Install React app dependencies",
		Command:      "/bin/zsh",
		Args:         []string{"-lc", "npm install"},
		WorkingDir:   "/Users/luka/projects/helloworld",
		InputSummary: "Install React app dependencies",
		Destructive:  true,
	}
	denial := policy.Result{
		Tier:             domain.RiskTierConsequential,
		Allowed:          false,
		RequiresApproval: true,
		Reasons:          []string{"destructive or secret-sensitive action"},
		Violations:       []string{"working directory is outside project workspace"},
	}

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, classification).Return(decision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          decision,
		CurrentWorkItemID: "work-item-denied",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &toolChoice}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" &&
			choice.Metadata["work_item_id"] == "work-item-denied"
	})).Return(denial, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, mock.MatchedBy(func(message string) bool {
		return strings.Contains(message, "policy blocked") &&
			strings.Contains(message, "working directory is outside project workspace") &&
			strings.Contains(message, "destructive or secret-sensitive action")
	})).Return(nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, mock.MatchedBy(func(message string) bool {
		return strings.Contains(message, "policy blocked") &&
			strings.Contains(message, "working directory is outside project workspace") &&
			strings.Contains(message, "destructive or secret-sensitive action")
	})).Return(nil)

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
		t.Fatalf("expected denied workflow to complete")
	}
	if result.AwaitingApproval {
		t.Fatalf("did not expect approval request for a denied policy result")
	}
	if got := result.Decision.Plan.Status; got != domain.PlanStatusBlocked {
		t.Fatalf("expected plan status %q, got %q", domain.PlanStatusBlocked, got)
	}
	if got := result.Decision.WorkItems[0].Status; got != domain.WorkItemStatusBlocked {
		t.Fatalf("expected work item status %q, got %q", domain.WorkItemStatusBlocked, got)
	}
	if !strings.Contains(result.Decision.ResponseMessage, "working directory is outside project workspace") {
		t.Fatalf("expected response to include policy violation, got %q", result.Decision.ResponseMessage)
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
	finalMessage := "landing page files created"

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, classification).Return(plannedDecision, nil)
	firstToolChoice := firstToolDecision.ToolChoice
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          plannedDecision,
		CurrentWorkItemID: "wi-1",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &firstToolChoice}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(4)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "wi-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.Metadata["execution_cycle"] == "1" &&
			request.ToolChoice.Metadata["work_item_id"] == "wi-1" &&
			request.WorkItemID == "wi-1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "inspect the repository structure",
		Observation:     "located the target folder and the page entry point",
	}, nil)
	secondToolChoice := secondToolDecision.ToolChoice
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ProjectID == "project-1" &&
			request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "wi-1" &&
			feedback != nil &&
			feedback.Cycle == 1 &&
			feedback.WorkItemID == "wi-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "located the target folder and the page entry point"
	})).Return(activities.ToolSelectionResult{
		Action:         agent.AgentLoopActionContinue,
		WorkItemID:     "wi-1",
		WorkItemStatus: domain.WorkItemStatusCompleted,
		ToolChoice:     &secondToolChoice,
	}, nil)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "2" && choice.Metadata["work_item_id"] == "wi-2"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.Metadata["execution_cycle"] == "2" &&
			request.ToolChoice.Metadata["work_item_id"] == "wi-2" &&
			request.WorkItemID == "wi-2"
	})).Return(activities.ExecuteToolResult{
		Cycle:           2,
		WorkItemID:      "wi-2",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "create the landing page files",
		Observation:     "landing page files created",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ProjectID == "project-1" &&
			request.ExecutionCycle == 3 &&
			request.CurrentWorkItemID == "wi-2" &&
			feedback != nil &&
			feedback.Cycle == 2 &&
			feedback.WorkItemID == "wi-2" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "landing page files created"
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionComplete,
		WorkItemID:      "wi-2",
		WorkItemStatus:  domain.WorkItemStatusCompleted,
		ResponseMessage: finalMessage,
	}, nil)
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

func TestTaskWorkflowRepairsAfterFailedToolObservation(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-repair",
		ProjectID: "project-1",
		Body:      "run the tests and fix the obvious issue",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "Run tests and repair failures.",
	}
	plannedDecision := agent.DecisionOutput{
		Classification: classification,
		Plan: domain.Plan{
			ID:        "plan-repair",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Run tests, repair the failure, then verify.",
		},
		WorkItems: []domain.WorkItem{{
			ID:        "wi-repair",
			ProjectID: "project-1",
			Title:     "Repair failing tests",
			Status:    domain.WorkItemStatusReady,
		}},
	}
	firstTool := agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "run tests"}
	repairTool := agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "inspect the failing package"}
	finalMessage := "tests pass after repairing the failure"

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, classification).Return(plannedDecision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          plannedDecision,
		CurrentWorkItemID: "wi-repair",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &firstTool}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(4)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" &&
			choice.Metadata["work_item_id"] == "wi-repair"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-repair" &&
			request.ToolChoice.Metadata["execution_cycle"] == "1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-repair",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusFailed,
		RequestedAction: "run tests",
		Observation:     "go test ./... failed in internal/runtime",
		Error:           "exit status 1",
		ResultCode:      "1",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "wi-repair" &&
			feedback != nil &&
			feedback.WorkItemID == "wi-repair" &&
			feedback.Status == string(domain.ExecutionStatusFailed) &&
			feedback.Error == "exit status 1"
	})).Return(activities.ToolSelectionResult{
		Action:         agent.AgentLoopActionContinue,
		WorkItemID:     "wi-repair",
		WorkItemStatus: domain.WorkItemStatusReady,
		ToolChoice:     &repairTool,
	}, nil)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "2" &&
			choice.Metadata["work_item_id"] == "wi-repair" &&
			choice.Intent == "inspect the failing package"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-repair" &&
			request.ToolChoice.Metadata["execution_cycle"] == "2"
	})).Return(activities.ExecuteToolResult{
		Cycle:           2,
		WorkItemID:      "wi-repair",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "inspect the failing package",
		Observation:     finalMessage,
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 3 &&
			request.CurrentWorkItemID == "wi-repair" &&
			feedback != nil &&
			feedback.WorkItemID == "wi-repair" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == finalMessage
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionComplete,
		WorkItemID:      "wi-repair",
		WorkItemStatus:  domain.WorkItemStatusCompleted,
		ResponseMessage: finalMessage,
	}, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, finalMessage).Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", finalMessage, []string{"Run tests, repair the failure, then verify."}).Return(domain.ADR{}, nil)
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
	if got := result.Decision.WorkItems[0].Status; got != domain.WorkItemStatusCompleted {
		t.Fatalf("expected work item to complete after repair, got %q", got)
	}
}

func TestTaskWorkflowContinuesSameWorkItemWhenSuccessIsInsufficient(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterActivityWithOptions(&activities.Activities{}, activity.RegisterOptions{Name: "Activities."})

	event := domain.Event{
		ID:        "event-insufficient",
		ProjectID: "project-1",
		Body:      "add the missing config and verify it",
	}
	classification := agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "Add and verify config.",
	}
	plannedDecision := agent.DecisionOutput{
		Classification: classification,
		Plan: domain.Plan{
			ID:        "plan-insufficient",
			ProjectID: "project-1",
			EventID:   event.ID,
			Summary:   "Inspect config, add what is missing, and verify.",
		},
		WorkItems: []domain.WorkItem{{
			ID:        "wi-config",
			ProjectID: "project-1",
			Title:     "Add missing config",
			Status:    domain.WorkItemStatusReady,
		}},
	}
	inspectTool := agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "inspect config"}
	addTool := agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "add missing config"}
	finalMessage := "config added and verified"

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, classification).Return(plannedDecision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          plannedDecision,
		CurrentWorkItemID: "wi-config",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &inspectTool}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(4)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" &&
			choice.Metadata["work_item_id"] == "wi-config"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-config" &&
			request.ToolChoice.Metadata["execution_cycle"] == "1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-config",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "inspect config",
		Observation:     "config file is missing",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "wi-config" &&
			feedback != nil &&
			feedback.WorkItemID == "wi-config" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "config file is missing"
	})).Return(activities.ToolSelectionResult{
		Action:         agent.AgentLoopActionContinue,
		WorkItemID:     "wi-config",
		WorkItemStatus: domain.WorkItemStatusReady,
		ToolChoice:     &addTool,
	}, nil)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "2" &&
			choice.Metadata["work_item_id"] == "wi-config" &&
			choice.Intent == "add missing config"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-config" &&
			request.ToolChoice.Metadata["execution_cycle"] == "2"
	})).Return(activities.ExecuteToolResult{
		Cycle:           2,
		WorkItemID:      "wi-config",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "add missing config",
		Observation:     finalMessage,
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ExecutionCycle == 3 &&
			request.CurrentWorkItemID == "wi-config" &&
			feedback != nil &&
			feedback.WorkItemID == "wi-config" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == finalMessage
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionComplete,
		WorkItemID:      "wi-config",
		WorkItemStatus:  domain.WorkItemStatusCompleted,
		ResponseMessage: finalMessage,
	}, nil)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, finalMessage).Return(nil)
	env.OnActivity("Activities.WriteADR", mock.Anything, "project-1", "Execution Summary", finalMessage, []string{"Inspect config, add what is missing, and verify."}).Return(domain.ADR{}, nil)
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
	if got := result.Decision.WorkItems[0].Status; got != domain.WorkItemStatusCompleted {
		t.Fatalf("expected work item to complete after second action, got %q", got)
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

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, classification).Return(plannedDecision, nil)
	clarifyToolChoice := firstToolDecision.ToolChoice
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:         "project-1",
		Event:             event,
		Decision:          plannedDecision,
		CurrentWorkItemID: "wi-1",
		ExecutionCycle:    1,
	}).Return(activities.ToolSelectionResult{ToolChoice: &clarifyToolChoice}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(2)
	env.OnActivity("Activities.EvaluatePolicy", mock.Anything, event, mock.MatchedBy(func(choice agent.ToolChoice) bool {
		return choice.Metadata["execution_cycle"] == "1" && choice.Metadata["work_item_id"] == "wi-1"
	})).Return(policy.Result{Allowed: true}, nil)
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.Metadata["execution_cycle"] == "1" &&
			request.ToolChoice.Metadata["work_item_id"] == "wi-1" &&
			request.WorkItemID == "wi-1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		Tool:            domain.ToolTypeShell,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "inspect the repository structure",
		Observation:     "folder something exists but no frontend entry point was found",
	}, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, mock.MatchedBy(func(request activities.ToolSelectionRequest) bool {
		feedback := request.Feedback
		return request.ProjectID == "project-1" &&
			request.ExecutionCycle == 2 &&
			request.CurrentWorkItemID == "wi-1" &&
			feedback != nil &&
			feedback.Cycle == 1 &&
			feedback.WorkItemID == "wi-1" &&
			feedback.Status == string(domain.ExecutionStatusSucceeded) &&
			feedback.Observation == "folder something exists but no frontend entry point was found"
	})).Return(activities.ToolSelectionResult{
		Action:          agent.AgentLoopActionClarify,
		WorkItemID:      "wi-1",
		WorkItemStatus:  domain.WorkItemStatusBlocked,
		ResponseMessage: question,
	}, nil)
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
	}
	responseMessage := "I can't help with active camera discovery or surveillance-related device enumeration."

	env.OnActivity("Activities.PersistEvent", mock.Anything, event).Return(nil)
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}, nil)
	env.OnActivity("Activities.Plan", mock.Anything, event, agent.Classification{
		Intent:   agent.ClassificationIntentActionRequest,
		RoutedTo: agent.ClassificationRoutePlan,
		Summary:  "request is actionable",
	}).Return(decision, nil)
	env.OnActivity("Activities.SelectTool", mock.Anything, activities.ToolSelectionRequest{
		ProjectID:      "project-1",
		Event:          event,
		Decision:       decision,
		ExecutionCycle: 1,
	}).Return(activities.ToolSelectionResult{ResponseMessage: responseMessage}, nil)
	env.OnActivity("Activities.PersistDecision", mock.Anything, mock.Anything).Return(nil).Times(1)
	env.OnActivity("Activities.PersistConversationMemory", mock.Anything, event, responseMessage).Return(nil)
	env.OnActivity("Activities.ReportResult", mock.Anything, event, responseMessage).Return(nil)

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
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)

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
	env.OnActivity("Activities.Classify", mock.Anything, event).Return(classification, nil)
	env.OnActivity("Activities.Ingest", mock.Anything, event, classification).Return(decision, nil)

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
