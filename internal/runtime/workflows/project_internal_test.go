package workflows

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/opencto/opencto/internal/workflowbundle"
)

func TestRememberProjectEventIDKeepsRecentWindow(t *testing.T) {
	t.Parallel()

	state := ProjectWorkflowState{}
	for i := 0; i < recentProjectEventIDLimit; i++ {
		if seen := rememberProjectEventID(&state, fmt.Sprintf("event-%d", i)); seen {
			t.Fatalf("new event was reported as seen")
		}
	}
	if len(state.RecentEventIDs) != recentProjectEventIDLimit {
		t.Fatalf("expected %d recent event ids, got %d", recentProjectEventIDLimit, len(state.RecentEventIDs))
	}
	if seen := rememberProjectEventID(&state, "event-0"); !seen {
		t.Fatalf("expected oldest event to still be remembered before eviction")
	}
	if seen := rememberProjectEventID(&state, "event-1000"); seen {
		t.Fatalf("new event was reported as seen")
	}
	if len(state.RecentEventIDs) != recentProjectEventIDLimit {
		t.Fatalf("expected capped recent event ids, got %d", len(state.RecentEventIDs))
	}
	if state.RecentEventIDs[0] != "event-1" {
		t.Fatalf("expected oldest event to be evicted, got first id %q", state.RecentEventIDs[0])
	}
	if seen := rememberProjectEventID(&state, "event-0"); seen {
		t.Fatalf("expected evicted event to be accepted again")
	}
}

func TestRememberProjectEventIDIgnoresBlankIDs(t *testing.T) {
	t.Parallel()

	state := ProjectWorkflowState{}
	if seen := rememberProjectEventID(&state, " "); seen {
		t.Fatalf("blank event id should not be reported as seen")
	}
	if len(state.RecentEventIDs) != 0 {
		t.Fatalf("blank event id should not be remembered")
	}
}

func TestWorkflowFailureMessageUsesApplicationErrorMessage(t *testing.T) {
	t.Parallel()

	err := temporal.NewApplicationError("actual step stderr", "WorkflowStepFailed")
	if got := workflowFailureMessage(err); got != "actual step stderr" {
		t.Fatalf("expected application error message, got %q", got)
	}
}

func TestWorkflowFailureMessageFallsBackToErrorString(t *testing.T) {
	t.Parallel()

	err := errors.New("plain failure")
	if got := workflowFailureMessage(err); got != "plain failure" {
		t.Fatalf("expected fallback error string, got %q", got)
	}
}

func TestWorkflowStepRetryPolicyDefaultsMaximumAttempts(t *testing.T) {
	t.Parallel()

	retryPolicy := workflowStepRetryPolicy(workflowbundle.Step{}, 0, 0)
	if retryPolicy.MaximumAttempts != defaultWorkflowStepMaximumAttempts {
		t.Fatalf("expected default maximum attempts %d, got %d", defaultWorkflowStepMaximumAttempts, retryPolicy.MaximumAttempts)
	}
	if retryPolicy.InitialInterval != time.Second {
		t.Fatalf("expected default initial interval, got %s", retryPolicy.InitialInterval)
	}
	if retryPolicy.BackoffCoefficient != 2 {
		t.Fatalf("expected default backoff coefficient, got %v", retryPolicy.BackoffCoefficient)
	}
}

func TestWorkflowStepRetryPolicyPreservesConfiguredMaximumAttempts(t *testing.T) {
	t.Parallel()

	step := workflowbundle.Step{RetryPolicy: workflowbundle.RetryPolicy{MaximumAttempts: 7}}
	retryPolicy := workflowStepRetryPolicy(step, 2*time.Second, 30*time.Second)
	if retryPolicy.MaximumAttempts != 7 {
		t.Fatalf("expected configured maximum attempts, got %d", retryPolicy.MaximumAttempts)
	}
	if retryPolicy.InitialInterval != 2*time.Second || retryPolicy.MaximumInterval != 30*time.Second {
		t.Fatalf("unexpected retry intervals: %#v", retryPolicy)
	}
}

func TestWorkflowStepRetryPolicyPreservesNonRetryableErrorTypes(t *testing.T) {
	t.Parallel()

	step := workflowbundle.Step{RetryPolicy: workflowbundle.RetryPolicy{
		NonRetryableErrorTypes: []string{workflowbundle.StepFailureErrorType},
	}}
	retryPolicy := workflowStepRetryPolicy(step, 0, 0)
	if len(retryPolicy.NonRetryableErrorTypes) != 1 || retryPolicy.NonRetryableErrorTypes[0] != workflowbundle.StepFailureErrorType {
		t.Fatalf("expected supported non-retryable error type, got %#v", retryPolicy.NonRetryableErrorTypes)
	}
}
