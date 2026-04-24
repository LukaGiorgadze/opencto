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
	ProjectID                string
	ContinueAsNewAfterEvents int
	Snapshot                 *ProjectWorkflowState
}

type ApprovalDecisionSignal = signals.ApprovalDecisionSignal
type EnqueueEventSignal = signals.EnqueueEventSignal
type ContradictionResolutionSignal = signals.ContradictionResolutionSignal

type ProjectWorkflowState struct {
	ProjectID         string
	Queue             []domain.Event
	ResumeQueue       []PausedTaskState
	ActiveTaskID      string
	PausedByApproval  map[string]PausedTaskState
	ProcessedEvents   int
	UpdatedAtUnixNano int64
}

type PausedTaskState struct {
	ApprovalID string
	Event      domain.Event
	Decision   agent.DecisionOutput
}

type TaskWorkflowInput struct {
	ProjectID        string
	Event            domain.Event
	ResumedFromPause bool
	ApprovalID       string
	Decision         *agent.DecisionOutput
}

type TaskWorkflowResult struct {
	Completed        bool
	AwaitingApproval bool
	ApprovalRequest  *domain.ApprovalRequest
	Decision         agent.DecisionOutput
}
