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
	ProjectID         string                    `json:"project_id"`
	Queue             []domain.Event            `json:"queue,omitempty"`
	ActiveTaskID      string                    `json:"active_task_id,omitempty"`
	ActiveTasks       map[string]string         `json:"active_tasks,omitempty"`
	Processes         map[string]ProjectProcess `json:"processes,omitempty"`
	SeenEventIDs      map[string]bool           `json:"seen_event_ids,omitempty"`
	ProcessedEvents   int                       `json:"processed_events"`
	UpdatedAtUnixNano int64                     `json:"updated_at_unix_nano,omitempty"`
}

type ProjectProcess = domain.ProcessReference

type TaskWorkflowInput struct {
	ProjectID        string                `json:"project_id"`
	Event            domain.Event          `json:"event"`
	AdditionalEvents []domain.Event        `json:"additional_events,omitempty"`
	ResumedFromPause bool                  `json:"resumed_from_pause,omitempty"`
	Decision         *agent.DecisionOutput `json:"decision,omitempty"`
}

type TaskWorkflowResult struct {
	Completed bool                 `json:"completed"`
	Decision  agent.DecisionOutput `json:"decision"`
	Processes []ProjectProcess     `json:"processes,omitempty"`
}
