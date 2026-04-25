package agent

import (
	"context"

	"github.com/opencto/opencto/internal/domain"
)

type Context struct {
	Event              domain.Event                  `json:"event"`
	Project            domain.Project                `json:"project"`
	ConversationMemory []domain.MemoryFact           `json:"conversation_memory,omitempty"`
	ProjectFacts       []domain.MemoryFact           `json:"project_facts,omitempty"`
	OpenContradictions []domain.PendingContradiction `json:"open_contradictions,omitempty"`
	Integrations       []domain.Integration          `json:"integrations,omitempty"`
	ActiveWorkItems    []domain.WorkItem             `json:"active_work_items,omitempty"`
	RecentDecisions    []domain.ADR                  `json:"recent_decisions,omitempty"`
}

type ClassificationIntent string

const (
	ClassificationIntentActionRequest ClassificationIntent = "ACTION_REQUEST"
	ClassificationIntentQuestion      ClassificationIntent = "QUESTION"
	ClassificationIntentContextUpdate ClassificationIntent = "CONTEXT_UPDATE"
	ClassificationIntentApproval      ClassificationIntent = "APPROVAL"
	ClassificationIntentRejection     ClassificationIntent = "REJECTION"
	ClassificationIntentIncident      ClassificationIntent = "INCIDENT_REPORT"
	ClassificationIntentNeutral       ClassificationIntent = "NEUTRAL"
	ClassificationIntentAmbiguous     ClassificationIntent = "AMBIGUOUS"
)

type ClassificationRoute string

const (
	ClassificationRouteClarify ClassificationRoute = "clarify"
	ClassificationRoutePlan    ClassificationRoute = "plan"
	ClassificationRouteExecute ClassificationRoute = "execute"
	ClassificationRouteAnswer  ClassificationRoute = "answer"
	ClassificationRouteIngest  ClassificationRoute = "ingest"
	ClassificationRouteIgnore  ClassificationRoute = "ignore"
)

type Classification struct {
	Intent             ClassificationIntent `json:"intent"`
	Tier               domain.RiskTier      `json:"tier"`
	Confidence         float64              `json:"confidence"`
	NeedsClarification bool                 `json:"needs_clarification"`
	ContradictionRisk  bool                 `json:"contradiction_risk"`
	RoutedTo           ClassificationRoute  `json:"routed_to"`
	Summary            string               `json:"summary"`
}

func (c Classification) IsZero() bool {
	return c.Intent == "" &&
		c.Tier == 0 &&
		c.Confidence == 0 &&
		!c.NeedsClarification &&
		!c.ContradictionRisk &&
		c.RoutedTo == "" &&
		c.Summary == ""
}

func (c Classification) RequiresClarification() bool {
	return c.NeedsClarification || c.RoutedTo == ClassificationRouteClarify
}

func (c Classification) PlanDecision() domain.DecisionKind {
	switch c.RoutedTo {
	case ClassificationRouteClarify:
		return domain.DecisionKindClarify
	case ClassificationRouteExecute, ClassificationRouteAnswer:
		return domain.DecisionKindAct
	default:
		return domain.DecisionKindPlan
	}
}

type ClarificationRequest struct {
	Reason          string   `json:"reason"`
	Message         string   `json:"message,omitempty"`
	Questions       []string `json:"questions"`
	KnownSummary    string   `json:"known_summary,omitempty"`
	BlockingGaps    []string `json:"blocking_gaps,omitempty"`
	Assumptions     []string `json:"assumptions,omitempty"`
	ConfidenceAfter float64  `json:"confidence_after,omitempty"`
}

type PlanningOutput struct {
	Plan      domain.Plan       `json:"plan"`
	WorkItems []domain.WorkItem `json:"work_items,omitempty"`
}

type DependencyAudit struct {
	Dependency string `json:"dependency"`
	Approved   bool   `json:"approved"`
	Reason     string `json:"reason"`
}

type ToolChoice struct {
	Type         domain.ToolType   `json:"type"`
	Intent       string            `json:"intent"`
	Command      string            `json:"command,omitempty"`
	Args         []string          `json:"args,omitempty"`
	WorkingDir   string            `json:"working_dir,omitempty"`
	TimeoutMs    int               `json:"timeout_ms,omitempty"`
	InputSummary string            `json:"input_summary,omitempty"`
	Destructive  bool              `json:"destructive,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
}

func (c ToolChoice) IsZero() bool {
	return c.Type == "" &&
		c.Intent == "" &&
		c.Command == "" &&
		len(c.Args) == 0 &&
		c.WorkingDir == "" &&
		c.TimeoutMs == 0 &&
		c.InputSummary == "" &&
		!c.Destructive &&
		len(c.Metadata) == 0
}

type ExecutionFeedback struct {
	Cycle           int               `json:"cycle"`
	WorkItemID      string            `json:"work_item_id,omitempty"`
	Tool            domain.ToolType   `json:"tool"`
	Status          string            `json:"status"`
	RequestedAction string            `json:"requested_action,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	Observation     string            `json:"observation,omitempty"`
	Error           string            `json:"error,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type ToolSelection struct {
	ToolChoice  *ToolChoice  `json:"tool_choice,omitempty"`
	ToolChoices []ToolChoice `json:"tool_choices,omitempty"`
}

type DecisionOutput struct {
	Classification  Classification        `json:"classification,omitzero"`
	Clarification   *ClarificationRequest `json:"clarification,omitempty"`
	Plan            domain.Plan           `json:"plan,omitzero"`
	WorkItems       []domain.WorkItem     `json:"work_items,omitempty"`
	ToolChoice      ToolChoice            `json:"tool_choice,omitempty,omitzero"`
	ToolChoices     []ToolChoice          `json:"tool_choices,omitempty"`
	ResponseMessage string                `json:"response_message,omitempty"`
	DependencyAudit *DependencyAudit      `json:"dependency_audit,omitempty"`
}

type DecisionInput struct {
	ProjectID string  `json:"project_id"`
	Context   Context `json:"context"`
}

type RuntimeContext struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Shell         string `json:"shell,omitempty"`
	Path          string `json:"path,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
}

type ClarificationInput struct {
	ProjectID      string
	Context        Context
	Classification Classification
}

type PlanningInput struct {
	ProjectID         string
	Context           Context
	Classification    Classification
	AutonomyThreshold int
	AvailableSkills   []string
}

type ToolSelectionInput struct {
	ProjectID          string
	Context            Context
	Classification     Classification
	Plan               domain.Plan
	WorkItems          []domain.WorkItem
	CurrentWorkItemID  string
	Runtime            RuntimeContext
	ExecutionCycle     int
	LastObservation    *ExecutionFeedback
	ObservationHistory []ExecutionFeedback
}

type Engine interface {
	Classify(context.Context, DecisionInput) (Classification, error)
	Clarify(context.Context, ClarificationInput) (*ClarificationRequest, error)
	Plan(context.Context, PlanningInput) (PlanningOutput, error)
	SelectTool(context.Context, ToolSelectionInput) (ToolSelection, error)
}
