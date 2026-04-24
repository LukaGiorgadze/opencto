package agent

import (
	"context"
	"fmt"
	"strings"
)

type UnavailableEngine struct {
	reason string
}

func NewUnavailableEngine(reason string) *UnavailableEngine {
	return &UnavailableEngine{reason: strings.TrimSpace(reason)}
}

func (e *UnavailableEngine) Classify(_ context.Context, _ DecisionInput) (Classification, error) {
	return Classification{}, e.err("classify")
}

func (e *UnavailableEngine) Clarify(_ context.Context, _ ClarificationInput) (*ClarificationRequest, error) {
	return nil, e.err("clarify")
}

func (e *UnavailableEngine) Plan(_ context.Context, _ PlanningInput) (PlanningOutput, error) {
	return PlanningOutput{}, e.err("plan")
}

func (e *UnavailableEngine) SelectTool(_ context.Context, _ ToolSelectionInput) (ToolChoice, error) {
	return ToolChoice{}, e.err("select tool")
}

func (e *UnavailableEngine) DecideNextAction(_ context.Context, _ ToolSelectionInput) (AgentLoopDecision, error) {
	return AgentLoopDecision{}, e.err("decide next action")
}

func (e *UnavailableEngine) err(operation string) error {
	if e.reason == "" {
		return fmt.Errorf("%w: cannot %s", ErrDecisionEngineUnavailable, operation)
	}
	return fmt.Errorf("%w: cannot %s: %s", ErrDecisionEngineUnavailable, operation, e.reason)
}
