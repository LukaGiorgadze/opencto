package activities

import (
	"encoding/json"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

type NextActionRequest struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	AdditionalEvents   []domain.Event            `json:"additional_events,omitempty"`
	NextAction         agent.NextAction          `json:"next_action"`
	LastResult         *ExecuteToolResult        `json:"last_result,omitempty"`
	LastResults        []ExecuteToolResult       `json:"last_results,omitempty"`
	ObservationHistory []agent.ExecutionFeedback `json:"observation_history,omitempty"`
	Processes          []domain.ProcessReference `json:"processes,omitempty"`
	ExecutionCycle     int                       `json:"execution_cycle"`
	ForceFinal         bool                      `json:"force_final,omitempty"`
	ResumedFromPause   bool                      `json:"resumed_from_pause,omitempty"`
	Completion         *TaskCompletionRequest    `json:"completion,omitempty"`
	SubAgent           *agent.SubAgentContext    `json:"sub_agent,omitempty"`
	ToolAllowlist      []domain.ToolType         `json:"tool_allowlist,omitempty"`
	RestrictTools      bool                      `json:"restrict_tools,omitempty"`
}

type NextActionResult struct {
	NextAction   agent.NextAction          `json:"next_action"`
	ToolChoices  []agent.ToolChoice        `json:"tool_choices,omitempty"`
	WorkItemID   string                    `json:"work_item_id,omitempty"`
	Observation  *agent.ExecutionFeedback  `json:"observation,omitempty"`
	Observations []agent.ExecutionFeedback `json:"observations,omitempty"`
	Status       string                    `json:"status"`
	Processes    []domain.ProcessReference `json:"processes,omitempty"`
}

type ExecuteToolRequest struct {
	ProjectID  string           `json:"project_id"`
	WorkItemID string           `json:"work_item_id"`
	Event      domain.Event     `json:"event"`
	ToolChoice agent.ToolChoice `json:"tool_choice"`
}

type TaskCompletionRequest struct {
	Status    string                    `json:"status"`
	Processes []domain.ProcessReference `json:"processes,omitempty"`
}

type ReportResponseRequest struct {
	Event       domain.Event              `json:"event"`
	Message     string                    `json:"message"`
	Attachments []domain.ReportAttachment `json:"attachments,omitempty"`
	ReplyTo     *domain.ReportReply       `json:"reply_to,omitempty"`
}

type ReportResponseResult struct {
	Receipts []domain.ReportReceipt `json:"receipts,omitempty"`
}

type ResponseSessionRequest struct {
	ProjectID              string       `json:"project_id"`
	Event                  domain.Event `json:"event"`
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds,omitempty"`
	MaxDurationSeconds     int          `json:"max_duration_seconds,omitempty"`
}

type PersistEventRequest struct {
	Event domain.Event `json:"event"`
}

type CompressConversationRequest struct {
	Event domain.Event `json:"event"`
}

type CompressConversationResult struct {
	Summarized   bool   `json:"summarized"`
	SummaryID    string `json:"summary_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
	SourceChars  int    `json:"source_chars,omitempty"`
}

type PersistNextActionRequest struct {
	Event            domain.Event     `json:"event"`
	NextAction       agent.NextAction `json:"next_action"`
	Status           string           `json:"status,omitempty"`
	SkipConversation bool             `json:"skip_conversation,omitempty"`
}

type PersistToolResultRequest struct {
	Event            domain.Event      `json:"event"`
	Result           ExecuteToolResult `json:"result"`
	SkipConversation bool              `json:"skip_conversation,omitempty"`
}

type CompressAgentObservationsRequest struct {
	ProjectID       string                    `json:"project_id"`
	Goal            string                    `json:"goal,omitempty"`
	PreviousSummary string                    `json:"previous_summary,omitempty"`
	Observations    []agent.ExecutionFeedback `json:"observations,omitempty"`
}

type CompressAgentObservationsResult struct {
	Summarized             bool                      `json:"summarized"`
	Summary                string                    `json:"summary,omitempty"`
	RemainingObservations  []agent.ExecutionFeedback `json:"remaining_observations,omitempty"`
	MessageCount           int                       `json:"message_count,omitempty"`
	SourceChars            int                       `json:"source_chars,omitempty"`
	CompressionUnavailable bool                      `json:"compression_unavailable,omitempty"`
}

type ExecuteToolResult struct {
	Cycle            int                       `json:"cycle"`
	WorkItemID       string                    `json:"work_item_id,omitempty"`
	ToolCallID       string                    `json:"tool_call_id,omitempty"`
	Tool             domain.ToolType           `json:"tool,omitempty"`
	Status           domain.ExecutionStatus    `json:"status"`
	RequestedAction  string                    `json:"requested_action,omitempty"`
	Command          string                    `json:"command,omitempty"`
	Args             []string                  `json:"args,omitempty"`
	Input            json.RawMessage           `json:"input,omitempty"`
	Observation      string                    `json:"observation,omitempty"`
	Error            string                    `json:"error,omitempty"`
	WorkingDirectory string                    `json:"working_directory,omitempty"`
	ResultCode       string                    `json:"result_code,omitempty"`
	Metadata         map[string]string         `json:"metadata,omitempty"`
	Processes        []domain.ProcessReference `json:"processes,omitempty"`
	ExecutionAttempt domain.ExecutionAttempt   `json:"execution_attempt,omitempty"`
	ToolInvocation   domain.ToolInvocation     `json:"tool_invocation,omitempty"`
}

func (r NextActionResult) IsTerminal() bool {
	return r.Status != NextActionStatusTool
}
