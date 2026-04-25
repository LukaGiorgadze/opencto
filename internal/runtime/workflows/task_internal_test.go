package workflows

import (
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
)

func TestApprovalMessageBuildsReviewFromPlan(t *testing.T) {
	t.Parallel()

	message := approvalMessage(
		domain.ApprovalRequest{
			ID:       "approval-1",
			RiskTier: domain.RiskTierConsequential,
		},
		agent.DecisionOutput{
			Plan: domain.Plan{
				Summary: "Deploy the current build to staging after verification.",
			},
			WorkItems: []domain.WorkItem{
				{
					Title:    "Verify deployment target",
					RiskTier: domain.RiskTierObserve,
				},
				{
					Title:    "Deploy to staging",
					RiskTier: domain.RiskTierConsequential,
					Status:   domain.WorkItemStatusAwaitingApproval,
				},
			},
			ToolChoice: agent.ToolChoice{
				Intent: "deploy to staging",
			},
		},
		policy.Result{
			Reasons: []string{"destructive or secret-sensitive action"},
		},
	)

	for _, want := range []string{
		"Approval required before I continue.",
		"Deploy the current build to staging after verification.",
		"1. [T0] Verify deployment target",
		"2. [T2] Deploy to staging (awaiting approval)",
		"Approval ID: `approval-1`",
		"Reason: destructive or secret-sensitive action",
		"approve approval-1",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("approval message missing %q\n%s", want, message)
		}
	}
}

func TestClarificationMessagePrefersUserFacingMessage(t *testing.T) {
	t.Parallel()

	message := clarificationMessage(&agent.ClarificationRequest{
		Message:   "I understand you want to scan your computer's local networks for cameras. Which computer should I scan?",
		Questions: []string{"Which computer should I scan?"},
		Reason:    "Blocked by: the specific computer is not identified",
	})

	if strings.Count(message, "Which computer should I scan?") != 1 {
		t.Fatalf("expected question to appear once, got %q", message)
	}
	if strings.Contains(message, "Blocked by:") {
		t.Fatalf("expected reason to stay internal when message exists, got %q", message)
	}
}

func TestClarificationMessageFallsBackToQuestionsThenReason(t *testing.T) {
	t.Parallel()

	message := clarificationMessage(&agent.ClarificationRequest{
		Questions: []string{"", "Which environment should I deploy to?"},
		Reason:    "Blocked by: the deployment target is not specified",
	})

	if message != "Which environment should I deploy to?" {
		t.Fatalf("expected question fallback, got %q", message)
	}

	message = clarificationMessage(&agent.ClarificationRequest{
		Reason: "Blocked by: the deployment target is not specified",
	})
	if message != "Blocked by: the deployment target is not specified" {
		t.Fatalf("expected reason fallback, got %q", message)
	}
}

func TestSelectionSnapshotStripsPromptNoise(t *testing.T) {
	t.Parallel()

	decision := agent.DecisionOutput{
		Classification: agent.Classification{
			Intent:   agent.ClassificationIntentActionRequest,
			RoutedTo: agent.ClassificationRoutePlan,
		},
		Plan: domain.Plan{
			ID:          "plan-1",
			ProjectID:   "project-1",
			Steps:       []domain.PlanStep{{ID: "wi-1", Title: "Inspect repo"}},
			WorkItemIDs: []string{"wi-1"},
			Metadata: map[string]string{
				"assumptions_json":     `["use defaults"]`,
				"execution_order_json": `[["Inspect repo"]]`,
				"test_strategy":        "Read the file.",
			},
		},
		WorkItems: []domain.WorkItem{{
			ID:     "wi-1",
			Title:  "Inspect repo",
			Status: domain.WorkItemStatusReady,
			Metadata: map[string]string{
				"acceptance_criteria_json": `["repo listed"]`,
				"depends_on_json":          `["Prepare workspace"]`,
				"rollback":                 "N/A",
				"skills_json":              `["go"]`,
			},
		}},
		ToolChoice:      agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "old tool"},
		ToolChoices:     []agent.ToolChoice{{Type: domain.ToolTypeShell, Intent: "queued tool"}},
		ResponseMessage: "old response",
	}

	snapshot := selectionSnapshot(decision)

	if !snapshot.ToolChoice.IsZero() || len(snapshot.ToolChoices) != 0 || snapshot.ResponseMessage != "" {
		t.Fatalf("expected tool and response to be stripped: %#v", snapshot)
	}
	if len(snapshot.Plan.Steps) != 0 || len(snapshot.Plan.WorkItemIDs) != 0 {
		t.Fatalf("expected duplicate plan steps and ids to be stripped: %#v", snapshot.Plan)
	}
	if _, ok := snapshot.Plan.Metadata["discord_message"]; ok {
		t.Fatalf("expected discord message to be omitted from selector metadata")
	}
	if _, ok := snapshot.Plan.Metadata["execution_order_json"]; !ok {
		t.Fatalf("expected execution order to be preserved")
	}
	if _, ok := snapshot.WorkItems[0].Metadata["acceptance_criteria_json"]; ok {
		t.Fatalf("expected acceptance criteria to be omitted from selector work item metadata")
	}
	if _, ok := snapshot.WorkItems[0].Metadata["depends_on_json"]; !ok {
		t.Fatalf("expected dependencies to be preserved")
	}
}

func TestPersistenceSnapshotKeepsOnlyPersistedPlanState(t *testing.T) {
	t.Parallel()

	decision := agent.DecisionOutput{
		Classification: agent.Classification{
			Intent:   agent.ClassificationIntentActionRequest,
			RoutedTo: agent.ClassificationRoutePlan,
		},
		Plan: domain.Plan{
			ID:        "plan-1",
			ProjectID: "project-1",
			Metadata: map[string]string{
				"discord_message":      "legacy channel copy",
				"execution_order_json": `[["Inspect repo"]]`,
			},
		},
		WorkItems:       []domain.WorkItem{{ID: "wi-1", ProjectID: "project-1"}},
		ToolChoice:      agent.ToolChoice{Type: domain.ToolTypeShell, Intent: "run command"},
		ToolChoices:     []agent.ToolChoice{{Type: domain.ToolTypeShell, Intent: "queued command"}},
		ResponseMessage: "done",
		DependencyAudit: &agent.DependencyAudit{Dependency: "example"},
	}

	snapshot := persistenceSnapshot(decision)

	if !snapshot.Classification.IsZero() || !snapshot.ToolChoice.IsZero() || len(snapshot.ToolChoices) != 0 || snapshot.ResponseMessage != "" || snapshot.DependencyAudit != nil {
		t.Fatalf("expected non-persisted decision fields to be stripped: %#v", snapshot)
	}
	if snapshot.Plan.ID != "plan-1" || len(snapshot.WorkItems) != 1 || snapshot.WorkItems[0].ID != "wi-1" {
		t.Fatalf("expected plan and work item state to be preserved: %#v", snapshot)
	}
	if _, ok := snapshot.Plan.Metadata["discord_message"]; ok {
		t.Fatalf("expected legacy discord message metadata to be stripped")
	}
	if _, ok := snapshot.Plan.Metadata["execution_order_json"]; !ok {
		t.Fatalf("expected structural plan metadata to be preserved")
	}
}

func TestDequeueNextToolChoiceKeepsBatchOnSameWorkItem(t *testing.T) {
	t.Parallel()

	decision := agent.DecisionOutput{
		ToolChoice: agent.ToolChoice{
			Type:    domain.ToolTypeShell,
			Intent:  "inspect package",
			Command: "pwd",
		},
		ToolChoices: []agent.ToolChoice{{
			Type:    domain.ToolTypeShell,
			Intent:  "list package files",
			Command: "ls",
			Args:    []string{"-la"},
		}},
	}

	dequeued := dequeueNextToolChoice(&decision, agent.ExecutionFeedback{
		WorkItemID: "wi-1",
		Status:     string(domain.ExecutionStatusSucceeded),
	})

	if !dequeued {
		t.Fatalf("expected queued tool to be dequeued")
	}
	if decision.ToolChoice.Command != "ls" {
		t.Fatalf("expected queued tool to become active, got %#v", decision.ToolChoice)
	}
	if decision.ToolChoice.Metadata["work_item_id"] != "wi-1" {
		t.Fatalf("expected queued tool to inherit work item id, got %#v", decision.ToolChoice.Metadata)
	}
	if len(decision.ToolChoices) != 0 {
		t.Fatalf("expected queue to be drained, got %#v", decision.ToolChoices)
	}
}

func TestDequeueNextToolChoiceDropsQueueOnFailure(t *testing.T) {
	t.Parallel()

	decision := agent.DecisionOutput{
		ToolChoice: agent.ToolChoice{
			Type:    domain.ToolTypeShell,
			Intent:  "inspect package",
			Command: "pwd",
		},
		ToolChoices: []agent.ToolChoice{{
			Type:    domain.ToolTypeShell,
			Intent:  "list package files",
			Command: "ls",
		}},
	}

	dequeued := dequeueNextToolChoice(&decision, agent.ExecutionFeedback{
		WorkItemID: "wi-1",
		Status:     string(domain.ExecutionStatusFailed),
		Error:      "exit status 1",
	})

	if dequeued {
		t.Fatalf("expected failed feedback not to dequeue")
	}
	if len(decision.ToolChoices) != 0 {
		t.Fatalf("expected failed feedback to discard queued tools, got %#v", decision.ToolChoices)
	}
	if decision.ToolChoice.Command != "pwd" {
		t.Fatalf("expected active tool to remain unchanged, got %#v", decision.ToolChoice)
	}
}

func TestReportActivityOptionsAreBoundedAndNonRetrying(t *testing.T) {
	t.Parallel()

	options := reportActivityOptions()

	if options.StartToCloseTimeout != 15*time.Second {
		t.Fatalf("unexpected report start-to-close timeout: %s", options.StartToCloseTimeout)
	}
	if options.ScheduleToCloseTimeout != 30*time.Second {
		t.Fatalf("unexpected report schedule-to-close timeout: %s", options.ScheduleToCloseTimeout)
	}
	if options.RetryPolicy == nil {
		t.Fatalf("expected report retry policy")
	}
	if options.RetryPolicy.MaximumAttempts != 1 {
		t.Fatalf("expected report activity to be non-retrying, got %d attempts", options.RetryPolicy.MaximumAttempts)
	}
}

func TestToolSelectionActivityOptionsAreBounded(t *testing.T) {
	t.Parallel()

	options := toolSelectionActivityOptions()

	if options.StartToCloseTimeout != 3*time.Minute {
		t.Fatalf("unexpected tool selection start-to-close timeout: %s", options.StartToCloseTimeout)
	}
	if options.ScheduleToCloseTimeout != 7*time.Minute {
		t.Fatalf("unexpected tool selection schedule-to-close timeout: %s", options.ScheduleToCloseTimeout)
	}
	if options.RetryPolicy == nil {
		t.Fatalf("expected tool selection retry policy")
	}
	if options.RetryPolicy.MaximumAttempts != 2 {
		t.Fatalf("unexpected tool selection attempts: %d", options.RetryPolicy.MaximumAttempts)
	}
	if options.RetryPolicy.MaximumInterval != 30*time.Second {
		t.Fatalf("unexpected tool selection maximum retry interval: %s", options.RetryPolicy.MaximumInterval)
	}
}
