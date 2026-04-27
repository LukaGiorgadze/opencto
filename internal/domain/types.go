package domain

import "time"

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

type ToolType string

const (
	ToolTypeShell ToolType = "shell"
	ToolTypeEdit  ToolType = "edit"
	ToolTypeGlob  ToolType = "glob"
	ToolTypeGrep  ToolType = "grep"
	ToolTypeRead  ToolType = "read"
	ToolTypeWrite ToolType = "write"
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
	ID                 string     `json:"id"`
	ProjectID          string     `json:"project_id"`
	ExecutionAttemptID string     `json:"execution_attempt_id"`
	RequestedIntent    string     `json:"requested_intent"`
	ChosenTool         ToolType   `json:"chosen_tool"`
	FallbackCandidates []ToolType `json:"fallback_candidates,omitempty"`
	WorkingDirectory   string     `json:"working_directory,omitempty"`
	TimeoutSeconds     int        `json:"timeout_seconds"`
	InputSummary       string     `json:"input_summary,omitempty"`
	OutputSummary      string     `json:"output_summary,omitempty"`
	ResultCode         string     `json:"result_code,omitempty"`
	ErrorDetails       string     `json:"error_details,omitempty"`
	CompensationNotes  string     `json:"compensation_notes,omitempty"`
	Metadata           Metadata   `json:"metadata,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
	CompletedAt        *time.Time `json:"completed_at,omitempty"`
}
