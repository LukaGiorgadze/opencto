package policy

import (
	"context"
	"testing"

	"github.com/opencto/opencto/internal/domain"
)

func TestEvaluateDeniedCommand(t *testing.T) {
	t.Parallel()

	engine := NewStaticEngine(1, true, []string{"sudo"})
	result, err := engine.Evaluate(context.Background(), Request{
		Command:       "sudo",
		WorkingDir:    "/tmp/project",
		WorkspaceRoot: "/tmp/project",
	})
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	if result.Allowed {
		t.Fatalf("expected command to be denied")
	}
}

func TestEvaluateProductionActionRequiresOwnerApproval(t *testing.T) {
	t.Parallel()

	engine := NewStaticEngine(1, true, nil)
	result, err := engine.Evaluate(context.Background(), Request{
		WorkingDir:    "/tmp/project",
		WorkspaceRoot: "/tmp/project",
		Production:    true,
		ToolType:      domain.ToolTypeShell,
	})
	if err != nil {
		t.Fatalf("evaluate policy: %v", err)
	}
	if result.Tier != domain.RiskTierOwnerApproval {
		t.Fatalf("unexpected tier: %d", result.Tier)
	}
	if !result.RequiresApproval {
		t.Fatalf("expected approval requirement")
	}
}
