package workflows

import (
	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/signals"
)

const (
	ProjectWorkflowName = "ProjectWorkflow"
	TaskWorkflowName    = "TaskWorkflow"

	SignalEnqueueEvent          = "enqueue-event"
	SignalTaskCancel            = "task-cancel"
	SignalTaskInterrupt         = "task-interrupt"
	SignalTaskAdditionalContext = "task-additional-context"

	QueryProjectState = "project-state"
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
	ProjectID       string            `json:"project_id"`
	Queue           []domain.Event    `json:"queue,omitempty"`
	ActiveTasks     map[string]string `json:"active_tasks,omitempty"`
	SeenEventIDs    map[string]bool   `json:"seen_event_ids,omitempty"`
	ProcessedEvents int               `json:"processed_events"`
}

type TaskWorkflowInput struct {
	ProjectID        string            `json:"project_id"`
	Event            domain.Event      `json:"event"`
	AdditionalEvents []domain.Event    `json:"additional_events,omitempty"`
	ResumedFromPause bool              `json:"resumed_from_pause,omitempty"`
	NextAction       *agent.NextAction `json:"next_action,omitempty"`
}

type TaskWorkflowResult struct {
	Completed bool `json:"completed"`
}
