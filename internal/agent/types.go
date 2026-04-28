package agent

import (
	"context"
	"encoding/json"

	"github.com/opencto/opencto/internal/domain"
)

type Context struct {
	Event            domain.Event      `json:"event"`
	Project          domain.Project    `json:"project"`
	ActiveWorkItems  []domain.WorkItem `json:"active_work_items,omitempty"`
	AdditionalEvents []domain.Event    `json:"additional_events,omitempty"`
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
	Decision      DecisionOutput     `json:"decision"`
	ToolChoice    *ToolChoice        `json:"tool_choice,omitempty"`
	WorkItemID    string             `json:"work_item_id,omitempty"`
	Observation   *ExecutionFeedback `json:"observation,omitempty"`
	FinalAnswer   string             `json:"final_answer,omitempty"`
	Status        string             `json:"status"`
	AssistantText string             `json:"assistant_text,omitempty"`
}

type DecisionOutput struct {
	WorkItems       []domain.WorkItem `json:"work_items,omitempty"`
	ToolChoice      ToolChoice        `json:"tool_choice,omitempty,omitzero"`
	ResponseMessage string            `json:"response_message,omitempty"`
}

type RuntimeContext struct {
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	Shell         string `json:"shell,omitempty"`
	Path          string `json:"path,omitempty"`
	WorkspaceRoot string `json:"workspace_root,omitempty"`
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
	Decision           DecisionOutput
	Runtime            RuntimeContext
	ExecutionCycle     int
	ForceFinal         bool
	ResumedFromPause   bool
	LastObservation    *ExecutionFeedback
	ObservationHistory []ExecutionFeedback
}

type Engine interface {
	NextAction(context.Context, NextActionInput) (NextActionOutput, error)
}
