package domain

import (
	"encoding/json"
	"strings"
	"time"
)

type Metadata map[string]string

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
	ToolTypeExec           ToolType = "exec"
	ToolTypeEdit           ToolType = "edit"
	ToolTypeGlob           ToolType = "glob"
	ToolTypeGrep           ToolType = "grep"
	ToolTypeMemoryForget   ToolType = "memory_forget"
	ToolTypeMemoryRemember ToolType = "memory_remember"
	ToolTypeMemorySearch   ToolType = "memory_search"
	ToolTypeRead           ToolType = "read"
	ToolTypeSchedule       ToolType = "schedule"
	ToolTypeSkill          ToolType = "skill"
	ToolTypeWrite          ToolType = "write"
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
	ID         string           `json:"id"`
	ProjectID  string           `json:"project_id"`
	EventID    string           `json:"event_id,omitempty"`
	Role       ConversationRole `json:"role"`
	Body       string           `json:"body,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Metadata   Metadata         `json:"metadata,omitempty"`
	CreatedAt  time.Time        `json:"created_at"`
}

type MemoryScope string

const (
	MemoryScopeProject MemoryScope = "project"
	MemoryScopeGlobal  MemoryScope = "global"
)

type Memory struct {
	ID         string      `json:"id"`
	ProjectID  string      `json:"project_id,omitempty"`
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
	ProjectID      string        `json:"project_id,omitempty"`
	Query          string        `json:"query,omitempty"`
	Scopes         []MemoryScope `json:"scopes,omitempty"`
	Limit          int           `json:"limit,omitempty"`
	FallbackRecent bool          `json:"fallback_recent,omitempty"`
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
