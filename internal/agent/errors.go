package agent

import "errors"

var (
	ErrDecisionEngineUnavailable = errors.New("decision engine unavailable")
	ErrInvalidNextAction         = errors.New("invalid next action output")
	ErrInvalidToolChoice         = errors.New("invalid tool choice")
)
