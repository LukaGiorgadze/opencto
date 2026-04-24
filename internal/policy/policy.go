package policy

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

type Request struct {
	ProjectID      string          `json:"project_id"`
	Intent         string          `json:"intent,omitempty"`
	ToolType       domain.ToolType `json:"tool_type,omitempty"`
	Command        string          `json:"command,omitempty"`
	Args           []string        `json:"args,omitempty"`
	WorkingDir     string          `json:"working_dir,omitempty"`
	WorkspaceRoot  string          `json:"workspace_root,omitempty"`
	NetworkEgress  bool            `json:"network_egress,omitempty"`
	SecretExposure bool            `json:"secret_exposure,omitempty"`
	Destructive    bool            `json:"destructive,omitempty"`
	Production     bool            `json:"production,omitempty"`
	Financial      bool            `json:"financial,omitempty"`
}

type Result struct {
	Tier             domain.RiskTier `json:"tier"`
	Allowed          bool            `json:"allowed"`
	RequiresApproval bool            `json:"requires_approval,omitempty"`
	Reasons          []string        `json:"reasons,omitempty"`
	Violations       []string        `json:"violations,omitempty"`
}

type Engine interface {
	Evaluate(context.Context, Request) (Result, error)
}

type StaticEngine struct {
	autonomyThreshold    int
	requireOwnerForTier3 bool
	deniedCommands       map[string]struct{}
}

func NewStaticEngine(autonomyThreshold int, requireOwnerForTier3 bool, deniedCommands []string) *StaticEngine {
	deny := make(map[string]struct{}, len(deniedCommands))
	for _, cmd := range deniedCommands {
		deny[strings.TrimSpace(strings.ToLower(cmd))] = struct{}{}
	}
	return &StaticEngine{
		autonomyThreshold:    autonomyThreshold,
		requireOwnerForTier3: requireOwnerForTier3,
		deniedCommands:       deny,
	}
}

func (e *StaticEngine) Evaluate(_ context.Context, req Request) (Result, error) {
	result := Result{
		Allowed: true,
		Tier:    domain.RiskTierSafeLocalChange,
	}

	if strings.TrimSpace(req.Command) != "" {
		if _, denied := e.deniedCommands[strings.ToLower(strings.TrimSpace(req.Command))]; denied {
			result.Allowed = false
			result.Violations = append(result.Violations, "command is explicitly denied")
		}
	}

	if req.WorkspaceRoot != "" && req.WorkingDir != "" {
		absRoot, _ := filepath.Abs(req.WorkspaceRoot)
		absDir, _ := filepath.Abs(req.WorkingDir)
		if absDir != absRoot && !strings.HasPrefix(absDir, absRoot+string(filepath.Separator)) {
			result.Allowed = false
			result.Violations = append(result.Violations, "working directory is outside project workspace")
		}
	}

	switch {
	case req.Financial || req.Production:
		result.Tier = domain.RiskTierOwnerApproval
		result.RequiresApproval = true
		result.Reasons = append(result.Reasons, "financial or production-facing action")
	case req.Destructive || req.SecretExposure:
		result.Tier = domain.RiskTierConsequential
		result.RequiresApproval = true
		result.Reasons = append(result.Reasons, "destructive or secret-sensitive action")
	case req.NetworkEgress:
		result.Tier = domain.RiskTierConsequential
		result.RequiresApproval = true
		result.Reasons = append(result.Reasons, "network egress requested")
	default:
		result.Tier = domain.RiskTierSafeLocalChange
	}

	if !result.RequiresApproval && int(result.Tier) > e.autonomyThreshold {
		result.RequiresApproval = true
		result.Reasons = append(result.Reasons, "risk exceeds autonomy threshold")
	}

	if e.requireOwnerForTier3 && result.Tier == domain.RiskTierOwnerApproval {
		result.RequiresApproval = true
	}

	return result, nil
}
