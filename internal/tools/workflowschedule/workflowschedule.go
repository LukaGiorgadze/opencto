package workflowschedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
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
	ErrScheduleSpecMissing = errors.New("schedule.cron or schedule.one_shot_at is required")
	ErrScheduleSpecMixed   = errors.New("schedule.cron and schedule.one_shot_at cannot both be set")
	ErrPastOneShot         = errors.New("schedule.one_shot_at must be in the future")
)

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
	Run(context.Context, Request) (Result, error)
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

func (e *TemporalExecutor) Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if e.Client == nil {
		return Result{}, ErrClientRequired
	}
	if e.Store == nil {
		return Result{}, ErrStoreRequired
	}
	if strings.TrimSpace(e.TaskQueue) == "" {
		return Result{}, ErrTaskQueueRequired
	}
	if strings.TrimSpace(e.WorkspaceRoot) == "" {
		return Result{}, ErrWorkspaceRequired
	}
	operation := normalizeOperation(req.Operation)
	switch operation {
	case OperationCreate:
		return e.create(ctx, req)
	case OperationUpdate:
		return e.update(ctx, req)
	case OperationList:
		return e.list(ctx, req)
	case OperationDescribe:
		return e.describe(ctx, req)
	case OperationDelete:
		return e.delete(ctx, req)
	case OperationPause:
		return e.pause(ctx, req)
	case OperationResume:
		return e.resume(ctx, req)
	case OperationTrigger:
		return e.trigger(ctx, req)
	case "":
		return Result{}, ErrOperationRequired
	default:
		return Result{}, fmt.Errorf("unsupported workflow schedule operation %q", req.Operation)
	}
}

func (e *TemporalExecutor) create(ctx context.Context, req Request) (Result, error) {
	workflowID, manifest, workflowPath, commitHash, err := e.prepareBundle(ctx, req)
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
		return Result{}, err
	}
	e.log("created", workflowID, scheduleID)
	return resultFromManifest(OperationCreate, workflowID, scheduleID, workflowPath, commitHash, manifest, "workflow schedule created"), nil
}

func (e *TemporalExecutor) update(ctx context.Context, req Request) (Result, error) {
	workflowID, manifest, workflowPath, commitHash, err := e.prepareBundle(ctx, req)
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
		DoUpdate: func(temporalclient.ScheduleUpdateInput) (*temporalclient.ScheduleUpdate, error) {
			remaining := options.RemainingActions
			return &temporalclient.ScheduleUpdate{
				Schedule: &temporalclient.Schedule{
					Action: options.Action,
					Spec:   &options.Spec,
					Policy: &temporalclient.SchedulePolicies{
						Overlap:        options.Overlap,
						CatchupWindow:  options.CatchupWindow,
						PauseOnFailure: options.PauseOnFailure,
					},
					State: &temporalclient.ScheduleState{
						Note:             options.Note,
						Paused:           options.Paused,
						LimitedActions:   remaining > 0,
						RemainingActions: remaining,
					},
				},
			}, nil
		},
	})
	if err != nil {
		return Result{}, err
	}
	if err := e.Store.UpsertScheduledWorkflow(ctx, e.workflowRecord(req, workflowID, manifest, workflowPath, commitHash, scheduleID, statusFromPaused(req.Paused))); err != nil {
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
	if err := e.Client.GetHandle(ctx, scheduleID).Delete(ctx); err != nil {
		return Result{}, err
	}
	if item, ok, err := e.Store.GetScheduledWorkflow(ctx, strings.TrimSpace(req.ProjectID), workflowID); err == nil && ok {
		item.Status = domain.ScheduledWorkflowStatusDeleted
		item.UpdatedAt = time.Now().UTC()
		_ = e.Store.UpsertScheduledWorkflow(ctx, item)
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
	if err == nil && ok {
		item.Status = statusFromPaused(paused)
		item.UpdatedAt = time.Now().UTC()
		_ = e.Store.UpsertScheduledWorkflow(ctx, item)
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
	scheduleID := workflowrun.ScheduleID(req.ProjectID, workflowID)
	if err := e.Client.GetHandle(ctx, scheduleID).Trigger(ctx, temporalclient.ScheduleTriggerOptions{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP}); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationTrigger, WorkflowID: workflowID, ScheduleID: scheduleID, Message: "workflow schedule triggered"}, nil
}

func (e *TemporalExecutor) prepareBundle(ctx context.Context, req Request) (string, workflowbundle.Manifest, string, string, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(req.WorkflowID)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", ErrWorkflowIDRequired
	}
	manifest, err := e.manifest(req)
	if err != nil {
		return "", workflowbundle.Manifest{}, "", "", err
	}
	workflowPath, err := workflowbundle.WorkflowDir(e.WorkspaceRoot, workflowID)
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

func (e *TemporalExecutor) manifest(req Request) (workflowbundle.Manifest, error) {
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
	steps := append([]workflowbundle.Step(nil), req.Steps...)
	for index := range steps {
		if steps[index].Args == nil {
			steps[index].Args = []string{}
		}
		if steps[index].RetryPolicy.NonRetryableErrorTypes == nil {
			steps[index].RetryPolicy.NonRetryableErrorTypes = []string{}
		}
	}
	manifest := workflowbundle.Manifest{
		Version:            1,
		Name:               firstNonEmpty(req.Name, req.WorkflowID),
		Description:        strings.TrimSpace(req.Description),
		Schedule:           schedule,
		NotificationPolicy: notification,
		Env:                env,
		Steps:              steps,
	}
	if err := workflowbundle.ValidateManifest(manifest); err != nil {
		return workflowbundle.Manifest{}, err
	}
	return manifest, nil
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
	logger.Info("temporal workflow schedule "+action,
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
