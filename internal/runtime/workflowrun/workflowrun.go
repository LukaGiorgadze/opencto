package workflowrun

import (
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/workflowbundle"
)

const (
	WorkflowName = "WorkflowRunWorkflow"

	DefaultRunRetention = 10

	StepFailureErrorType = "WorkflowStepFailed"

	PrepareActivityName     = "Activities.PrepareWorkflowRun"
	CleanupRunsActivityName = "Activities.CleanupWorkflowRuns"
	ExecuteStepName         = "Activities.ExecuteWorkflowStep"
	CompleteActivityName    = "Activities.CompleteWorkflowRun"
	NotifyFailureName       = "Activities.NotifyWorkflowFailure"
)

type Input struct {
	ProjectID        string       `json:"project_id"`
	WorkflowID       string       `json:"workflow_id"`
	WorkflowName     string       `json:"workflow_name,omitempty"`
	CommitHash       string       `json:"commit_hash"`
	ScheduleID       string       `json:"schedule_id,omitempty"`
	ScheduledAt      time.Time    `json:"scheduled_at"`
	SourceEvent      domain.Event `json:"source_event"`
	CreatedByEventID string       `json:"created_by_event_id,omitempty"`
}

type PrepareRequest struct {
	Input              Input  `json:"input"`
	TemporalWorkflowID string `json:"temporal_workflow_id,omitempty"`
	TemporalRunID      string `json:"temporal_run_id,omitempty"`
}

type PrepareResult struct {
	RunID    string                  `json:"run_id"`
	RunPath  string                  `json:"run_path"`
	Manifest workflowbundle.Manifest `json:"manifest"`
}

type CleanupRunsRequest struct {
	WorkflowID   string `json:"workflow_id"`
	CurrentRunID string `json:"current_run_id,omitempty"`
	KeepLast     int    `json:"keep_last,omitempty"`
}

type CleanupRunsResult struct {
	DeletedRunIDs []string `json:"deleted_run_ids,omitempty"`
}

type ExecuteStepRequest struct {
	ProjectID  string              `json:"project_id"`
	WorkflowID string              `json:"workflow_id"`
	CommitHash string              `json:"commit_hash"`
	RunID      string              `json:"run_id"`
	RunPath    string              `json:"run_path"`
	Step       workflowbundle.Step `json:"step"`
	Env        []string            `json:"env,omitempty"`
}

type ExecuteStepResult struct {
	StepID        string `json:"step_id"`
	ExitCode      int    `json:"exit_code"`
	StdoutLogPath string `json:"stdout_log_path,omitempty"`
	StderrLogPath string `json:"stderr_log_path,omitempty"`
	OutputSummary string `json:"output_summary,omitempty"`
}

type StepFailure struct {
	StepID        string   `json:"step_id"`
	Command       string   `json:"command"`
	Args          []string `json:"args,omitempty"`
	ExitCode      int      `json:"exit_code"`
	Error         string   `json:"error,omitempty"`
	OutputSummary string   `json:"output_summary,omitempty"`
	StdoutLogPath string   `json:"stdout_log_path,omitempty"`
	StderrLogPath string   `json:"stderr_log_path,omitempty"`
}

type CompleteRequest struct {
	ProjectID      string                 `json:"project_id"`
	WorkflowID     string                 `json:"workflow_id"`
	RunID          string                 `json:"run_id"`
	RunPath        string                 `json:"run_path,omitempty"`
	Status         domain.ExecutionStatus `json:"status"`
	FailureSummary string                 `json:"failure_summary,omitempty"`
}

type NotifyFailureRequest struct {
	Input          Input  `json:"input"`
	RunID          string `json:"run_id"`
	FailureSummary string `json:"failure_summary"`
}

func ScheduleID(projectID, workflowID string) string {
	projectID = slugify(projectID)
	workflowID = slugify(workflowID)
	if projectID == "" {
		projectID = "default"
	}
	return "opencto:" + projectID + ":workflow-schedule:" + workflowID
}

func slugify(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	id, err := workflowbundle.NormalizeWorkflowID(value)
	if err != nil {
		return ""
	}
	return id
}
