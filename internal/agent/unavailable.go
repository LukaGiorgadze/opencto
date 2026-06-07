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

func (e *UnavailableEngine) NextAction(_ context.Context, _ NextActionInput) (NextActionOutput, error) {
	return NextActionOutput{}, e.err("choose next action")
}

func (e *UnavailableEngine) err(operation string) error {
	if e.reason == "" {
		return fmt.Errorf("%w: cannot %s", ErrNextActionEngineUnavailable, operation)
	}
	return fmt.Errorf("%w: cannot %s: %s", ErrNextActionEngineUnavailable, operation, e.reason)
}
