package signals

import (
	"time"

	"github.com/opencto/opencto/internal/domain"
)

type ApprovalDecisionSignal struct {
	ProjectID  string
	ApprovalID string
	Approved   bool
	ActorID    string
	ActorName  string
	Comment    string
	DecidedAt  time.Time
}

type EnqueueEventSignal struct {
	Event domain.Event
}

type ContradictionResolutionSignal struct {
	ProjectID       string
	ContradictionID string
	Resolution      string
	ResolvedBy      string
}
