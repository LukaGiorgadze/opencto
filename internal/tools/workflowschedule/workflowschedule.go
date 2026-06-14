package workflowschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/storage"
	"github.com/opencto/opencto/internal/workflowbundle"
	"github.com/opencto/opencto/internal/workspace"
)

const (
	OperationCreate   = "create"
	OperationList     = "list"
	OperationDescribe = "describe"
	OperationUpdate   = "update"
	OperationDelete   = "delete"
	OperationPause    = "pause"
	OperationResume   = "resume"
	OperationTrigger  = "trigger"

	defaultListLimit = 20
)

var (
	ErrClientRequired                = errors.New("schedule client is required")
	ErrStoreRequired                 = errors.New("runtime store is required")
	ErrWorkspaceRequired             = errors.New("workspace root is required")
	ErrTaskQueueRequired             = errors.New("task queue is required")
	ErrOperationRequired             = errors.New("workflow schedule operation is required")
	ErrWorkflowIDRequired            = errors.New("workflow_id is required")
	ErrWorkflowExists                = errors.New("workflow already exists")
	ErrWorkflowNotFound              = errors.New("workflow not found")
	ErrWorkflowSourceNotFound        = errors.New("workflow source directory not found")
	ErrUnsupportedAuthoringOperation = errors.New("unsupported workflow authoring operation")
	ErrScheduleSpecMissing           = errors.New("schedule.cron or schedule.one_shot_at is required")
	ErrScheduleSpecMixed             = errors.New("schedule.cron and schedule.one_shot_at cannot both be set")
	ErrPastOneShot                   = errors.New("schedule.one_shot_at must be in the future")
)

const WorkflowAuthoringValidationErrorType = "WorkflowAuthoringValidation"

type workflowNotFoundError struct {
	workflowID string
}

func (e workflowNotFoundError) Error() string {
	return fmt.Sprintf("workflow %q not found", e.workflowID)
}

func (e workflowNotFoundError) Is(target error) bool {
	return target == ErrWorkflowNotFound
}

type workflowSourceNotFoundError struct {
	workflowID string
}

func (e workflowSourceNotFoundError) Error() string {
	return fmt.Sprintf("workflow %q source directory not found", e.workflowID)
}

func (e workflowSourceNotFoundError) Is(target error) bool {
	return target == ErrWorkflowSourceNotFound
}

type unsupportedAuthoringOperationError struct {
	operation string
}

func (e unsupportedAuthoringOperationError) Error() string {
	return fmt.Sprintf("unsupported workflow authoring operation %q", e.operation)
}

func (e unsupportedAuthoringOperationError) Is(target error) bool {
	return target == ErrUnsupportedAuthoringOperation
}

func IsNonRetryableAuthoringError(err error) bool {
	return errors.Is(err, ErrClientRequired) ||
		errors.Is(err, ErrStoreRequired) ||
		errors.Is(err, ErrWorkspaceRequired) ||
		errors.Is(err, ErrTaskQueueRequired) ||
		errors.Is(err, ErrOperationRequired) ||
		errors.Is(err, ErrWorkflowIDRequired) ||
		errors.Is(err, ErrWorkflowExists) ||
		errors.Is(err, ErrWorkflowNotFound) ||
		errors.Is(err, ErrWorkflowSourceNotFound) ||
		errors.Is(err, ErrUnsupportedAuthoringOperation)
}

type CreateRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	WorkflowID    string `json:"workflow_id"`
	Prompt        string `json:"prompt"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type UpdateRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	WorkflowID    string `json:"workflow_id"`
	Prompt        string `json:"prompt"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type DeleteRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`
	StateDir    string       `json:"-"`

	WorkflowID string `json:"workflow_id"`
}

type OperationRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	Operation        string `json:"operation"`
	WorkflowID       string `json:"workflow_id,omitempty"`
	Note             string `json:"note,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	IncludeCompleted bool   `json:"include_completed,omitempty"`
}

type Request struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`
	StateDir    string       `json:"-"`

	Operation        string `json:"operation"`
	WorkflowID       string `json:"workflow_id"`
	Prompt           string `json:"prompt"`
	CommitMessage    string `json:"commit_message"`
	CommitHash       string `json:"commit_hash"`
	Note             string `json:"note"`
	Limit            int    `json:"limit"`
	IncludeCompleted bool   `json:"include_completed"`
	Paused           bool   `json:"paused"`
}

type AuthoringRequest struct {
	ProjectID   string       `json:"project_id"`
	WorkItemID  string       `json:"work_item_id,omitempty"`
	ToolCallID  string       `json:"tool_call_id,omitempty"`
	Intent      string       `json:"intent,omitempty"`
	SourceEvent domain.Event `json:"source_event,omitempty"`

	Operation     string `json:"operation"`
	WorkflowID    string `json:"workflow_id"`
	Prompt        string `json:"prompt"`
	CommitMessage string `json:"commit_message,omitempty"`
}

type AuthoringPlan struct {
	Operation         string `json:"operation"`
	WorkflowID        string `json:"workflow_id"`
	WorkflowPath      string `json:"workflow_path"`
	AgentGoal         string `json:"agent_goal"`
	AgentPrompt       string `json:"agent_prompt"`
	CommitMessage     string `json:"commit_message,omitempty"`
	RemoveOnFailure   bool   `json:"remove_on_failure,omitempty"`
	RestoreCommitHash string `json:"restore_commit_hash,omitempty"`
}

type Result struct {
	Operation       string          `json:"operation"`
	WorkflowID      string          `json:"workflow_id,omitempty"`
	ScheduleID      string          `json:"schedule_id,omitempty"`
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	Message         string          `json:"message,omitempty"`
	CommitHash      string          `json:"commit_hash,omitempty"`
	WorkflowPath    string          `json:"workflow_path,omitempty"`
	TimeZone        string          `json:"time_zone,omitempty"`
	Cron            string          `json:"cron,omitempty"`
	OneShotAt       string          `json:"one_shot_at,omitempty"`
	Paused          bool            `json:"paused,omitempty"`
	NextActionTimes []string        `json:"next_action_times,omitempty"`
	Workflows       []WorkflowEntry `json:"workflows,omitempty"`
}

type WorkflowEntry struct {
	ID                 string `json:"id"`
	Name               string `json:"name,omitempty"`
	Description        string `json:"description,omitempty"`
	Status             string `json:"status,omitempty"`
	CommitHash         string `json:"commit_hash,omitempty"`
	TemporalScheduleID string `json:"temporal_schedule_id,omitempty"`
}

type Executor interface {
	Create(context.Context, CreateRequest) (Result, error)
	Update(context.Context, UpdateRequest) (Result, error)
	Delete(context.Context, DeleteRequest) (Result, error)
	Operation(context.Context, OperationRequest) (Result, error)
}

type SourcePublisher interface {
	PublishCurrentSource(context.Context, UpdateRequest) (Result, error)
}

type AuthoringExecutor interface {
	PrepareAuthoring(context.Context, AuthoringRequest) (AuthoringPlan, error)
	CleanupAuthoring(context.Context, AuthoringPlan) error
}

type Client interface {
	Create(context.Context, temporalclient.ScheduleOptions) (temporalclient.ScheduleHandle, error)
	List(context.Context, temporalclient.ScheduleListOptions) (temporalclient.ScheduleListIterator, error)
	GetHandle(context.Context, string) temporalclient.ScheduleHandle
}

type TemporalExecutor struct {
	Client          Client
	Store           storage.RuntimeStore
	TaskQueue       string
	WorkspaceRoot   string
	StateDir        string
	Now             func() time.Time
	ResolveTimeZone TimeZoneResolver
	Logger          *slog.Logger
}

type TimeZoneResolver func() (*time.Location, string, error)

func NewTemporalExecutor(client Client, store storage.RuntimeStore, taskQueue, workspaceRoot string, logger *slog.Logger) *TemporalExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &TemporalExecutor{
		Client:          client,
		Store:           store,
		TaskQueue:       taskQueue,
		WorkspaceRoot:   workspaceRoot,
		ResolveTimeZone: ResolveHostTimeZone,
		Logger:          logger,
	}
}

func (e *TemporalExecutor) Create(ctx context.Context, req CreateRequest) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return Result{}, err
	}
	return e.create(ctx, requestFromCreate(req))
}

func (e *TemporalExecutor) Update(ctx context.Context, req UpdateRequest) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return Result{}, err
	}
	return e.update(ctx, requestFromUpdate(req))
}

func (e *TemporalExecutor) PublishCurrentSource(ctx context.Context, req UpdateRequest) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return Result{}, err
	}
	return e.publishCurrentSource(ctx, requestFromUpdate(req))
}

func (e *TemporalExecutor) Delete(ctx context.Context, req DeleteRequest) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return Result{}, err
	}
	return e.delete(ctx, requestFromDelete(req))
}

func (e *TemporalExecutor) Operation(ctx context.Context, req OperationRequest) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return Result{}, err
	}
	request := requestFromOperation(req)
	switch normalizeOperation(req.Operation) {
	case OperationList:
		return e.list(ctx, request)
	case OperationDescribe:
		return e.describe(ctx, request)
	case OperationPause:
		return e.pause(ctx, request)
	case OperationResume:
		return e.resume(ctx, request)
	case OperationTrigger:
		return e.trigger(ctx, request)
	case "":
		return Result{}, ErrOperationRequired
	default:
		return Result{}, fmt.Errorf("unsupported workflow operation %q", req.Operation)
	}
}

func (e *TemporalExecutor) validate() error {
	if e == nil {
		return fmt.Errorf("workflow executor is nil")
	}
	if e.Client == nil {
		return ErrClientRequired
	}
	if e.Store == nil {
		return ErrStoreRequired
	}
	if strings.TrimSpace(e.TaskQueue) == "" {
		return ErrTaskQueueRequired
	}
	if strings.TrimSpace(e.WorkspaceRoot) == "" {
		return ErrWorkspaceRequired
	}
	return nil
}

func requestFromCreate(req CreateRequest) Request {
	return Request{
		ProjectID:     req.ProjectID,
		WorkItemID:    req.WorkItemID,
		ToolCallID:    req.ToolCallID,
		Intent:        req.Intent,
		SourceEvent:   req.SourceEvent,
		Operation:     OperationCreate,
		WorkflowID:    req.WorkflowID,
		Prompt:        req.Prompt,
		CommitMessage: req.CommitMessage,
	}
}

func requestFromUpdate(req UpdateRequest) Request {
	return Request{
		ProjectID:     req.ProjectID,
		WorkItemID:    req.WorkItemID,
		ToolCallID:    req.ToolCallID,
		Intent:        req.Intent,
		SourceEvent:   req.SourceEvent,
		Operation:     OperationUpdate,
		WorkflowID:    req.WorkflowID,
		Prompt:        req.Prompt,
		CommitMessage: req.CommitMessage,
	}
}

func requestFromDelete(req DeleteRequest) Request {
	return Request{
		ProjectID:   req.ProjectID,
		WorkItemID:  req.WorkItemID,
		ToolCallID:  req.ToolCallID,
		Intent:      req.Intent,
		SourceEvent: req.SourceEvent,
		StateDir:    req.StateDir,
		Operation:   OperationDelete,
		WorkflowID:  req.WorkflowID,
	}
}

func requestFromOperation(req OperationRequest) Request {
	return Request{
		ProjectID:        req.ProjectID,
		WorkItemID:       req.WorkItemID,
		ToolCallID:       req.ToolCallID,
		Intent:           req.Intent,
		SourceEvent:      req.SourceEvent,
		Operation:        req.Operation,
		WorkflowID:       req.WorkflowID,
		Note:             req.Note,
		Limit:            req.Limit,
		IncludeCompleted: req.IncludeCompleted,
	}
}

func (e *TemporalExecutor) create(ctx context.Context, req Request) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, ErrWorkflowIDRequired
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return Result{}, err
	}
	exists, err := e.workflowExists(ctx, req.ProjectID, workflowID, workflowPath)
	if err != nil {
		return Result{}, err
	}
	if exists {
		return Result{}, ErrWorkflowExists
	}
	req.WorkflowID = workflowID

	req.SourceEvent = defaultWorkflowSourceEvent(req.SourceEvent)
	manifest, commitHash, err := e.commitAuthoredBundle(ctx, req, workflowPath)
	if err != nil {
		return Result{}, err
	}
	scheduleID := workflowrun.ScheduleID(req.ProjectID, workflowID)
	options, err := e.scheduleOptions(req, workflowID, scheduleID, commitHash, manifest)
	if err != nil {
		return Result{}, err
	}
	handle, err := e.Client.Create(ctx, options)
	if err != nil {
		return Result{}, err
	}
	scheduleID = handle.GetID()
	if err := e.Store.UpsertScheduledWorkflow(ctx, e.workflowRecord(req, workflowID, manifest, workflowPath, commitHash, scheduleID, domain.ScheduledWorkflowStatusActive)); err != nil {
		_ = e.Client.GetHandle(ctx, scheduleID).Delete(ctx)
		return Result{}, err
	}
	e.log("created", workflowID, scheduleID)
	return resultFromManifest(OperationCreate, workflowID, scheduleID, workflowPath, commitHash, manifest, "workflow schedule created"), nil
}

func (e *TemporalExecutor) update(ctx context.Context, req Request) (Result, error) {
	return e.publishCurrentSource(ctx, req)
}

func (e *TemporalExecutor) publishCurrentSource(ctx context.Context, req Request) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, ErrWorkflowIDRequired
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return Result{}, err
	}
	existing, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, workflowNotFoundError{workflowID: workflowID}
	}
	scheduleID := firstNonEmpty(strings.TrimSpace(existing.TemporalScheduleID), workflowrun.ScheduleID(req.ProjectID, workflowID))
	status := existing.Status
	if status == "" {
		status = domain.ScheduledWorkflowStatusActive
	}
	req.Operation = OperationUpdate
	req.WorkflowID = workflowID
	req.Paused = status == domain.ScheduledWorkflowStatusPaused
	req.SourceEvent = preservedWorkflowSourceEvent(existing.SourceEvent, req.SourceEvent)
	manifest, commitHash, err := e.commitAuthoredBundle(ctx, req, workflowPath)
	if err != nil {
		return Result{}, err
	}
	if strings.EqualFold(commitHash, strings.TrimSpace(existing.CurrentCommitHash)) {
		return resultFromManifest(OperationUpdate, workflowID, scheduleID, workflowPath, commitHash, manifest, "workflow schedule already current"), nil
	}
	options, err := e.scheduleOptions(req, workflowID, scheduleID, commitHash, manifest)
	if err != nil {
		return Result{}, err
	}
	handle := e.Client.GetHandle(ctx, scheduleID)
	var previousSchedule temporalclient.Schedule
	previousScheduleCaptured := false
	err = handle.Update(ctx, temporalclient.ScheduleUpdateOptions{
		DoUpdate: func(input temporalclient.ScheduleUpdateInput) (*temporalclient.ScheduleUpdate, error) {
			previousSchedule = input.Description.Schedule
			previousScheduleCaptured = true
			return scheduleUpdateFromOptions(input, options), nil
		},
	})
	if err != nil {
		return Result{}, err
	}
	if err := e.Store.UpsertScheduledWorkflow(ctx, e.workflowRecord(req, workflowID, manifest, workflowPath, commitHash, scheduleID, status)); err != nil {
		if previousScheduleCaptured {
			if rollbackErr := rollbackScheduleUpdate(ctx, handle, previousSchedule); rollbackErr != nil {
				return Result{}, fmt.Errorf("persist scheduled workflow after Temporal update: %w; rollback Temporal schedule: %v", err, rollbackErr)
			}
		}
		return Result{}, err
	}
	e.log("updated", workflowID, scheduleID)
	return resultFromManifest(OperationUpdate, workflowID, scheduleID, workflowPath, commitHash, manifest, "workflow schedule updated"), nil
}

func (e *TemporalExecutor) list(ctx context.Context, req Request) (Result, error) {
	limit := req.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	items, err := e.Store.ListScheduledWorkflows(ctx, storage.ScheduledWorkflowQuery{
		ProjectID:      strings.TrimSpace(req.ProjectID),
		IncludeDeleted: req.IncludeCompleted,
		Limit:          limit,
	})
	if err != nil {
		return Result{}, err
	}
	entries := make([]WorkflowEntry, 0, len(items))
	for _, item := range items {
		entries = append(entries, WorkflowEntry{
			ID:                 item.ID,
			Name:               item.Name,
			Description:        item.Description,
			Status:             string(item.Status),
			CommitHash:         item.CurrentCommitHash,
			TemporalScheduleID: item.TemporalScheduleID,
		})
	}
	return Result{Operation: OperationList, Message: fmt.Sprintf("found %d workflow(s)", len(entries)), Workflows: entries}, nil
}

func (e *TemporalExecutor) describe(ctx context.Context, req Request) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	item, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID)
	if err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, workflowNotFoundError{workflowID: workflowID}
	}
	result := Result{
		Operation:    OperationDescribe,
		WorkflowID:   item.ID,
		ScheduleID:   item.TemporalScheduleID,
		Name:         item.Name,
		Description:  item.Description,
		Message:      "workflow schedule described",
		CommitHash:   item.CurrentCommitHash,
		WorkflowPath: item.WorkflowPath,
		Paused:       item.Status == domain.ScheduledWorkflowStatusPaused,
	}
	return result, nil
}

func (e *TemporalExecutor) delete(ctx context.Context, req Request) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	scheduleID := workflowrun.ScheduleID(req.ProjectID, workflowID)
	if item, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID); err != nil {
		return Result{}, err
	} else if ok && strings.TrimSpace(item.TemporalScheduleID) != "" {
		scheduleID = strings.TrimSpace(item.TemporalScheduleID)
	}
	if err := e.Client.GetHandle(ctx, scheduleID).Delete(ctx); err != nil && !isNotFound(err) {
		return Result{}, err
	}
	if err := e.Store.DeleteScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID); err != nil {
		return Result{}, err
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(workflowPath); err != nil {
		return Result{}, err
	}
	runsPath, err := workflowbundle.WorkflowRunsDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(runsPath); err != nil {
		return Result{}, err
	}
	logsPath, err := e.workflowLogsDir(req, workflowID)
	if err != nil {
		return Result{}, err
	}
	if err := os.RemoveAll(logsPath); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationDelete, WorkflowID: workflowID, ScheduleID: scheduleID, Message: "workflow schedule deleted"}, nil
}

func (e *TemporalExecutor) workflowLogsDir(req Request, workflowID string) (string, error) {
	stateDir := strings.TrimSpace(req.StateDir)
	if stateDir == "" {
		stateDir = strings.TrimSpace(e.StateDir)
	}
	resolvedStateDir, err := workspace.ResolveStateDir(stateDir, e.WorkspaceRoot)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedStateDir, "workflow-logs", strings.TrimSpace(workflowID)), nil
}

func (e *TemporalExecutor) pause(ctx context.Context, req Request) (Result, error) {
	return e.pauseState(ctx, req, true)
}

func (e *TemporalExecutor) resume(ctx context.Context, req Request) (Result, error) {
	return e.pauseState(ctx, req, false)
}

func (e *TemporalExecutor) pauseState(ctx context.Context, req Request, paused bool) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	scheduleID := workflowrun.ScheduleID(req.ProjectID, workflowID)
	if paused {
		err = e.Client.GetHandle(ctx, scheduleID).Pause(ctx, temporalclient.SchedulePauseOptions{Note: strings.TrimSpace(req.Note)})
	} else {
		err = e.Client.GetHandle(ctx, scheduleID).Unpause(ctx, temporalclient.ScheduleUnpauseOptions{Note: strings.TrimSpace(req.Note)})
	}
	if err != nil {
		return Result{}, err
	}
	item, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID)
	if err != nil {
		return Result{}, err
	}
	if ok {
		item.Status = statusFromPaused(paused)
		item.UpdatedAt = time.Now().UTC()
		if err := e.Store.UpsertScheduledWorkflow(ctx, item); err != nil {
			return Result{}, err
		}
	}
	op := OperationResume
	msg := "workflow schedule resumed"
	if paused {
		op = OperationPause
		msg = "workflow schedule paused"
	}
	return Result{Operation: op, WorkflowID: workflowID, ScheduleID: scheduleID, Message: msg, Paused: paused}, nil
}

func (e *TemporalExecutor) trigger(ctx context.Context, req Request) (Result, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return Result{}, err
	}
	published, err := e.publishCurrentSource(ctx, Request{
		ProjectID:     req.ProjectID,
		WorkItemID:    req.WorkItemID,
		ToolCallID:    req.ToolCallID,
		Intent:        req.Intent,
		SourceEvent:   req.SourceEvent,
		Operation:     OperationUpdate,
		WorkflowID:    workflowID,
		CommitMessage: req.CommitMessage,
	})
	if err != nil {
		return Result{}, err
	}
	scheduleID := strings.TrimSpace(published.ScheduleID)
	if scheduleID == "" {
		scheduleID = workflowrun.ScheduleID(req.ProjectID, workflowID)
	}
	if err := e.Client.GetHandle(ctx, scheduleID).Trigger(ctx, temporalclient.ScheduleTriggerOptions{}); err != nil {
		return Result{}, err
	}
	published.Operation = OperationTrigger
	published.Message = "workflow schedule triggered"
	return published, nil
}

func scheduleUpdateFromOptions(input temporalclient.ScheduleUpdateInput, options temporalclient.ScheduleOptions) *temporalclient.ScheduleUpdate {
	remaining := options.RemainingActions
	state := &temporalclient.ScheduleState{
		Note:             options.Note,
		Paused:           options.Paused,
		LimitedActions:   remaining > 0,
		RemainingActions: remaining,
	}
	if input.Description.Schedule.State != nil {
		existingState := *input.Description.Schedule.State
		state = &existingState
		state.Note = options.Note
		state.LimitedActions = remaining > 0
		state.RemainingActions = remaining
	}
	return &temporalclient.ScheduleUpdate{
		Schedule: &temporalclient.Schedule{
			Action: options.Action,
			Spec:   &options.Spec,
			Policy: &temporalclient.SchedulePolicies{
				Overlap:        options.Overlap,
				CatchupWindow:  options.CatchupWindow,
				PauseOnFailure: options.PauseOnFailure,
			},
			State: state,
		},
	}
}

func rollbackScheduleUpdate(ctx context.Context, handle temporalclient.ScheduleHandle, previous temporalclient.Schedule) error {
	return handle.Update(ctx, temporalclient.ScheduleUpdateOptions{
		DoUpdate: func(temporalclient.ScheduleUpdateInput) (*temporalclient.ScheduleUpdate, error) {
			return &temporalclient.ScheduleUpdate{Schedule: &previous}, nil
		},
	})
}

func (e *TemporalExecutor) commitAuthoredBundle(ctx context.Context, req Request, workflowPath string) (workflowbundle.Manifest, string, error) {
	if _, _, err := e.resolveTimeZone(); err != nil {
		return workflowbundle.Manifest{}, "", err
	}
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return workflowbundle.Manifest{}, "", ErrWorkflowIDRequired
	}
	manifest, err := workflowbundle.LoadManifest(workflowPath)
	if err != nil {
		return workflowbundle.Manifest{}, "", err
	}
	manifest, err = ensureManifestNotificationTarget(workflowPath, manifest, notificationTargetFromEvent(req.SourceEvent))
	if err != nil {
		return workflowbundle.Manifest{}, "", err
	}
	commitMessage := defaultCommitMessage(ctx, workflowPath, req, workflowID)
	commitHash, err := workflowbundle.CommitBundle(ctx, workflowPath, commitMessage, nil)
	if err != nil {
		return workflowbundle.Manifest{}, "", err
	}
	return manifest, commitHash, nil
}

func (e *TemporalExecutor) PrepareAuthoring(ctx context.Context, req AuthoringRequest) (AuthoringPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := e.validate(); err != nil {
		return AuthoringPlan{}, err
	}
	request := Request{
		ProjectID:     req.ProjectID,
		WorkItemID:    req.WorkItemID,
		ToolCallID:    req.ToolCallID,
		Intent:        req.Intent,
		SourceEvent:   req.SourceEvent,
		Operation:     normalizeOperation(req.Operation),
		WorkflowID:    req.WorkflowID,
		Prompt:        req.Prompt,
		CommitMessage: req.CommitMessage,
	}
	switch request.Operation {
	case OperationCreate:
		return e.prepareCreateAuthoring(ctx, request)
	case OperationUpdate:
		return e.prepareUpdateAuthoring(ctx, request)
	default:
		return AuthoringPlan{}, unsupportedAuthoringOperationError{operation: req.Operation}
	}
}

func (e *TemporalExecutor) prepareCreateAuthoring(ctx context.Context, req Request) (AuthoringPlan, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return AuthoringPlan{}, ErrWorkflowIDRequired
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return AuthoringPlan{}, err
	}
	if exists, err := e.workflowExists(ctx, req.ProjectID, workflowID, workflowPath); err != nil {
		return AuthoringPlan{}, err
	} else if exists {
		return AuthoringPlan{}, ErrWorkflowExists
	}
	if hadLocalBundle, err := pathExists(workflowPath); err != nil {
		return AuthoringPlan{}, err
	} else if hadLocalBundle {
		if err := os.RemoveAll(workflowPath); err != nil {
			return AuthoringPlan{}, err
		}
	}
	if err := workflowbundle.InitializeBundle(ctx, workflowPath); err != nil {
		return AuthoringPlan{}, err
	}
	target := notificationTargetFromEvent(req.SourceEvent)
	return e.authoringPlan(OperationCreate, workflowID, workflowPath, req, true, "", target), nil
}

func (e *TemporalExecutor) prepareUpdateAuthoring(ctx context.Context, req Request) (AuthoringPlan, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return AuthoringPlan{}, ErrWorkflowIDRequired
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return AuthoringPlan{}, err
	}
	existing, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID)
	if err != nil {
		return AuthoringPlan{}, err
	}
	if !ok {
		return AuthoringPlan{}, workflowNotFoundError{workflowID: workflowID}
	}
	if exists, err := pathExists(workflowPath); err != nil {
		return AuthoringPlan{}, err
	} else if !exists {
		return AuthoringPlan{}, workflowSourceNotFoundError{workflowID: workflowID}
	}
	target := notificationTargetFromEvent(preservedWorkflowSourceEvent(existing.SourceEvent, req.SourceEvent))
	if manifest, err := workflowbundle.LoadManifest(workflowPath); err == nil {
		if manifestTarget, ok := notificationTargetFromPolicy(manifest.NotificationPolicy); ok {
			target = manifestTarget
		}
	}
	head, err := checkpointWorkflowAuthoringBase(ctx, workflowID, workflowPath)
	if err != nil {
		return AuthoringPlan{}, err
	}
	return e.authoringPlan(OperationUpdate, workflowID, workflowPath, req, false, head, target), nil
}

func (e *TemporalExecutor) authoringPlan(operation, workflowID, workflowPath string, req Request, removeOnFailure bool, restoreCommitHash string, target notificationTarget) AuthoringPlan {
	return AuthoringPlan{
		Operation:         operation,
		WorkflowID:        workflowID,
		WorkflowPath:      workflowPath,
		AgentGoal:         authoringAgentGoal(operation, workflowID),
		AgentPrompt:       authoringAgentPrompt(operation, workflowID, workflowPath, req.Prompt, req.CommitMessage, target),
		CommitMessage:     req.CommitMessage,
		RemoveOnFailure:   removeOnFailure,
		RestoreCommitHash: restoreCommitHash,
	}
}

func checkpointWorkflowAuthoringBase(ctx context.Context, workflowID, workflowPath string) (string, error) {
	head, err := gitOutput(ctx, workflowPath, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	status, err := gitOutput(ctx, workflowPath, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(status) == "" {
		return strings.TrimSpace(head), nil
	}
	return workflowbundle.CommitBundle(ctx, workflowPath, "Checkpoint local edits for workflow "+strings.TrimSpace(workflowID), nil)
}

func (e *TemporalExecutor) CleanupAuthoring(ctx context.Context, plan AuthoringPlan) error {
	// TODO: Temporarily hide workflow authoring cleanup so failed agent runs leave
	// the authored bundle on disk for debugging. Re-enable this once the
	// workflow-authoring prompt and validation loop are stable.
	/*
		if ctx == nil {
			ctx = context.Background()
		}
		if strings.TrimSpace(plan.WorkflowPath) == "" {
			return nil
		}
		if plan.RemoveOnFailure {
			return os.RemoveAll(plan.WorkflowPath)
		}
		if strings.TrimSpace(plan.RestoreCommitHash) == "" {
			return nil
		}
		if _, err := gitOutput(ctx, plan.WorkflowPath, "reset", "--hard", strings.TrimSpace(plan.RestoreCommitHash)); err != nil {
			return err
		}
		_, err := gitOutput(ctx, plan.WorkflowPath, "clean", "-fd")
		return err
	*/
	return nil
}

func defaultCommitMessage(ctx context.Context, workflowPath string, req Request, workflowID string) string {
	if commitMessage := strings.TrimSpace(req.CommitMessage); commitMessage != "" {
		return commitMessage
	}
	if normalizeOperation(req.Operation) == OperationUpdate && strings.TrimSpace(req.Prompt) == "" && workflowHasUncommittedChanges(ctx, workflowPath) {
		return "Apply manual edits to workflow " + strings.TrimSpace(workflowID)
	}
	switch normalizeOperation(req.Operation) {
	case OperationCreate:
		return "Create workflow " + strings.TrimSpace(workflowID)
	case OperationUpdate:
		return "Update workflow " + strings.TrimSpace(workflowID)
	default:
		return "Update workflow " + strings.TrimSpace(workflowID)
	}
}

func workflowHasUncommittedChanges(ctx context.Context, workflowPath string) bool {
	status, err := gitOutput(ctx, workflowPath, "status", "--porcelain")
	return err == nil && strings.TrimSpace(status) != ""
}

func authoringAgentGoal(operation, workflowID string) string {
	switch normalizeOperation(operation) {
	case OperationCreate:
		return "Create scheduled workflow " + strings.TrimSpace(workflowID)
	case OperationUpdate:
		return "Update scheduled workflow " + strings.TrimSpace(workflowID)
	default:
		return "Author scheduled workflow " + strings.TrimSpace(workflowID)
	}
}

func authoringAgentPrompt(operation, workflowID, workflowPath, userPrompt, commitMessage string, target notificationTarget) string {
	return PromptAuthoringAgentWithNotificationTarget(normalizeOperation(operation), workflowID, workflowPath, userPrompt, commitMessage, target.ChannelType, target.ChannelID)
}

type notificationTarget struct {
	ChannelType string
	ChannelID   string
}

func ensureManifestNotificationTarget(workflowPath string, manifest workflowbundle.Manifest, fallback notificationTarget) (workflowbundle.Manifest, error) {
	if target, ok := notificationTargetFromPolicy(manifest.NotificationPolicy); ok {
		manifest.NotificationPolicy.ChannelType = target.ChannelType
		manifest.NotificationPolicy.ChannelID = target.ChannelID
		return manifest, nil
	}
	if strings.TrimSpace(manifest.NotificationPolicy.ChannelType) != "" || strings.TrimSpace(manifest.NotificationPolicy.ChannelID) != "" {
		return workflowbundle.Manifest{}, fmt.Errorf("notification_policy.channel_type and notification_policy.channel_id must be set together")
	}
	manifest.NotificationPolicy.ChannelType = fallback.ChannelType
	manifest.NotificationPolicy.ChannelID = fallback.ChannelID
	if err := workflowbundle.WriteManifest(workflowPath, manifest); err != nil {
		return workflowbundle.Manifest{}, err
	}
	return manifest, nil
}

func notificationTargetEvent(source domain.Event, policy workflowbundle.NotificationPolicy) domain.Event {
	event := defaultWorkflowSourceEvent(source)
	if target, ok := notificationTargetFromPolicy(policy); ok {
		event.ChannelType = domain.ChannelType(target.ChannelType)
		event.ChannelID = target.ChannelID
	}
	event.ThreadID = ""
	return event
}

func notificationTargetFromPolicy(policy workflowbundle.NotificationPolicy) (notificationTarget, bool) {
	channelType := strings.TrimSpace(policy.ChannelType)
	channelID := strings.TrimSpace(policy.ChannelID)
	if channelType == "" || channelID == "" {
		return notificationTarget{}, false
	}
	normalized, err := workflowbundle.NormalizeNotificationChannelType(channelType)
	if err != nil {
		return notificationTarget{}, false
	}
	return notificationTarget{ChannelType: normalized, ChannelID: channelID}, true
}

func notificationTargetFromEvent(event domain.Event) notificationTarget {
	event = defaultWorkflowSourceEvent(event)
	return notificationTarget{
		ChannelType: string(event.ChannelType),
		ChannelID:   strings.TrimSpace(event.ChannelID),
	}
}

func preservedWorkflowSourceEvent(existing, fallback domain.Event) domain.Event {
	if strings.TrimSpace(existing.ID) != "" || strings.TrimSpace(existing.ChannelID) != "" || strings.TrimSpace(string(existing.ChannelType)) != "" {
		return defaultWorkflowSourceEvent(existing)
	}
	return defaultWorkflowSourceEvent(fallback)
}

func defaultWorkflowSourceEvent(event domain.Event) domain.Event {
	if strings.TrimSpace(string(event.ChannelType)) == "" {
		event.ChannelType = domain.ChannelTypeCLI
	}
	if strings.TrimSpace(event.ChannelID) == "" {
		event.ChannelID = "default"
	}
	return event
}

func (e *TemporalExecutor) workflowExists(ctx context.Context, projectID, workflowID, _ string) (bool, error) {
	if _, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(projectID), workflowID); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	return false, nil
}

func pathExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}

func isNotFound(err error) bool {
	var notFound *serviceerror.NotFound
	return errors.As(err, &notFound)
}

func (e *TemporalExecutor) scheduleOptions(req Request, workflowID, scheduleID, commitHash string, manifest workflowbundle.Manifest) (temporalclient.ScheduleOptions, error) {
	location, timeZoneName, err := e.resolveTimeZone()
	if err != nil {
		return temporalclient.ScheduleOptions{}, err
	}
	spec, remainingActions, err := e.scheduleSpec(manifest.Schedule, location, timeZoneName)
	if err != nil {
		return temporalclient.ScheduleOptions{}, err
	}
	catchupWindow, err := workflowbundle.ParseCatchupWindow(manifest.Schedule.CatchupWindow)
	if err != nil {
		return temporalclient.ScheduleOptions{}, err
	}
	overlap, err := temporalOverlapPolicy(manifest.Schedule.OverlapPolicy)
	if err != nil {
		return temporalclient.ScheduleOptions{}, err
	}
	input := workflowrun.Input{
		ProjectID:        strings.TrimSpace(req.ProjectID),
		WorkflowID:       workflowID,
		WorkflowName:     manifest.Name,
		CommitHash:       commitHash,
		ScheduleID:       scheduleID,
		SourceEvent:      notificationTargetEvent(req.SourceEvent, manifest.NotificationPolicy),
		CreatedByEventID: strings.TrimSpace(req.SourceEvent.ID),
	}
	return temporalclient.ScheduleOptions{
		ID:   scheduleID,
		Spec: spec,
		Action: &temporalclient.ScheduleWorkflowAction{
			ID:            scheduleID + ":run",
			Workflow:      workflowrun.WorkflowName,
			Args:          []interface{}{input},
			TaskQueue:     strings.TrimSpace(e.TaskQueue),
			StaticSummary: manifest.Name,
			StaticDetails: manifest.Description,
		},
		Overlap:          overlap,
		CatchupWindow:    catchupWindow,
		PauseOnFailure:   manifest.Schedule.PauseOnFailure,
		Note:             firstNonEmpty(req.Note, manifest.Description, manifest.Name),
		Paused:           req.Paused,
		RemainingActions: remainingActions,
		Memo: map[string]interface{}{
			"opencto_project_id":    strings.TrimSpace(req.ProjectID),
			"opencto_workflow_id":   workflowID,
			"opencto_workflow_name": manifest.Name,
			"opencto_commit_hash":   commitHash,
			"opencto_schedule_id":   scheduleID,
		},
	}, nil
}

func (e *TemporalExecutor) scheduleSpec(schedule workflowbundle.Schedule, location *time.Location, timeZoneName string) (temporalclient.ScheduleSpec, int, error) {
	cron := strings.TrimSpace(schedule.Cron)
	oneShotAt := strings.TrimSpace(schedule.OneShotAt)
	switch {
	case cron == "" && oneShotAt == "":
		return temporalclient.ScheduleSpec{}, 0, ErrScheduleSpecMissing
	case cron != "" && oneShotAt != "":
		return temporalclient.ScheduleSpec{}, 0, ErrScheduleSpecMixed
	case oneShotAt != "":
		parsed, err := time.Parse(time.RFC3339, oneShotAt)
		if err != nil {
			return temporalclient.ScheduleSpec{}, 0, fmt.Errorf("parse schedule.one_shot_at as RFC3339: %w", err)
		}
		if !parsed.After(e.now()) {
			return temporalclient.ScheduleSpec{}, 0, ErrPastOneShot
		}
		local := parsed.In(location)
		return temporalclient.ScheduleSpec{
			Calendars: []temporalclient.ScheduleCalendarSpec{{
				Second:     []temporalclient.ScheduleRange{{Start: local.Second()}},
				Minute:     []temporalclient.ScheduleRange{{Start: local.Minute()}},
				Hour:       []temporalclient.ScheduleRange{{Start: local.Hour()}},
				DayOfMonth: []temporalclient.ScheduleRange{{Start: local.Day()}},
				Month:      []temporalclient.ScheduleRange{{Start: int(local.Month())}},
				Year:       []temporalclient.ScheduleRange{{Start: local.Year()}},
			}},
			StartAt:      parsed.Add(-time.Second),
			EndAt:        parsed.Add(time.Second),
			TimeZoneName: timeZoneName,
		}, 1, nil
	default:
		if strings.ContainsAny(cron, "\r\n") {
			return temporalclient.ScheduleSpec{}, 0, fmt.Errorf("schedule.cron must be a single-line Temporal cron expression")
		}
		return temporalclient.ScheduleSpec{CronExpressions: []string{cron}, TimeZoneName: timeZoneName}, 0, nil
	}
}

func (e *TemporalExecutor) workflowRecord(req Request, workflowID string, manifest workflowbundle.Manifest, workflowPath, commitHash, scheduleID string, status domain.ScheduledWorkflowStatus) domain.ScheduledWorkflow {
	now := time.Now().UTC()
	return domain.ScheduledWorkflow{
		ID:                 workflowID,
		ProjectID:          strings.TrimSpace(req.ProjectID),
		Name:               manifest.Name,
		Description:        manifest.Description,
		Status:             status,
		CurrentCommitHash:  commitHash,
		TemporalScheduleID: scheduleID,
		WorkflowPath:       workflowPath,
		CreatedByEventID:   strings.TrimSpace(req.SourceEvent.ID),
		SourceEvent:        req.SourceEvent,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}

func (e *TemporalExecutor) resolveTimeZone() (*time.Location, string, error) {
	resolver := e.ResolveTimeZone
	if resolver == nil {
		resolver = ResolveHostTimeZone
	}
	location, name, err := resolver()
	if err != nil {
		return nil, "", err
	}
	if location == nil || strings.TrimSpace(name) == "" {
		return nil, "", fmt.Errorf("host IANA timezone could not be resolved")
	}
	return location, strings.TrimSpace(name), nil
}

func (e *TemporalExecutor) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *TemporalExecutor) log(action, workflowID, scheduleID string) {
	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info(
		"temporal workflow schedule "+action,
		slog.String("workflow_id", workflowID),
		slog.String("schedule_id", scheduleID),
	)
}

func temporalOverlapPolicy(value string) (enumspb.ScheduleOverlapPolicy, error) {
	normalized, err := workflowbundle.NormalizeOverlapPolicy(value)
	if err != nil {
		return enumspb.SCHEDULE_OVERLAP_POLICY_UNSPECIFIED, err
	}
	switch normalized {
	case workflowbundle.OverlapPolicySkip:
		return enumspb.SCHEDULE_OVERLAP_POLICY_SKIP, nil
	case workflowbundle.OverlapPolicyBufferOne:
		return enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ONE, nil
	case workflowbundle.OverlapPolicyBufferAll:
		return enumspb.SCHEDULE_OVERLAP_POLICY_BUFFER_ALL, nil
	case workflowbundle.OverlapPolicyCancelOther:
		return enumspb.SCHEDULE_OVERLAP_POLICY_CANCEL_OTHER, nil
	case workflowbundle.OverlapPolicyTerminateOther:
		return enumspb.SCHEDULE_OVERLAP_POLICY_TERMINATE_OTHER, nil
	case workflowbundle.OverlapPolicyAllowAll:
		return enumspb.SCHEDULE_OVERLAP_POLICY_ALLOW_ALL, nil
	default:
		return enumspb.SCHEDULE_OVERLAP_POLICY_UNSPECIFIED, fmt.Errorf("unsupported overlap_policy %q", value)
	}
}

func statusFromPaused(paused bool) domain.ScheduledWorkflowStatus {
	if paused {
		return domain.ScheduledWorkflowStatusPaused
	}
	return domain.ScheduledWorkflowStatusActive
}

func resultFromManifest(operation, workflowID, scheduleID, workflowPath, commitHash string, manifest workflowbundle.Manifest, message string) Result {
	return Result{
		Operation:    operation,
		WorkflowID:   workflowID,
		ScheduleID:   scheduleID,
		Name:         manifest.Name,
		Description:  manifest.Description,
		Message:      message,
		CommitHash:   commitHash,
		WorkflowPath: workflowPath,
		Cron:         manifest.Schedule.Cron,
		OneShotAt:    manifest.Schedule.OneShotAt,
	}
}

func (r Result) Observation() string {
	var lines []string
	lines = append(lines, firstNonEmpty(r.Message, "workflow schedule operation completed"))
	if r.WorkflowID != "" {
		lines = append(lines, "workflow_id: "+r.WorkflowID)
	}
	if r.Name != "" {
		lines = append(lines, "name: "+r.Name)
	}
	if r.CommitHash != "" {
		lines = append(lines, "commit_hash: "+r.CommitHash)
	}
	if r.Cron != "" {
		lines = append(lines, "cron: "+r.Cron)
	}
	if r.OneShotAt != "" {
		lines = append(lines, "one_shot_at: "+r.OneShotAt)
	}
	if len(r.Workflows) > 0 {
		lines = append(lines, "workflows:")
		for index, entry := range r.Workflows {
			lines = append(lines, fmt.Sprintf("%d. %s (%s)", index+1, firstNonEmpty(entry.Name, entry.ID), entry.Status))
		}
	}
	return strings.Join(lines, "\n")
}

func normalizeOperation(operation string) string {
	return strings.ToLower(strings.TrimSpace(operation))
}

func cleanStrings(values []string) []string {
	var out []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return "", gitCommandError(cmd, err)
	}
	return string(output), nil
}

func gitCommandError(cmd *exec.Cmd, err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("%s: %w: %s", strings.Join(cmd.Args, " "), err, strings.TrimSpace(string(exitErr.Stderr)))
	}
	return fmt.Errorf("%s: %w", strings.Join(cmd.Args, " "), err)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
