package workflows

import (
	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/signals"
)

const (
	ProjectWorkflowName       = "ProjectWorkflow"
	TaskWorkflowName          = "TaskWorkflow"
	ApprovalWorkflowName      = "ApprovalWorkflow"
	ContradictionWorkflowName = "ContradictionWorkflow"

	SignalEnqueueEvent          = "enqueue-event"
	SignalApprovalDecision      = "approval-decision"
	SignalContradictionResolved = "contradiction-resolved"

	QueryProjectState = "project-state"
)

type ProjectWorkflowInput struct {
	ProjectID                string                `json:"project_id"`
	ContinueAsNewAfterEvents int                   `json:"continue_as_new_after_events,omitempty"`
	Snapshot                 *ProjectWorkflowState `json:"snapshot,omitempty"`
}

type ApprovalDecisionSignal = signals.ApprovalDecisionSignal
type EnqueueEventSignal = signals.EnqueueEventSignal
type ContradictionResolutionSignal = signals.ContradictionResolutionSignal

type ProjectWorkflowState struct {
	ProjectID         string                     `json:"project_id"`
	Queue             []domain.Event             `json:"queue,omitempty"`
	ResumeQueue       []PausedTaskState          `json:"resume_queue,omitempty"`
	ActiveTaskID      string                     `json:"active_task_id,omitempty"`
	PausedByApproval  map[string]PausedTaskState `json:"paused_by_approval,omitempty"`
	ProcessedEvents   int                        `json:"processed_events"`
	UpdatedAtUnixNano int64                      `json:"updated_at_unix_nano,omitempty"`
}

type PausedTaskState struct {
	ApprovalID      string                  `json:"approval_id"`
	ApprovalRequest *domain.ApprovalRequest `json:"approval_request,omitempty"`
	Event           domain.Event            `json:"event"`
	Decision        agent.DecisionOutput    `json:"decision"`
}

type TaskWorkflowInput struct {
	ProjectID        string                  `json:"project_id"`
	Event            domain.Event            `json:"event"`
	ResumedFromPause bool                    `json:"resumed_from_pause,omitempty"`
	ApprovalID       string                  `json:"approval_id,omitempty"`
	ApprovalRequest  *domain.ApprovalRequest `json:"approval_request,omitempty"`
	Decision         *agent.DecisionOutput   `json:"decision,omitempty"`
}

type TaskWorkflowResult struct {
	Completed        bool                    `json:"completed"`
	AwaitingApproval bool                    `json:"awaiting_approval,omitempty"`
	ApprovalRequest  *domain.ApprovalRequest `json:"approval_request,omitempty"`
	Decision         agent.DecisionOutput    `json:"decision"`
}
