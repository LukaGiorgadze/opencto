package agent

import "errors"

var (
	ErrDecisionEngineUnavailable = errors.New("decision engine unavailable")
	ErrInvalidClassification     = errors.New("invalid classification output")
	ErrInvalidClarification      = errors.New("invalid clarification output")
	ErrInvalidPlanningOutput     = errors.New("invalid planning output")
	ErrInvalidAgentLoopDecision  = errors.New("invalid agent loop decision")
	ErrInvalidToolChoice         = errors.New("invalid tool choice")
)
