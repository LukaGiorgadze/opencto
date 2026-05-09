package domain

import (
	"encoding/json"
	"strings"
	"time"
)

type Metadata map[string]string

const (
	MetadataKeyControl             = "control"
	MetadataKeyApprovalDecision    = "approval_decision"
	MetadataKeyPlanningToken       = "planning_token"
	MetadataKeyPlanningTokenSource = "planning_token_source"
	MetadataKeyReplyToMessageID    = "reply_to_message_id"
	MetadataKeyReplyToChannelID    = "reply_to_channel_id"
	MetadataKeyReplyToContextID    = "reply_to_context_id"
	MetadataKeyReplyToActorID      = "reply_to_actor_id"
	MetadataKeyWaitingKind         = "waiting_kind"
)

const (
	MetadataControlPlanningAnswer = "planning_answer"
	MetadataControlTaskReply      = "task_reply"
)

const (
	MetadataApprovalApproved = "approved"
	MetadataApprovalRevision = "revision"
)

type Provenance struct {
	Source     string    `json:"source"`
	SourceID   string    `json:"source_id,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
	Metadata   Metadata  `json:"metadata,omitempty"`
}

type ChannelType string

const (
	ChannelTypeDiscord ChannelType = "discord"
	ChannelTypeLocal   ChannelType = "local"
)

type EventKind string

const (
	EventKindMessage EventKind = "message"
	EventKindSystem  EventKind = "system"
)

type WorkItemStatus string

const (
	WorkItemStatusPending   WorkItemStatus = "pending"
	WorkItemStatusReady     WorkItemStatus = "ready"
	WorkItemStatusRunning   WorkItemStatus = "running"
	WorkItemStatusCompleted WorkItemStatus = "completed"
	WorkItemStatusFailed    WorkItemStatus = "failed"
	WorkItemStatusBlocked   WorkItemStatus = "blocked"
)

type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
)

type ToolRunMode string

const (
	ToolRunModeWaitForExit     ToolRunMode = "wait_for_exit"
	ToolRunModeStartBackground ToolRunMode = "start_background"
)

type ToolIdempotency string

const (
	ToolIdempotencyReadOnly      ToolIdempotency = "read_only"
	ToolIdempotencyIdempotent    ToolIdempotency = "idempotent"
	ToolIdempotencyNonIdempotent ToolIdempotency = "non_idempotent"
	ToolIdempotencyUnknown       ToolIdempotency = "unknown"
)

type ProcessScope string

const (
	ProcessScopeStopOnFinish ProcessScope = "stop_on_finish"
	ProcessScopeProject      ProcessScope = "project"
)

type ToolType string

const (
	ToolTypeExec                ToolType = "Exec"
	ToolTypeEdit                ToolType = "Edit"
	ToolTypeGlob                ToolType = "Glob"
	ToolTypeGrep                ToolType = "Grep"
	ToolTypeMemoryList          ToolType = "MemoryList"
	ToolTypeMemoryProposeAdd    ToolType = "MemoryProposeAdd"
	ToolTypeMemoryProposeForget ToolType = "MemoryProposeForget"
	ToolTypeMemoryProposeUpdate ToolType = "MemoryProposeUpdate"
	ToolTypeMemorySearch        ToolType = "MemorySearch"
	ToolTypeAskUserQuestion     ToolType = "AskUserQuestion"
	ToolTypeProposePlan         ToolType = "ProposePlan"
	ToolTypeRead                ToolType = "Read"
	ToolTypeSchedule            ToolType = "Schedule"
	ToolTypeSkill               ToolType = "Skill"
	ToolTypeWrite               ToolType = "Write"
)

type Project struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Channels    []ChannelBinding `json:"channels,omitempty"`
	Metadata    Metadata         `json:"metadata,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type ChannelBinding struct {
	ID          string      `json:"id"`
	ProjectID   string      `json:"project_id"`
	ChannelID   string      `json:"channel_id"`
	ChannelType ChannelType `json:"channel_type"`
	Hint        string      `json:"hint,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type Event struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	Kind        EventKind      `json:"kind"`
	ChannelID   string         `json:"channel_id,omitempty"`
	ChannelType ChannelType    `json:"channel_type,omitempty"`
	ThreadID    string         `json:"thread_id,omitempty"`
	ActorID     string         `json:"actor_id,omitempty"`
	ActorName   string         `json:"actor_name,omitempty"`
	Body        string         `json:"body"`
	Metadata    Metadata       `json:"metadata,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	Provenance  Provenance     `json:"provenance"`
	CreatedAt   time.Time      `json:"created_at"`
}

type EventAttachment struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	EventID     string    `json:"event_id"`
	Source      string    `json:"source"`
	SourceID    string    `json:"source_id,omitempty"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type,omitempty"`
	SizeBytes   int64     `json:"size_bytes,omitempty"`
	URL         string    `json:"url,omitempty"`
	LocalPath   string    `json:"local_path,omitempty"`
	Metadata    Metadata  `json:"metadata,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type ReportMessage struct {
	Text        string             `json:"text,omitempty"`
	Attachments []ReportAttachment `json:"attachments,omitempty"`
	ReplyTo     *ReportReply       `json:"reply_to,omitempty"`
}

func (m ReportMessage) Empty() bool {
	return strings.TrimSpace(m.Text) == "" && len(m.Attachments) == 0
}

type ReportAttachment struct {
	Path        string   `json:"path"`
	Filename    string   `json:"filename,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Description string   `json:"description,omitempty"`
	SizeBytes   int64    `json:"size_bytes,omitempty"`
	Metadata    Metadata `json:"metadata,omitempty"`
}

type ReportReply struct {
	MessageID string `json:"message_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
}

func (r ReportReply) Empty() bool {
	return strings.TrimSpace(r.MessageID) == "" && strings.TrimSpace(r.ChannelID) == "" && strings.TrimSpace(r.ContextID) == ""
}

type ReportReceipt struct {
	MessageID string `json:"message_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	ContextID string `json:"context_id,omitempty"`
	ThreadID  string `json:"thread_id,omitempty"`
}

type WorkItem struct {
	ID          string         `json:"id"`
	ProjectID   string         `json:"project_id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Status      WorkItemStatus `json:"status"`
	Metadata    Metadata       `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

type ExecutionAttempt struct {
	ID            string          `json:"id"`
	ProjectID     string          `json:"project_id"`
	WorkItemID    string          `json:"work_item_id"`
	Status        ExecutionStatus `json:"status"`
	Attempt       int             `json:"attempt"`
	Tool          ToolType        `json:"tool"`
	Summary       string          `json:"summary,omitempty"`
	OutputSummary string          `json:"output_summary,omitempty"`
	Metadata      Metadata        `json:"metadata,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
}

type ToolInvocation struct {
	ID                 string          `json:"id"`
	ProjectID          string          `json:"project_id"`
	ExecutionAttemptID string          `json:"execution_attempt_id"`
	RequestedIntent    string          `json:"requested_intent"`
	ChosenTool         ToolType        `json:"chosen_tool"`
	FallbackCandidates []ToolType      `json:"fallback_candidates,omitempty"`
	WorkingDirectory   string          `json:"working_directory,omitempty"`
	TimeoutSeconds     int             `json:"timeout_seconds"`
	InputSummary       string          `json:"input_summary,omitempty"`
	InputPayload       json.RawMessage `json:"input_payload,omitempty"`
	OutputSummary      string          `json:"output_summary,omitempty"`
	OutputPayload      json.RawMessage `json:"output_payload,omitempty"`
	ResultCode         string          `json:"result_code,omitempty"`
	ErrorDetails       string          `json:"error_details,omitempty"`
	CompensationNotes  string          `json:"compensation_notes,omitempty"`
	Metadata           Metadata        `json:"metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	CompletedAt        *time.Time      `json:"completed_at,omitempty"`
}

type ConversationRole string

const (
	ConversationRoleUser      ConversationRole = "user"
	ConversationRoleAssistant ConversationRole = "assistant"
	ConversationRoleTool      ConversationRole = "tool"
)

type ConversationMessage struct {
	ID          string           `json:"id"`
	ProjectID   string           `json:"project_id"`
	EventID     string           `json:"event_id,omitempty"`
	Role        ConversationRole `json:"role"`
	ChannelType ChannelType      `json:"channel_type,omitempty"`
	ChannelID   string           `json:"channel_id,omitempty"`
	ThreadID    string           `json:"thread_id,omitempty"`
	Body        string           `json:"body,omitempty"`
	ToolCallID  string           `json:"tool_call_id,omitempty"`
	Metadata    Metadata         `json:"metadata,omitempty"`
	CreatedAt   time.Time        `json:"created_at"`
}

type ConversationThread struct {
	ID            string      `json:"id"`
	ProjectID     string      `json:"project_id"`
	ChannelType   ChannelType `json:"channel_type"`
	ChannelID     string      `json:"channel_id"`
	ThreadID      string      `json:"thread_id"`
	RootMessageID string      `json:"root_message_id,omitempty"`
	WorkflowID    string      `json:"workflow_id,omitempty"`
	EventID       string      `json:"event_id,omitempty"`
	Title         string      `json:"title,omitempty"`
	Status        string      `json:"status,omitempty"`
	Metadata      Metadata    `json:"metadata,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	LastMessageAt time.Time   `json:"last_message_at"`
}

type ConversationSummaryScope string

const (
	ConversationSummaryScopeProject ConversationSummaryScope = "project"
	ConversationSummaryScopeChannel ConversationSummaryScope = "channel"
	ConversationSummaryScopeThread  ConversationSummaryScope = "thread"
)

type ConversationSummary struct {
	ID            string                   `json:"id"`
	ProjectID     string                   `json:"project_id"`
	ChannelType   ChannelType              `json:"channel_type,omitempty"`
	ChannelID     string                   `json:"channel_id,omitempty"`
	ThreadID      string                   `json:"thread_id,omitempty"`
	Scope         ConversationSummaryScope `json:"scope"`
	Summary       string                   `json:"summary"`
	FromMessageID string                   `json:"from_message_id"`
	ToMessageID   string                   `json:"to_message_id"`
	FromCreatedAt time.Time                `json:"from_created_at"`
	ToCreatedAt   time.Time                `json:"to_created_at"`
	MessageCount  int                      `json:"message_count"`
	SourceChars   int                      `json:"source_chars"`
	Metadata      Metadata                 `json:"metadata,omitempty"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
}

type MemoryScope string

const (
	MemoryScopeThread  MemoryScope = "thread"
	MemoryScopeProject MemoryScope = "project"
	MemoryScopeUser    MemoryScope = "user"
	MemoryScopeGlobal  MemoryScope = "global"
)

type Memory struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"project_id,omitempty"`
	UserID     string      `json:"user_id,omitempty"`
	ThreadID   string      `json:"thread_id,omitempty"`
	Scope      MemoryScope `json:"scope"`
	Kind       string      `json:"kind,omitempty"`
	Content    string      `json:"content"`
	Tags       []string    `json:"tags,omitempty"`
	Source     string      `json:"source,omitempty"`
	SourceID   string      `json:"source_id,omitempty"`
	Actor      string      `json:"actor,omitempty"`
	Confidence float64     `json:"confidence,omitempty"`
	Pinned     bool        `json:"pinned,omitempty"`
	Metadata   Metadata    `json:"metadata,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}

type MemorySearchRequest struct {
	ProjectID           string        `json:"project_id,omitempty"`
	UserID              string        `json:"user_id,omitempty"`
	ThreadID            string        `json:"thread_id,omitempty"`
	Query               string        `json:"query,omitempty"`
	Scopes              []MemoryScope `json:"scopes,omitempty"`
	Tags                []string      `json:"tags,omitempty"`
	QueryEmbedding      []float32     `json:"query_embedding,omitempty"`
	EmbeddingProvider   string        `json:"embedding_provider,omitempty"`
	EmbeddingModel      string        `json:"embedding_model,omitempty"`
	EmbeddingDimensions int           `json:"embedding_dimensions,omitempty"`
	Limit               int           `json:"limit,omitempty"`
	FallbackRecent      bool          `json:"fallback_recent,omitempty"`
}

type MemoryListRequest struct {
	ProjectID string        `json:"project_id,omitempty"`
	UserID    string        `json:"user_id,omitempty"`
	ThreadID  string        `json:"thread_id,omitempty"`
	Scopes    []MemoryScope `json:"scopes,omitempty"`
	Kind      string        `json:"kind,omitempty"`
	Tags      []string      `json:"tags,omitempty"`
	Limit     int           `json:"limit,omitempty"`
}

type MemoryUpdateRequest struct {
	ProjectID   string   `json:"project_id,omitempty"`
	UserID      string   `json:"user_id,omitempty"`
	ThreadID    string   `json:"thread_id,omitempty"`
	MemoryID    string   `json:"memory_id"`
	Content     string   `json:"content,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	ReplaceTags bool     `json:"replace_tags,omitempty"`
	Confidence  *float64 `json:"confidence,omitempty"`
	Pinned      *bool    `json:"pinned,omitempty"`
}

type MemoryUpdateResult struct {
	Memory  Memory `json:"memory"`
	Updated bool   `json:"updated"`
}

type MemoryForgetRequest struct {
	ProjectID string        `json:"project_id,omitempty"`
	UserID    string        `json:"user_id,omitempty"`
	ThreadID  string        `json:"thread_id,omitempty"`
	MemoryIDs []string      `json:"memory_ids,omitempty"`
	Scopes    []MemoryScope `json:"scopes,omitempty"`
	Tags      []string      `json:"tags,omitempty"`
}

type MemoryForgetResult struct {
	DeletedMemoryIDs []string `json:"deleted_memory_ids,omitempty"`
}

type MemoryEmbedding struct {
	MemoryID    string    `json:"memory_id"`
	Provider    string    `json:"provider"`
	Model       string    `json:"model"`
	Dimensions  int       `json:"dimensions"`
	ContentHash string    `json:"content_hash"`
	Vector      []float32 `json:"vector"`
}

type ProcessStatus string

const (
	ProcessStatusStarting ProcessStatus = "starting"
	ProcessStatusRunning  ProcessStatus = "running"
	ProcessStatusExited   ProcessStatus = "exited"
	ProcessStatusStopped  ProcessStatus = "stopped"
	ProcessStatusFailed   ProcessStatus = "failed"
)

type ProcessReference struct {
	ID          string        `json:"id"`
	Description string        `json:"description,omitempty"`
	Status      ProcessStatus `json:"status"`
	Scope       ProcessScope  `json:"scope"`
}

type ManagedProcess struct {
	ID               string        `json:"id"`
	ProjectID        string        `json:"project_id"`
	WorkItemID       string        `json:"work_item_id,omitempty"`
	ToolCallID       string        `json:"tool_call_id,omitempty"`
	Command          string        `json:"command"`
	Args             []string      `json:"args,omitempty"`
	WorkingDirectory string        `json:"working_directory"`
	PID              int           `json:"pid"`
	PGID             int           `json:"pgid,omitempty"`
	Status           ProcessStatus `json:"status"`
	StdoutLogPath    string        `json:"stdout_log_path,omitempty"`
	StderrLogPath    string        `json:"stderr_log_path,omitempty"`
	StartedAt        time.Time     `json:"started_at"`
	UpdatedAt        time.Time     `json:"updated_at"`
	Metadata         Metadata      `json:"metadata,omitempty"`
}
