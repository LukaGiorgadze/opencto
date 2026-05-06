package schedule

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/scheduled"
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

	defaultListLimit     = 20
	defaultCatchupWindow = 10 * time.Minute

	memoProjectID           = "opencto_project_id"
	memoScheduleID          = "opencto_schedule_id"
	memoScheduleName        = "opencto_schedule_name"
	memoScheduleNameLegacy  = "opencto_schedule"
	memoScheduleDescription = "opencto_schedule_description"
	memoTask                = "opencto_task"
)

var (
	ErrClientRequired      = errors.New("schedule client is required")
	ErrTaskQueueRequired   = errors.New("task queue is required")
	ErrOperationRequired   = errors.New("schedule operation is required")
	ErrScheduleIDRequired  = errors.New("schedule_id is required")
	ErrTaskRequired        = errors.New("task is required")
	ErrScheduleSpecMissing = errors.New("one_shot_at or cron is required")
	ErrScheduleSpecMixed   = errors.New("one_shot_at and cron cannot both be set")
	ErrPastOneShot         = errors.New("one_shot_at must be in the future")
)

type Request struct {
	ProjectID   string       `json:"-"`
	WorkItemID  string       `json:"-"`
	ToolCallID  string       `json:"-"`
	Intent      string       `json:"-"`
	SourceEvent domain.Event `json:"-"`

	Operation        string `json:"operation"`
	ScheduleID       string `json:"schedule_id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Task             string `json:"task"`
	OneShotAt        string `json:"one_shot_at"`
	Cron             string `json:"cron"`
	Paused           bool   `json:"paused"`
	Note             string `json:"note"`
	Limit            int    `json:"limit"`
	IncludeCompleted bool   `json:"include_completed"`
}

type Result struct {
	Operation       string          `json:"operation"`
	ScheduleID      string          `json:"schedule_id,omitempty"`
	Name            string          `json:"name,omitempty"`
	Description     string          `json:"description,omitempty"`
	Message         string          `json:"message,omitempty"`
	Kind            string          `json:"kind,omitempty"`
	TimeZone        string          `json:"time_zone,omitempty"`
	OneShotAt       string          `json:"one_shot_at,omitempty"`
	Cron            string          `json:"cron,omitempty"`
	Paused          bool            `json:"paused,omitempty"`
	NextActionTimes []string        `json:"next_action_times,omitempty"`
	Schedules       []ScheduleEntry `json:"schedules,omitempty"`
}

type ScheduleEntry struct {
	ID              string   `json:"id"`
	Name            string   `json:"name,omitempty"`
	Description     string   `json:"description,omitempty"`
	Note            string   `json:"note,omitempty"`
	Paused          bool     `json:"paused,omitempty"`
	Completed       bool     `json:"completed,omitempty"`
	WorkflowType    string   `json:"workflow_type,omitempty"`
	NextActionTimes []string `json:"next_action_times,omitempty"`
}

type Executor interface {
	Run(context.Context, Request) (Result, error)
}

type Client interface {
	Create(context.Context, temporalclient.ScheduleOptions) (temporalclient.ScheduleHandle, error)
	List(context.Context, temporalclient.ScheduleListOptions) (temporalclient.ScheduleListIterator, error)
	GetHandle(context.Context, string) temporalclient.ScheduleHandle
}

type TimeZoneResolver func() (*time.Location, string, error)
type IDGenerator func() (string, error)

type TemporalExecutor struct {
	Client          Client
	TaskQueue       string
	Now             func() time.Time
	ResolveTimeZone TimeZoneResolver
	NewID           IDGenerator
	Logger          *slog.Logger
}

func NewTemporalExecutor(client Client, taskQueue string, logger *slog.Logger) *TemporalExecutor {
	if logger == nil {
		logger = slog.Default()
	}
	return &TemporalExecutor{
		Client:          client,
		TaskQueue:       taskQueue,
		ResolveTimeZone: ResolveHostTimeZone,
		NewID:           domain.NewID,
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
	if strings.TrimSpace(e.TaskQueue) == "" {
		return Result{}, ErrTaskQueueRequired
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}

	operation := normalizeOperation(req.Operation)
	switch operation {
	case OperationCreate:
		return e.create(ctx, req)
	case OperationList:
		return e.list(ctx, req)
	case OperationDescribe:
		return e.describe(ctx, req)
	case OperationUpdate:
		return e.update(ctx, req)
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
		return Result{}, fmt.Errorf("unsupported schedule operation %q", req.Operation)
	}
}

func (e *TemporalExecutor) create(ctx context.Context, req Request) (Result, error) {
	options, result, err := e.scheduleOptions(req, true)
	if err != nil {
		return Result{}, err
	}
	handle, err := e.Client.Create(ctx, options)
	if err != nil {
		return Result{}, err
	}
	result.ScheduleID = handle.GetID()
	result.Message = "schedule created"
	e.log("created", result.ScheduleID, result.Name)
	return result, nil
}

func (e *TemporalExecutor) update(ctx context.Context, req Request) (Result, error) {
	options, result, err := e.scheduleOptions(req, false)
	if err != nil {
		return Result{}, err
	}
	handle := e.Client.GetHandle(ctx, options.ID)
	err = handle.Update(ctx, temporalclient.ScheduleUpdateOptions{
		DoUpdate: func(temporalclient.ScheduleUpdateInput) (*temporalclient.ScheduleUpdate, error) {
			remaining := options.RemainingActions
			state := &temporalclient.ScheduleState{
				Note:             options.Note,
				Paused:           options.Paused,
				LimitedActions:   remaining > 0,
				RemainingActions: remaining,
			}
			return &temporalclient.ScheduleUpdate{
				Schedule: &temporalclient.Schedule{
					Action: options.Action,
					Spec:   &options.Spec,
					Policy: &temporalclient.SchedulePolicies{
						Overlap:       options.Overlap,
						CatchupWindow: options.CatchupWindow,
					},
					State: state,
				},
			}, nil
		},
	})
	if err != nil {
		return Result{}, err
	}
	result.Message = "schedule updated"
	e.log("updated", result.ScheduleID, result.Name)
	return result, nil
}

func (e *TemporalExecutor) list(ctx context.Context, req Request) (Result, error) {
	location, timeZoneName, err := e.resolveTimeZone()
	if err != nil {
		return Result{}, err
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	iterator, err := e.Client.List(ctx, temporalclient.ScheduleListOptions{PageSize: limit})
	if err != nil {
		return Result{}, err
	}
	prefix := scheduleIDPrefix(req.ProjectID)
	entries := make([]ScheduleEntry, 0, limit)
	now := e.now()
	for iterator.HasNext() && len(entries) < limit {
		entry, err := iterator.Next()
		if err != nil {
			return Result{}, err
		}
		if prefix != "" && !strings.HasPrefix(entry.ID, prefix) {
			continue
		}
		description, err := e.Client.GetHandle(ctx, entry.ID).Describe(ctx)
		if err != nil {
			return Result{}, err
		}
		completed := scheduleCompleted(description, now)
		if completed && !req.IncludeCompleted {
			continue
		}
		entries = append(entries, scheduleEntry(entry, location, description, completed))
	}
	return Result{
		Operation: OperationList,
		Message:   fmt.Sprintf("found %d schedule(s)", len(entries)),
		TimeZone:  timeZoneName,
		Schedules: entries,
	}, nil
}

func (e *TemporalExecutor) describe(ctx context.Context, req Request) (Result, error) {
	location, timeZoneName, err := e.resolveTimeZone()
	if err != nil {
		return Result{}, err
	}
	id, err := e.resolveExistingScheduleID(ctx, req.ProjectID, req.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	description, err := e.Client.GetHandle(ctx, id).Describe(ctx)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Operation:       OperationDescribe,
		ScheduleID:      id,
		Message:         "schedule described",
		TimeZone:        timeZoneName,
		NextActionTimes: formatTimes(description.Info.NextActionTimes, location),
	}
	fillResultMetadataFromDescription(&result, description)
	if description.Schedule.State != nil {
		result.Paused = description.Schedule.State.Paused
		if result.Description == "" && description.Schedule.State.Note != "" {
			result.Description = strings.TrimSpace(description.Schedule.State.Note)
		}
	}
	if description.Schedule.Spec != nil {
		if len(description.Schedule.Spec.CronExpressions) > 0 {
			result.Kind = "recurring"
			result.Cron = strings.Join(description.Schedule.Spec.CronExpressions, ", ")
		}
	}
	return result, nil
}

func (e *TemporalExecutor) delete(ctx context.Context, req Request) (Result, error) {
	id, err := e.resolveExistingScheduleID(ctx, req.ProjectID, req.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	if err := e.Client.GetHandle(ctx, id).Delete(ctx); err != nil {
		return Result{}, err
	}
	e.log("deleted", id, "")
	return Result{Operation: OperationDelete, ScheduleID: id, Message: "schedule deleted"}, nil
}

func (e *TemporalExecutor) pause(ctx context.Context, req Request) (Result, error) {
	id, err := e.resolveExistingScheduleID(ctx, req.ProjectID, req.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	if err := e.Client.GetHandle(ctx, id).Pause(ctx, temporalclient.SchedulePauseOptions{Note: strings.TrimSpace(req.Note)}); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationPause, ScheduleID: id, Message: "schedule paused", Paused: true}, nil
}

func (e *TemporalExecutor) resume(ctx context.Context, req Request) (Result, error) {
	id, err := e.resolveExistingScheduleID(ctx, req.ProjectID, req.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	if err := e.Client.GetHandle(ctx, id).Unpause(ctx, temporalclient.ScheduleUnpauseOptions{Note: strings.TrimSpace(req.Note)}); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationResume, ScheduleID: id, Message: "schedule resumed"}, nil
}

func (e *TemporalExecutor) trigger(ctx context.Context, req Request) (Result, error) {
	id, err := e.resolveExistingScheduleID(ctx, req.ProjectID, req.ScheduleID)
	if err != nil {
		return Result{}, err
	}
	if err := e.Client.GetHandle(ctx, id).Trigger(ctx, temporalclient.ScheduleTriggerOptions{Overlap: enumspb.SCHEDULE_OVERLAP_POLICY_SKIP}); err != nil {
		return Result{}, err
	}
	return Result{Operation: OperationTrigger, ScheduleID: id, Message: "schedule triggered"}, nil
}

func (e *TemporalExecutor) scheduleOptions(req Request, allowGeneratedID bool) (temporalclient.ScheduleOptions, Result, error) {
	id, err := e.scheduleID(req.ProjectID, req.ScheduleID, allowGeneratedID)
	if err != nil {
		return temporalclient.ScheduleOptions{}, Result{}, err
	}
	name := strings.TrimSpace(req.Name)
	task := strings.TrimSpace(req.Task)
	if task == "" {
		return temporalclient.ScheduleOptions{}, Result{}, ErrTaskRequired
	}
	description := firstNonEmpty(strings.TrimSpace(req.Description), strings.TrimSpace(req.Note))
	if name == "" {
		name = firstNonEmpty(description, task, friendlyScheduleID(id))
	}

	location, timeZoneName, err := e.resolveTimeZone()
	if err != nil {
		return temporalclient.ScheduleOptions{}, Result{}, err
	}
	spec, kind, oneShotAt, cron, remainingActions, err := e.scheduleSpec(req, location, timeZoneName)
	if err != nil {
		return temporalclient.ScheduleOptions{}, Result{}, err
	}

	input := scheduled.DispatchWorkflowInput{
		ProjectID:        strings.TrimSpace(req.ProjectID),
		ScheduleID:       id,
		ScheduleName:     name,
		Task:             task,
		SourceEvent:      req.SourceEvent,
		CreatedByEventID: strings.TrimSpace(req.SourceEvent.ID),
	}
	options := temporalclient.ScheduleOptions{
		ID:   id,
		Spec: spec,
		Action: &temporalclient.ScheduleWorkflowAction{
			ID:        id + ":dispatch",
			Workflow:  scheduled.DispatchWorkflowName,
			Args:      []interface{}{input},
			TaskQueue: strings.TrimSpace(e.TaskQueue),
			Memo: map[string]interface{}{
				memoProjectID:           strings.TrimSpace(req.ProjectID),
				memoScheduleID:          id,
				memoScheduleName:        name,
				memoScheduleNameLegacy:  name,
				memoScheduleDescription: description,
				memoTask:                task,
			},
			StaticSummary: name,
			StaticDetails: description,
		},
		Overlap:          enumspb.SCHEDULE_OVERLAP_POLICY_SKIP,
		CatchupWindow:    defaultCatchupWindow,
		PauseOnFailure:   false,
		Note:             firstNonEmpty(strings.TrimSpace(req.Note), description, name),
		Paused:           req.Paused,
		RemainingActions: remainingActions,
		Memo: map[string]interface{}{
			memoProjectID:           strings.TrimSpace(req.ProjectID),
			memoScheduleID:          id,
			memoScheduleName:        name,
			memoScheduleNameLegacy:  name,
			memoScheduleDescription: description,
			memoTask:                task,
		},
	}
	result := Result{
		Operation:       normalizeOperation(req.Operation),
		ScheduleID:      id,
		Name:            name,
		Description:     description,
		Kind:            kind,
		TimeZone:        timeZoneName,
		OneShotAt:       oneShotAt,
		Cron:            cron,
		Paused:          req.Paused,
		NextActionTimes: nextActionPreview(oneShotAt),
	}
	return options, result, nil
}

func (e *TemporalExecutor) scheduleSpec(req Request, location *time.Location, timeZoneName string) (temporalclient.ScheduleSpec, string, string, string, int, error) {
	oneShotAt := strings.TrimSpace(req.OneShotAt)
	cron := strings.TrimSpace(req.Cron)
	switch {
	case oneShotAt == "" && cron == "":
		return temporalclient.ScheduleSpec{}, "", "", "", 0, ErrScheduleSpecMissing
	case oneShotAt != "" && cron != "":
		return temporalclient.ScheduleSpec{}, "", "", "", 0, ErrScheduleSpecMixed
	case oneShotAt != "":
		parsed, err := time.Parse(time.RFC3339, oneShotAt)
		if err != nil {
			return temporalclient.ScheduleSpec{}, "", "", "", 0, fmt.Errorf("parse one_shot_at as RFC3339: %w", err)
		}
		now := e.now()
		if !parsed.After(now) {
			return temporalclient.ScheduleSpec{}, "", "", "", 0, ErrPastOneShot
		}
		local := parsed.In(location)
		spec := temporalclient.ScheduleSpec{
			Calendars: []temporalclient.ScheduleCalendarSpec{{
				Second:     []temporalclient.ScheduleRange{{Start: local.Second()}},
				Minute:     []temporalclient.ScheduleRange{{Start: local.Minute()}},
				Hour:       []temporalclient.ScheduleRange{{Start: local.Hour()}},
				DayOfMonth: []temporalclient.ScheduleRange{{Start: local.Day()}},
				Month:      []temporalclient.ScheduleRange{{Start: int(local.Month())}},
				Year:       []temporalclient.ScheduleRange{{Start: local.Year()}},
				Comment:    strings.TrimSpace(req.Name),
			}},
			StartAt:      parsed.Add(-time.Second),
			EndAt:        parsed.Add(time.Second),
			TimeZoneName: timeZoneName,
		}
		return spec, "one_shot", parsed.Format(time.RFC3339), "", 1, nil
	default:
		if strings.ContainsAny(cron, "\r\n") {
			return temporalclient.ScheduleSpec{}, "", "", "", 0, fmt.Errorf("cron must be a single-line Temporal cron expression")
		}
		spec := temporalclient.ScheduleSpec{CronExpressions: []string{cron}}
		if !hasCronTimeZonePrefix(cron) {
			spec.TimeZoneName = timeZoneName
		}
		return spec, "recurring", "", cron, 0, nil
	}
}

func (e *TemporalExecutor) scheduleID(projectID, requested string, allowGenerated bool) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		if !allowGenerated {
			return "", ErrScheduleIDRequired
		}
		generator := e.NewID
		if generator == nil {
			generator = domain.NewID
		}
		id, err := generator()
		if err != nil {
			return "", err
		}
		requested = id
	}
	return normalizeScheduleID(projectID, requested)
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

func (e *TemporalExecutor) log(action, scheduleID, name string) {
	logger := e.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Info("temporal schedule "+action,
		slog.String("schedule_id", scheduleID),
		slog.String("name", name),
	)
}

func existingScheduleID(projectID, requested string) (string, error) {
	id, err := normalizeScheduleID(projectID, requested)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(id) == "" {
		return "", ErrScheduleIDRequired
	}
	return id, nil
}

func (e *TemporalExecutor) resolveExistingScheduleID(ctx context.Context, projectID, requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	normalized, err := existingScheduleID(projectID, requested)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(requested, "opencto:") {
		return normalized, nil
	}
	matched, err := e.findScheduleByReference(ctx, projectID, requested, normalized)
	if err != nil {
		return "", err
	}
	if matched != "" {
		return matched, nil
	}
	return normalized, nil
}

func (e *TemporalExecutor) findScheduleByReference(ctx context.Context, projectID, requested, normalized string) (string, error) {
	iterator, err := e.Client.List(ctx, temporalclient.ScheduleListOptions{PageSize: 100})
	if err != nil {
		return "", nil
	}
	prefix := scheduleIDPrefix(projectID)
	requestedSlug := slugify(requested)
	var matches []string
	for iterator.HasNext() {
		entry, err := iterator.Next()
		if err != nil {
			return "", err
		}
		if entry == nil {
			continue
		}
		if prefix != "" && !strings.HasPrefix(entry.ID, prefix) {
			continue
		}
		if entry.ID == normalized {
			return normalized, nil
		}
		if entryMatchesReference(entry, requested, requestedSlug) {
			matches = append(matches, entry.ID)
		}
	}
	if len(matches) == 0 {
		return "", nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple schedules match %q; use a more specific schedule name", requested)
	}
	return matches[0], nil
}

func entryMatchesReference(entry *temporalclient.ScheduleListEntry, requested, requestedSlug string) bool {
	if entry == nil || requestedSlug == "" {
		return false
	}
	candidates := []string{
		friendlyScheduleID(entry.ID),
		entry.Note,
		memoString(entry.Memo, memoScheduleName, memoScheduleNameLegacy),
		memoString(entry.Memo, memoScheduleDescription),
	}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if strings.EqualFold(candidate, requested) || slugify(candidate) == requestedSlug {
			return true
		}
	}
	return false
}

func normalizeScheduleID(projectID, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	prefix := scheduleIDPrefix(projectID)
	if strings.HasPrefix(value, "opencto:") {
		if prefix != "" && !strings.HasPrefix(value, prefix) {
			return "", fmt.Errorf("schedule_id %q is outside project %q", value, projectID)
		}
		return value, nil
	}
	slug := slugify(value)
	if slug == "" {
		return "", ErrScheduleIDRequired
	}
	if prefix == "" {
		return slug, nil
	}
	return prefix + slug, nil
}

func scheduleIDPrefix(projectID string) string {
	slug := slugify(projectID)
	if slug == "" {
		return ""
	}
	return "opencto:" + slug + ":schedule:"
}

var slugPattern = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func slugify(value string) string {
	value = strings.TrimSpace(value)
	value = slugPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	return strings.ToLower(value)
}

func normalizeOperation(operation string) string {
	return strings.ToLower(strings.TrimSpace(operation))
}

func hasCronTimeZonePrefix(cron string) bool {
	upper := strings.ToUpper(strings.TrimSpace(cron))
	return strings.HasPrefix(upper, "TZ=") || strings.HasPrefix(upper, "CRON_TZ=")
}

func formatTimes(times []time.Time, location *time.Location) []string {
	out := make([]string, 0, len(times))
	for _, value := range times {
		if location != nil {
			value = value.In(location)
		} else {
			value = value.UTC()
		}
		out = append(out, value.Format(time.DateTime))
	}
	return out
}

func nextActionPreview(oneShotAt string) []string {
	if strings.TrimSpace(oneShotAt) == "" {
		return nil
	}
	return []string{strings.TrimSpace(oneShotAt)}
}

func scheduleEntry(entry *temporalclient.ScheduleListEntry, location *time.Location, scheduleDescription *temporalclient.ScheduleDescription, completed bool) ScheduleEntry {
	if entry == nil {
		return ScheduleEntry{}
	}
	workflowType := ""
	if strings.TrimSpace(entry.WorkflowType.Name) != "" {
		workflowType = entry.WorkflowType.Name
	}
	descriptionName, descriptionText := scheduleDescriptionMetadata(scheduleDescription)
	name := firstNonEmpty(
		memoString(entry.Memo, memoScheduleName, memoScheduleNameLegacy),
		descriptionName,
		strings.TrimSpace(entry.Note),
		friendlyScheduleID(entry.ID),
	)
	description := firstNonEmpty(memoString(entry.Memo, memoScheduleDescription), descriptionText)
	if description == "" && strings.TrimSpace(entry.Note) != "" && strings.TrimSpace(entry.Note) != name {
		description = strings.TrimSpace(entry.Note)
	}
	nextActionTimes := entry.NextActionTimes
	if scheduleDescription != nil && len(scheduleDescription.Info.NextActionTimes) > 0 {
		nextActionTimes = scheduleDescription.Info.NextActionTimes
	}
	paused := entry.Paused
	if scheduleDescription != nil && scheduleDescription.Schedule.State != nil {
		paused = scheduleDescription.Schedule.State.Paused
	}
	return ScheduleEntry{
		ID:              entry.ID,
		Name:            name,
		Description:     description,
		Note:            entry.Note,
		Paused:          paused,
		Completed:       completed,
		WorkflowType:    workflowType,
		NextActionTimes: formatTimes(nextActionTimes, location),
	}
}

func scheduleCompleted(description *temporalclient.ScheduleDescription, now time.Time) bool {
	if description == nil {
		return false
	}
	if len(description.Info.NextActionTimes) > 0 {
		return false
	}
	if state := description.Schedule.State; state != nil && state.LimitedActions && state.RemainingActions <= 0 {
		return true
	}
	if spec := description.Schedule.Spec; spec != nil && !spec.EndAt.IsZero() && !spec.EndAt.After(now) {
		return true
	}
	return false
}

func fillResultMetadataFromDescription(result *Result, description *temporalclient.ScheduleDescription) {
	if result == nil || description == nil {
		return
	}
	result.Name, result.Description = scheduleDescriptionMetadata(description)
	result.Name = firstNonEmpty(result.Name, memoString(description.Memo, memoScheduleName, memoScheduleNameLegacy))
	result.Description = firstNonEmpty(result.Description, memoString(description.Memo, memoScheduleDescription))
	result.Name = firstNonEmpty(result.Name, friendlyScheduleID(result.ScheduleID))
}

func scheduleDescriptionMetadata(description *temporalclient.ScheduleDescription) (string, string) {
	if description == nil {
		return "", ""
	}
	name := memoString(description.Memo, memoScheduleName, memoScheduleNameLegacy)
	details := memoString(description.Memo, memoScheduleDescription)
	if action, ok := description.Schedule.Action.(*temporalclient.ScheduleWorkflowAction); ok && action != nil {
		name = firstNonEmpty(name, strings.TrimSpace(action.StaticSummary), mapMemoString(action.Memo, memoScheduleName, memoScheduleNameLegacy))
		details = firstNonEmpty(details, strings.TrimSpace(action.StaticDetails), mapMemoString(action.Memo, memoScheduleDescription))
	}
	return name, details
}

func displayScheduleName(entry ScheduleEntry) string {
	return firstNonEmpty(entry.Name, friendlyScheduleID(entry.ID), "untitled schedule")
}

func friendlyScheduleID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	const marker = ":schedule:"
	if index := strings.LastIndex(id, marker); index >= 0 {
		return strings.TrimSpace(id[index+len(marker):])
	}
	return id
}

func memoString(memo *commonpb.Memo, keys ...string) string {
	if memo == nil || len(memo.Fields) == 0 {
		return ""
	}
	for _, key := range keys {
		payload := memo.Fields[key]
		value := payloadString(payload)
		if value != "" {
			return value
		}
	}
	return ""
}

func mapMemoString(memo map[string]interface{}, keys ...string) string {
	if len(memo) == 0 {
		return ""
	}
	for _, key := range keys {
		value := interfaceString(memo[key])
		if value != "" {
			return value
		}
	}
	return ""
}

func interfaceString(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case *commonpb.Payload:
		return payloadString(typed)
	default:
		return ""
	}
}

func payloadString(payload *commonpb.Payload) string {
	if payload == nil {
		return ""
	}
	var value string
	if err := converter.GetDefaultDataConverter().FromPayload(payload, &value); err != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func (r Result) Observation() string {
	var lines []string
	lines = append(lines, firstNonEmpty(r.Message, "schedule operation completed"))
	if r.Name != "" {
		lines = append(lines, "name: "+r.Name)
	}
	if r.Description != "" {
		lines = append(lines, "description: "+r.Description)
	}
	if r.Kind != "" {
		lines = append(lines, "kind: "+r.Kind)
	}
	if r.TimeZone != "" {
		lines = append(lines, "time_zone: "+r.TimeZone)
	}
	if r.OneShotAt != "" {
		lines = append(lines, "one_shot_at: "+r.OneShotAt)
	}
	if r.Cron != "" {
		lines = append(lines, "cron: "+r.Cron)
	}
	if len(r.NextActionTimes) > 0 {
		lines = append(lines, "next_action_times: "+strings.Join(r.NextActionTimes, ", "))
	}
	if len(r.Schedules) > 0 {
		lines = append(lines, "schedules:")
		for index, entry := range r.Schedules {
			prefix := strconv.Itoa(index+1) + ". " + displayScheduleName(entry)
			if entry.Paused {
				prefix += " (paused)"
			}
			if entry.Completed {
				prefix += " (completed)"
			}
			lines = append(lines, prefix)
			if len(entry.NextActionTimes) > 0 {
				lines = append(lines, "   next_action_times: "+strings.Join(entry.NextActionTimes, ", "))
			} else if !entry.Completed {
				lines = append(lines, "   next_action_times: none")
			}
			if entry.Description != "" {
				lines = append(lines, "   description: "+entry.Description)
			}
		}
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
