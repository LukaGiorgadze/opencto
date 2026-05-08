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

type PlanningWaitSignal struct {
	WorkflowID string       `json:"workflow_id,omitempty"`
	EventID    string       `json:"event_id,omitempty"`
	Token      string       `json:"token,omitempty"`
	Kind       string       `json:"kind,omitempty"`
	Event      domain.Event `json:"event"`
}

type TaskOutputSignal struct {
	WorkflowID  string                 `json:"workflow_id,omitempty"`
	EventID     string                 `json:"event_id,omitempty"`
	WaitingKind string                 `json:"waiting_kind,omitempty"`
	Receipts    []domain.ReportReceipt `json:"receipts,omitempty"`
}

type PlanningAnswerSignal struct {
	Event       domain.Event `json:"event"`
	WaitingKind string       `json:"waiting_kind,omitempty"`
	Decision    string       `json:"decision,omitempty"`
}
