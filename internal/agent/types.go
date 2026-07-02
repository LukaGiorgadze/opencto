package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/skills"
)

type Context struct {
	Event                       domain.Event                 `json:"event"`
	Project                     domain.Project               `json:"project"`
	ActiveWorkItems             []domain.WorkItem            `json:"active_work_items,omitempty"`
	Memory                      []domain.Memory              `json:"memory,omitempty"`
	Conversation                []domain.ConversationMessage `json:"conversation,omitempty"`
	ConversationSummaries       []domain.ConversationSummary `json:"conversation_summaries,omitempty"`
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
	ToolChoices   []ToolChoice       `json:"tool_choices,omitempty"`
	WorkItemID    string             `json:"work_item_id,omitempty"`
	Observation   *ExecutionFeedback `json:"observation,omitempty"`
	Status        string             `json:"status"`
	AssistantText string             `json:"assistant_text,omitempty"`
}

type NextAction struct {
	WorkItems           []domain.WorkItem         `json:"work_items,omitempty"`
	ResponseMessage     string                    `json:"response_message,omitempty"`
	ResponseAttachments []domain.ReportAttachment `json:"response_attachments,omitempty"`
}

type RuntimeContext struct {
	OS                       string `json:"os"`
	Arch                     string `json:"arch"`
	Exec                     string `json:"exec,omitempty"`
	Path                     string `json:"path,omitempty"`
	WorkspaceRoot            string `json:"workspace_root,omitempty"`
	CurrentLocalTime         string `json:"current_local_time,omitempty"`
	CurrentUTCTime           string `json:"current_utc_time,omitempty"`
	HostTimeZone             string `json:"host_time_zone,omitempty"`
	HostTimeZoneError        string `json:"host_time_zone_error,omitempty"`
	AgentMailAPIKeyAvailable bool   `json:"agentmail_api_key_available,omitempty"`
}

type OnboardingContext struct {
	Active bool   `json:"active,omitempty"`
	Source string `json:"source,omitempty"`
	Status string `json:"status,omitempty"`
}

type LLMSession struct {
	ProjectID     string
	WorkflowID    string
	WorkflowRunID string
	RequestKind   string
}

type llmSessionContextKey struct{}

func ContextWithLLMSession(ctx context.Context, session LLMSession) context.Context {
	session.ProjectID = strings.TrimSpace(session.ProjectID)
	session.WorkflowID = strings.TrimSpace(session.WorkflowID)
	session.WorkflowRunID = strings.TrimSpace(session.WorkflowRunID)
	session.RequestKind = strings.TrimSpace(session.RequestKind)
	if session.ProjectID == "" && session.WorkflowID == "" && session.WorkflowRunID == "" && session.RequestKind == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, llmSessionContextKey{}, session)
}

func LLMSessionFromContext(ctx context.Context) LLMSession {
	if ctx == nil {
		return LLMSession{}
	}
	session, _ := ctx.Value(llmSessionContextKey{}).(LLMSession)
	session.ProjectID = strings.TrimSpace(session.ProjectID)
	session.WorkflowID = strings.TrimSpace(session.WorkflowID)
	session.WorkflowRunID = strings.TrimSpace(session.WorkflowRunID)
	session.RequestKind = strings.TrimSpace(session.RequestKind)
	return session
}

type ToolSelectionInput struct {
	ProjectID      string
	Context        Context
	Runtime        RuntimeContext
	ExecutionCycle int
	Onboarding     OnboardingContext
	ToolAllowlist  []domain.ToolType
	RestrictTools  bool
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
	Onboarding         OnboardingContext
	SubAgent           *SubAgentContext
	ToolAllowlist      []domain.ToolType
	RestrictTools      bool
}

type SubAgentContext struct {
	Goal       string `json:"goal"`
	Prompt     string `json:"prompt,omitempty"`
	RunSummary string `json:"run_summary,omitempty"`
	RunID      string `json:"run_id,omitempty"`
}

type Engine interface {
	NextAction(context.Context, NextActionInput) (NextActionOutput, error)
}

type ConversationCompressionInput struct {
	ProjectID       string                          `json:"project_id"`
	Scope           domain.ConversationSummaryScope `json:"scope"`
	Messages        []domain.ConversationMessage    `json:"messages"`
	MaxSummaryChars int                             `json:"max_summary_chars"`
}

type ConversationCompressionOutput struct {
	Summary string `json:"summary"`
}

type ConversationCompressor interface {
	CompressConversation(context.Context, ConversationCompressionInput) (ConversationCompressionOutput, error)
}

type AgentObservationCompressionInput struct {
	ProjectID       string              `json:"project_id"`
	Goal            string              `json:"goal,omitempty"`
	PreviousSummary string              `json:"previous_summary,omitempty"`
	Observations    []ExecutionFeedback `json:"observations,omitempty"`
	MaxSummaryChars int                 `json:"max_summary_chars"`
}

type AgentObservationCompressionOutput struct {
	Summary string `json:"summary"`
}

type AgentObservationCompressor interface {
	CompressAgentObservations(context.Context, AgentObservationCompressionInput) (AgentObservationCompressionOutput, error)
}
