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
		ResponseMessage: "old response",
	}

	snapshot := selectionSnapshot(decision)

	if !snapshot.ToolChoice.IsZero() || snapshot.ResponseMessage != "" {
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
		ResponseMessage: "done",
		DependencyAudit: &agent.DependencyAudit{Dependency: "example"},
	}

	snapshot := persistenceSnapshot(decision)

	if !snapshot.Classification.IsZero() || !snapshot.ToolChoice.IsZero() || snapshot.ResponseMessage != "" || snapshot.DependencyAudit != nil {
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
