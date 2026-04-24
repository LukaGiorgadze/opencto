package workflows

import (
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
)

func TestApprovalMessagePrefersPlannedDiscordReviewMessage(t *testing.T) {
	t.Parallel()

	message := approvalMessage(
		domain.ApprovalRequest{
			ID:       "approval-1",
			RiskTier: domain.RiskTierConsequential,
		},
		agent.DecisionOutput{
			Plan: domain.Plan{
				Summary: "Fallback summary",
				Metadata: map[string]string{
					"discord_message": "Plan review message.",
				},
			},
			ToolChoice: agent.ToolChoice{
				Intent: "deploy to staging",
			},
		},
		policy.Result{
			Reasons: []string{"network egress requested"},
		},
	)

	for _, want := range []string{
		"Plan review message.",
		"Approval ID: `approval-1`",
		"Reason: network egress requested",
		"approve approval-1",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("approval message missing %q\n%s", want, message)
		}
	}
}
