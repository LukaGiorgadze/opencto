package signals

import "github.com/opencto/opencto/internal/domain"

type EnqueueEventSignal struct {
	Event domain.Event
}

type TaskControlSignal struct {
	Event  domain.Event `json:"event"`
	Reason string       `json:"reason,omitempty"`
}

type AdditionalContextSignal struct {
	Event domain.Event `json:"event"`
}
