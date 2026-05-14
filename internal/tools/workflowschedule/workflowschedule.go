package workflowschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/storage"
	"github.com/opencto/opencto/internal/workflowbundle"
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
	ErrClientRequired      = errors.New("schedule client is required")
	ErrStoreRequired       = errors.New("runtime store is required")
	ErrWorkspaceRequired   = errors.New("workspace root is required")
	ErrTaskQueueRequired   = errors.New("task queue is required")
	ErrOperationRequired   = errors.New("workflow schedule operation is required")
	ErrWorkflowIDRequired  = errors.New("workflow_id is required")
	ErrWorkflowExists      = errors.New("workflow already exists")
	ErrScheduleSpecMissing = errors.New("schedule.cron or schedule.one_shot_at is required")
	ErrScheduleSpecMixed   = errors.New("schedule.cron and schedule.one_shot_at cannot both be set")
	ErrPastOneShot         = errors.New("schedule.one_shot_at must be in the future")
)

type CreateRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	WorkflowID         string                            `json:"workflow_id"`
	Name               string                            `json:"name"`
	Description        string                            `json:"description"`
	Schedule           workflowbundle.Schedule           `json:"schedule"`
	NotificationPolicy workflowbundle.NotificationPolicy `json:"notification_policy"`
	Env                []string                          `json:"env"`
	Steps              []workflowbundle.Step             `json:"steps"`
	Files              []workflowbundle.File             `json:"files"`
	CommitMessage      string                            `json:"commit_message,omitempty"`
	Paused             bool                              `json:"paused,omitempty"`
	Note               string                            `json:"note,omitempty"`
}

type UpdateRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	WorkflowID string `json:"workflow_id"`

	Name               *string                  `json:"name,omitempty"`
	Description        *string                  `json:"description,omitempty"`
	Schedule           *SchedulePatch           `json:"schedule,omitempty"`
	NotificationPolicy *NotificationPolicyPatch `json:"notification_policy,omitempty"`
	Env                []string                 `json:"env,omitempty"`
	Steps              []workflowbundle.Step    `json:"steps,omitempty"`
	Files              []workflowbundle.File    `json:"files,omitempty"`
	CommitMessage      string                   `json:"commit_message,omitempty"`
	Paused             *bool                    `json:"paused,omitempty"`
	Note               string                   `json:"note,omitempty"`
}

type DeleteRequest struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

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

type SchedulePatch struct {
	Cron           *string `json:"cron,omitempty"`
	OneShotAt      *string `json:"one_shot_at,omitempty"`
	OverlapPolicy  *string `json:"overlap_policy,omitempty"`
	CatchupWindow  *string `json:"catchup_window,omitempty"`
	PauseOnFailure *bool   `json:"pause_on_failure,omitempty"`
}

type NotificationPolicyPatch struct {
	OnFailure *bool `json:"on_failure,omitempty"`
}

type Request struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	Operation          string                            `json:"operation"`
	WorkflowID         string                            `json:"workflow_id"`
	Name               string                            `json:"name"`
	Description        string                            `json:"description"`
	Schedule           workflowbundle.Schedule           `json:"schedule"`
	NotificationPolicy workflowbundle.NotificationPolicy `json:"notification_policy"`
	Env                []string                          `json:"env"`
	Steps              []workflowbundle.Step             `json:"steps"`
	Files              []workflowbundle.File             `json:"files"`
	CommitMessage      string                            `json:"commit_message"`
	CommitHash         string                            `json:"commit_hash"`
	Paused             bool                              `json:"paused"`
	Note               string                            `json:"note"`
	Limit              int                               `json:"limit"`
	IncludeCompleted   bool                              `json:"include_completed"`

	nameSet               bool
	descriptionSet        bool
	schedulePatch         *SchedulePatch
	notificationPolicySet bool
	envSet                bool
	stepsSet              bool
	pausedSet             bool
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
		ProjectID:          req.ProjectID,
		WorkItemID:         req.WorkItemID,
		ToolCallID:         req.ToolCallID,
		Intent:             req.Intent,
		SourceEvent:        req.SourceEvent,
		Operation:          OperationCreate,
		WorkflowID:         req.WorkflowID,
		Name:               req.Name,
		Description:        req.Description,
		Schedule:           req.Schedule,
		NotificationPolicy: req.NotificationPolicy,
		Env:                req.Env,
		Steps:              req.Steps,
		Files:              req.Files,
		CommitMessage:      req.CommitMessage,
		Paused:             req.Paused,
		Note:               req.Note,
	}
}

func requestFromUpdate(req UpdateRequest) Request {
	request := Request{
		ProjectID:     req.ProjectID,
		WorkItemID:    req.WorkItemID,
		ToolCallID:    req.ToolCallID,
		Intent:        req.Intent,
		SourceEvent:   req.SourceEvent,
		Operation:     OperationUpdate,
		WorkflowID:    req.WorkflowID,
		Files:         req.Files,
		CommitMessage: req.CommitMessage,
		Note:          req.Note,
		schedulePatch: req.Schedule,
		envSet:        req.Env != nil,
		stepsSet:      req.Steps != nil,
	}
	if req.Name != nil {
		request.Name = *req.Name
		request.nameSet = true
	}
	if req.Description != nil {
		request.Description = *req.Description
		request.descriptionSet = true
	}
	if req.NotificationPolicy != nil && req.NotificationPolicy.OnFailure != nil {
		request.notificationPolicySet = true
		request.NotificationPolicy.OnFailure = *req.NotificationPolicy.OnFailure
	}
	if req.Env != nil {
		request.Env = req.Env
	}
	if req.Steps != nil {
		request.Steps = req.Steps
	}
	if req.Paused != nil {
		request.Paused = *req.Paused
		request.pausedSet = true
	}
	return request
}

func requestFromDelete(req DeleteRequest) Request {
	return Request{
		ProjectID:   req.ProjectID,
		WorkItemID:  req.WorkItemID,
		ToolCallID:  req.ToolCallID,
		Intent:      req.Intent,
		SourceEvent: req.SourceEvent,
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
	hadLocalBundle, err := pathExists(workflowPath)
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

	workflowID, manifest, workflowPath, commitHash, err := e.prepareBundle(ctx, req, false)
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
		e.cleanupCreateFailure(workflowPath, !hadLocalBundle)
		return Result{}, err
	}
	scheduleID = handle.GetID()
	if err := e.Store.UpsertScheduledWorkflow(ctx, e.workflowRecord(req, workflowID, manifest, workflowPath, commitHash, scheduleID, statusFromPaused(req.Paused))); err != nil {
		_ = e.Client.GetHandle(ctx, scheduleID).Delete(ctx)
		e.cleanupCreateFailure(workflowPath, !hadLocalBundle)
		return Result{}, err
	}
	e.log("created", workflowID, scheduleID)
	return resultFromManifest(OperationCreate, workflowID, scheduleID, workflowPath, commitHash, manifest, "workflow schedule created"), nil
}

func (e *TemporalExecutor) update(ctx context.Context, req Request) (Result, error) {
	workflowID, manifest, workflowPath, commitHash, err := e.prepareBundle(ctx, req, true)
	if err != nil {
		return Result{}, err
	}
	scheduleID := workflowrun.ScheduleID(req.ProjectID, workflowID)
	options, err := e.scheduleOptions(req, workflowID, scheduleID, commitHash, manifest)
	if err != nil {
		return Result{}, err
	}
	handle := e.Client.GetHandle(ctx, scheduleID)
	err = handle.Update(ctx, temporalclient.ScheduleUpdateOptions{
		DoUpdate: func(input temporalclient.ScheduleUpdateInput) (*temporalclient.ScheduleUpdate, error) {
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
				if req.pausedSet {
					state.Paused = options.Paused
				}
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
			}, nil
		},
	})
	if err != nil {
		return Result{}, err
	}
	status := statusFromPaused(req.Paused)
	if !req.pausedSet {
		if item, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID); err != nil {
			return Result{}, err
		} else if ok {
			status = item.Status
		}
	}
	if err := e.Store.UpsertScheduledWorkflow(ctx, e.workflowRecord(req, workflowID, manifest, workflowPath, commitHash, scheduleID, status)); err != nil {
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
		return Result{}, fmt.Errorf("workflow %q not found", workflowID)
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
	return Result{Operation: OperationDelete, WorkflowID: workflowID, ScheduleID: scheduleID, Message: "workflow schedule deleted"}, nil
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
	item, scheduleID, err := e.validateTriggerReady(ctx, strings.TrimSpace(req.ProjectID), workflowID)
	if err != nil {
		return Result{}, err
	}
	if err := e.Client.GetHandle(ctx, scheduleID).Trigger(ctx, temporalclient.ScheduleTriggerOptions{}); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationTrigger, WorkflowID: workflowID, ScheduleID: scheduleID, Name: item.Name, CommitHash: item.CurrentCommitHash, WorkflowPath: item.WorkflowPath, Message: "workflow schedule triggered"}, nil
}

func (e *TemporalExecutor) validateTriggerReady(ctx context.Context, projectID, workflowID string) (domain.ScheduledWorkflow, string, error) {
	item, ok, err := e.Store.GetScheduledWorkflow(ctx, projectID, workflowID)
	if err != nil {
		return domain.ScheduledWorkflow{}, "", err
	}
	if !ok {
		return domain.ScheduledWorkflow{}, "", fmt.Errorf("workflow %q not found", workflowID)
	}
	scheduleID := strings.TrimSpace(item.TemporalScheduleID)
	if scheduleID == "" {
		scheduleID = workflowrun.ScheduleID(projectID, workflowID)
	}
	workflowPath := strings.TrimSpace(item.WorkflowPath)
	if workflowPath == "" {
		workflowPath, err = workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
		if err != nil {
			return domain.ScheduledWorkflow{}, "", err
		}
	}
	status, err := gitOutput(ctx, workflowPath, "status", "--porcelain")
	if err != nil {
		return domain.ScheduledWorkflow{}, "", err
	}
	if strings.TrimSpace(status) != "" {
		return domain.ScheduledWorkflow{}, "", fmt.Errorf("workflow %q has uncommitted changes; run WorkflowUpdate before triggering", workflowID)
	}
	head, err := gitOutput(ctx, workflowPath, "rev-parse", "HEAD")
	if err != nil {
		return domain.ScheduledWorkflow{}, "", err
	}
	head = strings.TrimSpace(head)
	if !strings.EqualFold(head, strings.TrimSpace(item.CurrentCommitHash)) {
		return domain.ScheduledWorkflow{}, "", fmt.Errorf("workflow %q has unpublished commit %s; run WorkflowUpdate before triggering", workflowID, head)
	}
	return item, scheduleID, nil
}

func (e *TemporalExecutor) prepareBundle(ctx context.Context, req Request, useExisting bool) (string, workflowbundle.Manifest, string, string, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", ErrWorkflowIDRequired
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", err
	}
	manifest, err := e.manifest(req, workflowPath, useExisting)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", err
	}
	if err := workflowbundle.WriteBundle(ctx, workflowPath, manifest, req.Files); err != nil {
		return "", workflowbundle.Manifest{}, "", "", err
	}
	commitMessage := firstNonEmpty(strings.TrimSpace(req.CommitMessage), strings.TrimSpace(req.Name), "Update workflow "+workflowID)
	commitHash, err := workflowbundle.CommitBundle(ctx, workflowPath, commitMessage, req.Files)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", err
	}
	return workflowID, manifest, workflowPath, commitHash, nil
}

func (e *TemporalExecutor) manifest(req Request, workflowPath string, useExisting bool) (workflowbundle.Manifest, error) {
	if useExisting {
		existing, err := workflowbundle.LoadManifest(workflowPath)
		if err != nil {
			return workflowbundle.Manifest{}, err
		}
		return e.mergeManifest(existing, req)
	}
	return e.manifestFromRequest(req)
}

func (e *TemporalExecutor) manifestFromRequest(req Request) (workflowbundle.Manifest, error) {
	_, timeZoneName, err := e.resolveTimeZone()
	if err != nil {
		return workflowbundle.Manifest{}, err
	}
	schedule := req.Schedule
	schedule.TimeZoneName = timeZoneName
	if strings.TrimSpace(schedule.OverlapPolicy) == "" {
		schedule.OverlapPolicy = workflowbundle.OverlapPolicySkip
	}
	if strings.TrimSpace(schedule.CatchupWindow) == "" {
		schedule.CatchupWindow = workflowbundle.DefaultCatchupWindow.String()
	}
	notification := req.NotificationPolicy
	env := cleanStrings(req.Env)
	if env == nil {
		env = []string{}
	}
	manifest := workflowbundle.Manifest{
		Name:               firstNonEmpty(req.Name, req.WorkflowID),
		Description:        strings.TrimSpace(req.Description),
		Schedule:           schedule,
		NotificationPolicy: notification,
		Env:                env,
		Steps:              normalizeSteps(req.Steps),
	}
	if err := workflowbundle.ValidateManifest(manifest); err != nil {
		return workflowbundle.Manifest{}, err
	}
	return manifest, nil
}

func (e *TemporalExecutor) mergeManifest(existing workflowbundle.Manifest, req Request) (workflowbundle.Manifest, error) {
	manifest := existing
	if req.nameSet {
		manifest.Name = strings.TrimSpace(req.Name)
	}
	if req.descriptionSet {
		manifest.Description = strings.TrimSpace(req.Description)
	}
	if req.schedulePatch != nil {
		manifest.Schedule = mergeSchedulePatch(manifest.Schedule, req.schedulePatch)
	}
	if req.notificationPolicySet {
		manifest.NotificationPolicy.OnFailure = req.NotificationPolicy.OnFailure
	}
	if req.envSet {
		manifest.Env = cleanStrings(req.Env)
		if manifest.Env == nil {
			manifest.Env = []string{}
		}
	}
	if req.stepsSet {
		manifest.Steps = normalizeSteps(req.Steps)
	}
	if err := workflowbundle.ValidateManifest(manifest); err != nil {
		return workflowbundle.Manifest{}, err
	}
	return manifest, nil
}

func mergeSchedulePatch(existing workflowbundle.Schedule, update *SchedulePatch) workflowbundle.Schedule {
	schedule := existing
	if update == nil {
		return schedule
	}
	if update.Cron != nil {
		schedule.Cron = strings.TrimSpace(*update.Cron)
		if schedule.Cron != "" && update.OneShotAt == nil {
			schedule.OneShotAt = ""
		}
	}
	if update.OneShotAt != nil {
		schedule.OneShotAt = strings.TrimSpace(*update.OneShotAt)
		if schedule.OneShotAt != "" && update.Cron == nil {
			schedule.Cron = ""
		}
	}
	if update.OverlapPolicy != nil {
		schedule.OverlapPolicy = strings.TrimSpace(*update.OverlapPolicy)
	}
	if update.CatchupWindow != nil {
		schedule.CatchupWindow = strings.TrimSpace(*update.CatchupWindow)
	}
	if update.PauseOnFailure != nil {
		schedule.PauseOnFailure = *update.PauseOnFailure
	}
	return schedule
}

func normalizeSteps(steps []workflowbundle.Step) []workflowbundle.Step {
	next := append([]workflowbundle.Step(nil), steps...)
	for index := range next {
		if next[index].Args == nil {
			next[index].Args = []string{}
		}
		if next[index].RetryPolicy.NonRetryableErrorTypes == nil {
			next[index].RetryPolicy.NonRetryableErrorTypes = []string{}
		}
	}
	return next
}

func (e *TemporalExecutor) workflowExists(ctx context.Context, projectID, workflowID, _ string) (bool, error) {
	if _, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(projectID), workflowID); err != nil {
		return false, err
	} else if ok {
		return true, nil
	}
	return false, nil
}

func (e *TemporalExecutor) cleanupCreateFailure(workflowPath string, removeBundle bool) {
	if !removeBundle {
		return
	}
	if err := os.RemoveAll(workflowPath); err != nil {
		logger := e.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn("failed to remove workflow bundle after create failure", slog.String("workflow_path", workflowPath), slog.String("error", err.Error()))
	}
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
		SourceEvent:      req.SourceEvent,
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
		TimeZone:     manifest.Schedule.TimeZoneName,
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
