package workflows

import (
	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/scheduled"
	"github.com/opencto/opencto/internal/runtime/signals"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
)

const (
	ProjectWorkflowName           = "ProjectWorkflow"
	TaskWorkflowName              = "TaskWorkflow"
	AgentWorkflowName             = "AgentWorkflow"
	ScheduledDispatchWorkflowName = scheduled.DispatchWorkflowName
	WorkflowRunWorkflowName       = workflowrun.WorkflowName

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

type AgentWorkflowInput struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	ParentWorkItemID   string                    `json:"parent_work_item_id"`
	ParentToolCallID   string                    `json:"parent_tool_call_id"`
	ParentWorkflowID   string                    `json:"parent_workflow_id,omitempty"`
	AgentWorkflowID    string                    `json:"agent_workflow_id,omitempty"`
	ToolChoice         agent.ToolChoice          `json:"tool_choice"`
	AdditionalEvents   []domain.Event            `json:"additional_events,omitempty"`
	ResumedFromPause   bool                      `json:"resumed_from_pause,omitempty"`
	InitialRunSummary  string                    `json:"initial_run_summary,omitempty"`
	InitialNextAction  *agent.NextAction         `json:"initial_next_action,omitempty"`
	InitialObservation []agent.ExecutionFeedback `json:"initial_observation,omitempty"`
	CompletionTool     *agent.ToolChoice         `json:"completion_tool,omitempty"`
}

type AgentWorkflowResult struct {
	Status       domain.ExecutionStatus    `json:"status"`
	Message      string                    `json:"message,omitempty"`
	TurnCount    int                       `json:"turn_count,omitempty"`
	FilesTouched []string                  `json:"files_touched,omitempty"`
	ToolsUsed    []domain.ToolType         `json:"tools_used,omitempty"`
	Processes    []domain.ProcessReference `json:"processes,omitempty"`
	Metadata     map[string]string         `json:"metadata,omitempty"`
	Error        string                    `json:"error,omitempty"`
}
