package agent

import (
	"context"
	"encoding/json"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/skills"
)

type Context struct {
	Event                       domain.Event                 `json:"event"`
	Project                     domain.Project               `json:"project"`
	ActiveWorkItems             []domain.WorkItem            `json:"active_work_items,omitempty"`
	Memory                      []domain.Memory              `json:"memory,omitempty"`
	Conversation                []domain.ConversationMessage `json:"conversation,omitempty"`
	ConversationMaxContextChars int                          `json:"conversation_max_context_chars,omitempty"`
	Skills                      []skills.Summary             `json:"skills,omitempty"`
	AdditionalEvents            []domain.Event               `json:"additional_events,omitempty"`
}

type ToolChoice struct {
	ToolCallID   string                 `json:"tool_call_id,omitempty"`
	Type         domain.ToolType        `json:"type"`
	Intent       string                 `json:"intent"`
	Command      string                 `json:"command,omitempty"`
	Args         []string               `json:"args,omitempty"`
	Input        json.RawMessage        `json:"input,omitempty"`
	WorkingDir   string                 `json:"working_dir,omitempty"`
	TimeoutMs    int                    `json:"timeout_ms,omitempty"`
	RunMode      domain.ToolRunMode     `json:"run_mode,omitempty"`
	Idempotency  domain.ToolIdempotency `json:"idempotency,omitempty"`
	ProcessScope domain.ProcessScope    `json:"process_scope,omitempty"`
	InputSummary string                 `json:"input_summary,omitempty"`
	Destructive  bool                   `json:"destructive,omitempty"`
	Metadata     domain.Metadata        `json:"metadata,omitempty"`
}

func (c ToolChoice) IsZero() bool {
	return c.ToolCallID == "" &&
		c.Type == "" &&
		c.Intent == "" &&
		c.Command == "" &&
		len(c.Args) == 0 &&
		len(c.Input) == 0 &&
		c.WorkingDir == "" &&
		c.TimeoutMs == 0 &&
		c.RunMode == "" &&
		c.Idempotency == "" &&
		c.ProcessScope == "" &&
		c.InputSummary == "" &&
		!c.Destructive &&
		len(c.Metadata) == 0
}

type ExecutionFeedback struct {
	Cycle           int             `json:"cycle"`
	WorkItemID      string          `json:"work_item_id,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	Tool            domain.ToolType `json:"tool"`
	Status          string          `json:"status"`
	RequestedAction string          `json:"requested_action,omitempty"`
	Command         string          `json:"command,omitempty"`
	Args            []string        `json:"args,omitempty"`
	Input           json.RawMessage `json:"input,omitempty"`
	Observation     string          `json:"observation,omitempty"`
	Error           string          `json:"error,omitempty"`
	Metadata        domain.Metadata `json:"metadata,omitempty"`
}

type NextActionOutput struct {
	NextAction    NextAction         `json:"next_action"`
	ToolChoice    *ToolChoice        `json:"tool_choice,omitempty"`
	ToolChoices   []ToolChoice       `json:"tool_choices,omitempty"`
	WorkItemID    string             `json:"work_item_id,omitempty"`
	WaitingToken  string             `json:"waiting_token,omitempty"`
	WaitingKind   string             `json:"waiting_kind,omitempty"`
	Observation   *ExecutionFeedback `json:"observation,omitempty"`
	Status        string             `json:"status"`
	AssistantText string             `json:"assistant_text,omitempty"`
}

type NextAction struct {
	WorkItems           []domain.WorkItem         `json:"work_items,omitempty"`
	ToolChoice          ToolChoice                `json:"tool_choice,omitempty,omitzero"`
	ResponseMessage     string                    `json:"response_message,omitempty"`
	ResponseAttachments []domain.ReportAttachment `json:"response_attachments,omitempty"`
	WaitingToken        string                    `json:"waiting_token,omitempty"`
	WaitingKind         string                    `json:"waiting_kind,omitempty"`
}

type RuntimeContext struct {
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	Exec              string `json:"exec,omitempty"`
	Path              string `json:"path,omitempty"`
	WorkspaceRoot     string `json:"workspace_root,omitempty"`
	OpenCTORoot       string `json:"opencto_root"`
	CurrentLocalTime  string `json:"current_local_time,omitempty"`
	CurrentUTCTime    string `json:"current_utc_time,omitempty"`
	HostTimeZone      string `json:"host_time_zone,omitempty"`
	HostTimeZoneError string `json:"host_time_zone_error,omitempty"`
}

type ToolSelectionInput struct {
	ProjectID      string
	Context        Context
	Runtime        RuntimeContext
	ExecutionCycle int
}

type NextActionInput struct {
	ProjectID          string
	Context            Context
	NextAction         NextAction
	Runtime            RuntimeContext
	ExecutionCycle     int
	ForceFinal         bool
	ResumedFromPause   bool
	LastObservation    *ExecutionFeedback
	ObservationHistory []ExecutionFeedback
	ChannelType        domain.ChannelType
}

type Engine interface {
	NextAction(context.Context, NextActionInput) (NextActionOutput, error)
}

type MemoryExtractionInput struct {
	ProjectID        string          `json:"project_id"`
	Event            domain.Event    `json:"event"`
	ExistingMemories []domain.Memory `json:"existing_memories,omitempty"`
}

type MemoryExtractionOutput struct {
	Candidates []MemoryCandidate `json:"candidates,omitempty"`
}

type MemoryCandidate struct {
	Scope      domain.MemoryScope `json:"scope"`
	Kind       string             `json:"kind"`
	Content    string             `json:"content"`
	Tags       []string           `json:"tags,omitempty"`
	Confidence float64            `json:"confidence,omitempty"`
	Pinned     bool               `json:"pinned,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

type MemoryExtractor interface {
	ExtractMemories(context.Context, MemoryExtractionInput) (MemoryExtractionOutput, error)
}
