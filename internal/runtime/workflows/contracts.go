package workflows

import (
	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/scheduled"
	"github.com/opencto/opencto/internal/runtime/signals"
)

const (
	ProjectWorkflowName           = "ProjectWorkflow"
	TaskWorkflowName              = "TaskWorkflow"
	ScheduledDispatchWorkflowName = scheduled.DispatchWorkflowName

	SignalEnqueueEvent          = "enqueue-event"
	SignalTaskCancel            = "task-cancel"
	SignalTaskInterrupt         = "task-interrupt"
	SignalTaskAdditionalContext = "task-additional-context"
)

type ProjectWorkflowInput struct {
	ProjectID                string                `json:"project_id"`
	ContinueAsNewAfterEvents int                   `json:"continue_as_new_after_events,omitempty"`
	Snapshot                 *ProjectWorkflowState `json:"snapshot,omitempty"`
}

type (
	EnqueueEventSignal      = signals.EnqueueEventSignal
	TaskControlSignal       = signals.TaskControlSignal
	AdditionalContextSignal = signals.AdditionalContextSignal
)

type ProjectWorkflowState struct {
	ProjectID       string         `json:"project_id"`
	Queue           []domain.Event `json:"queue,omitempty"`
	RecentEventIDs  []string       `json:"recent_event_ids,omitempty"`
	ProcessedEvents int            `json:"processed_events"`
}

type TaskWorkflowInput struct {
	ProjectID        string            `json:"project_id"`
	Event            domain.Event      `json:"event"`
	AdditionalEvents []domain.Event    `json:"additional_events,omitempty"`
	ResumedFromPause bool              `json:"resumed_from_pause,omitempty"`
	NextAction       *agent.NextAction `json:"next_action,omitempty"`
}

type TaskWorkflowResult struct {
	Completed           bool                      `json:"completed"`
	Status              string                    `json:"status,omitempty"`
	Event               domain.Event              `json:"event,omitempty"`
	ResponseMessage     string                    `json:"response_message,omitempty"`
	ResponseAttachments []domain.ReportAttachment `json:"response_attachments,omitempty"`
	Report              bool                      `json:"report,omitempty"`
}
