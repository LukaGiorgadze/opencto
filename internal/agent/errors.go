package agent

import "errors"

var (
	ErrNextActionEngineUnavailable = errors.New("next action engine unavailable")
	ErrInvalidNextAction           = errors.New("invalid next action output")
	ErrInvalidToolChoice           = errors.New("invalid tool choice")
)
