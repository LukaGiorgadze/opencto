package domain

import "time"

type Metadata map[string]string

type Provenance struct {
	Source        string            `json:"source"`
	SourceID      string            `json:"source_id,omitempty"`
	Actor         string            `json:"actor,omitempty"`
	ObservedAt    time.Time         `json:"observed_at"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type ChannelType string

const (
	ChannelTypeDiscord ChannelType = "discord"
	ChannelTypeLocal   ChannelType = "local"
)

type EventKind string

const (
	EventKindMessage          EventKind = "message"
	EventKindApprovalDecision EventKind = "approval_decision"
	EventKindSystem           EventKind = "system"
)

type WorkItemStatus string

const (
	WorkItemStatusPending          WorkItemStatus = "pending"
	WorkItemStatusPlanning         WorkItemStatus = "planning"
	WorkItemStatusAwaitingApproval WorkItemStatus = "awaiting_approval"
	WorkItemStatusReady            WorkItemStatus = "ready"
	WorkItemStatusRunning          WorkItemStatus = "running"
	WorkItemStatusCompleted        WorkItemStatus = "completed"
	WorkItemStatusFailed           WorkItemStatus = "failed"
	WorkItemStatusBlocked          WorkItemStatus = "blocked"
)

type PlanStatus string

const (
	PlanStatusDraft     PlanStatus = "draft"
	PlanStatusReady     PlanStatus = "ready"
	PlanStatusBlocked   PlanStatus = "blocked"
	PlanStatusExecuted  PlanStatus = "executed"
	PlanStatusAbandoned PlanStatus = "abandoned"
)

type ExecutionStatus string

const (
	ExecutionStatusPending   ExecutionStatus = "pending"
	ExecutionStatusRunning   ExecutionStatus = "running"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusCanceled  ExecutionStatus = "canceled"
)

type ApprovalStatus string

const (
	ApprovalStatusPending  ApprovalStatus = "pending"
	ApprovalStatusApproved ApprovalStatus = "approved"
	ApprovalStatusRejected ApprovalStatus = "rejected"
	ApprovalStatusExpired  ApprovalStatus = "expired"
)

type ContradictionStatus string

const (
	ContradictionStatusOpen     ContradictionStatus = "open"
	ContradictionStatusResolved ContradictionStatus = "resolved"
)

type MemoryCategory string

const (
	MemoryCategoryConversation  MemoryCategory = "conversation"
	MemoryCategoryProjectFact   MemoryCategory = "project_fact"
	MemoryCategoryContradiction MemoryCategory = "pending_contradiction"
	MemoryCategoryExecution     MemoryCategory = "execution_history"
	MemoryCategoryDecision      MemoryCategory = "decision"
)

type ToolType string

const (
	ToolTypeShell ToolType = "shell"
)

type ArtifactKind string

const (
	ArtifactKindLog       ArtifactKind = "log"
	ArtifactKindADR       ArtifactKind = "adr"
	ArtifactKindPlan      ArtifactKind = "plan"
	ArtifactKindExecution ArtifactKind = "execution"
)

type IntegrationStatus string

const (
	IntegrationStatusPending  IntegrationStatus = "pending"
	IntegrationStatusReady    IntegrationStatus = "ready"
	IntegrationStatusFailed   IntegrationStatus = "failed"
	IntegrationStatusDisabled IntegrationStatus = "disabled"
)

type RiskTier int

const (
	RiskTierObserve RiskTier = iota
	RiskTierSafeLocalChange
	RiskTierConsequential
	RiskTierOwnerApproval
)

type DecisionKind string

const (
	DecisionKindClarify DecisionKind = "clarify"
	DecisionKindPlan    DecisionKind = "plan"
	DecisionKindAct     DecisionKind = "act"
)

type Project struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Channels    []ChannelBinding  `json:"channels,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
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
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	Kind          EventKind         `json:"kind"`
	ChannelID     string            `json:"channel_id,omitempty"`
	ChannelType   ChannelType       `json:"channel_type,omitempty"`
	ActorID       string            `json:"actor_id,omitempty"`
	ActorName     string            `json:"actor_name,omitempty"`
	Body          string            `json:"body"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Payload       map[string]any    `json:"payload,omitempty"`
	Provenance    Provenance        `json:"provenance"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
}

type PlanStep struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description,omitempty"`
	ToolHint    ToolType          `json:"tool_hint,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type WorkItem struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	PlanID        string            `json:"plan_id,omitempty"`
	Title         string            `json:"title"`
	Description   string            `json:"description,omitempty"`
	Status        WorkItemStatus    `json:"status"`
	RiskTier      RiskTier          `json:"risk_tier"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

type Plan struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	EventID     string            `json:"event_id"`
	Status      PlanStatus        `json:"status"`
	Summary     string            `json:"summary"`
	Decision    DecisionKind      `json:"decision"`
	Steps       []PlanStep        `json:"steps,omitempty"`
	WorkItemIDs []string          `json:"work_item_ids,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type ExecutionAttempt struct {
	ID            string            `json:"id"`
	ProjectID     string            `json:"project_id"`
	WorkItemID    string            `json:"work_item_id"`
	Status        ExecutionStatus   `json:"status"`
	Attempt       int               `json:"attempt"`
	Tool          ToolType          `json:"tool"`
	Summary       string            `json:"summary,omitempty"`
	OutputSummary string            `json:"output_summary,omitempty"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	StartedAt     time.Time         `json:"started_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
}

type ApprovalRequest struct {
	ID              string            `json:"id"`
	ProjectID       string            `json:"project_id"`
	WorkItemID      string            `json:"work_item_id"`
	Status          ApprovalStatus    `json:"status"`
	RiskTier        RiskTier          `json:"risk_tier"`
	RequestedAction string            `json:"requested_action"`
	Reason          string            `json:"reason"`
	RequestedBy     string            `json:"requested_by,omitempty"`
	DecidedBy       string            `json:"decided_by,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	UpdatedAt       time.Time         `json:"updated_at"`
	DecidedAt       *time.Time        `json:"decided_at,omitempty"`
}

type PendingContradiction struct {
	ID           string              `json:"id"`
	ProjectID    string              `json:"project_id"`
	Status       ContradictionStatus `json:"status"`
	Topic        string              `json:"topic"`
	ExistingFact string              `json:"existing_fact,omitempty"`
	IncomingFact string              `json:"incoming_fact,omitempty"`
	Resolution   string              `json:"resolution,omitempty"`
	Metadata     map[string]string   `json:"metadata,omitempty"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
}

type MemoryFact struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	Category    MemoryCategory    `json:"category"`
	Key         string            `json:"key"`
	Value       string            `json:"value"`
	Status      string            `json:"status,omitempty"`
	EmbeddingID string            `json:"embedding_id,omitempty"`
	Provenance  Provenance        `json:"provenance"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type ToolInvocation struct {
	ID                 string            `json:"id"`
	ProjectID          string            `json:"project_id"`
	ExecutionAttemptID string            `json:"execution_attempt_id"`
	RequestedIntent    string            `json:"requested_intent"`
	ChosenTool         ToolType          `json:"chosen_tool"`
	FallbackCandidates []ToolType        `json:"fallback_candidates,omitempty"`
	RiskTier           RiskTier          `json:"risk_tier"`
	WorkingDirectory   string            `json:"working_directory,omitempty"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	InputSummary       string            `json:"input_summary,omitempty"`
	OutputSummary      string            `json:"output_summary,omitempty"`
	ResultCode         string            `json:"result_code,omitempty"`
	ErrorDetails       string            `json:"error_details,omitempty"`
	CompensationNotes  string            `json:"compensation_notes,omitempty"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	CreatedAt          time.Time         `json:"created_at"`
	CompletedAt        *time.Time        `json:"completed_at,omitempty"`
}

type Artifact struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Kind      ArtifactKind      `json:"kind"`
	URI       string            `json:"uri"`
	Digest    string            `json:"digest,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type ADR struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Path      string            `json:"path"`
	CommitSHA string            `json:"commit_sha,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type Integration struct {
	ID          string            `json:"id"`
	ProjectID   string            `json:"project_id"`
	Kind        string            `json:"kind"`
	Status      IntegrationStatus `json:"status"`
	ExternalRef string            `json:"external_ref,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

type CredentialRef struct {
	ID        string            `json:"id"`
	ProjectID string            `json:"project_id"`
	Provider  string            `json:"provider"`
	Handle    string            `json:"handle"`
	Scope     string            `json:"scope,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}
