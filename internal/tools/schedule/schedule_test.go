package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

func TestCreateOneShotScheduleBuildsTemporalSchedule(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("Asia/Tbilisi", 4*60*60)
	client := &fakeScheduleClient{}
	executor := &TemporalExecutor{
		Client:    client,
		TaskQueue: "opencto",
		Now: func() time.Time {
			return time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
		},
		ResolveTimeZone: func() (*time.Location, string, error) {
			return location, "Asia/Tbilisi", nil
		},
		NewID: func() (string, error) {
			return "generated-id", nil
		},
	}

	result, err := executor.Run(context.Background(), Request{
		ProjectID: "project-1",
		SourceEvent: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
		},
		Operation: "create",
		Name:      "deploy app",
		Task:      "deploy this app",
		OneShotAt: "2026-05-07T09:00:00+04:00",
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	if result.ScheduleID != "opencto:project-1:schedule:generated-id" || result.Kind != "one_shot" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(client.created) != 1 {
		t.Fatalf("expected one create call, got %d", len(client.created))
	}
	options := client.created[0]
	if options.RemainingActions != 1 || options.CatchupWindow != 10*time.Minute || options.Overlap != enumspb.SCHEDULE_OVERLAP_POLICY_SKIP {
		t.Fatalf("unexpected schedule policies: %#v", options)
	}
	if options.Spec.TimeZoneName != "Asia/Tbilisi" || len(options.Spec.Calendars) != 1 {
		t.Fatalf("unexpected one-shot spec: %#v", options.Spec)
	}
	calendar := options.Spec.Calendars[0]
	if calendar.Hour[0].Start != 9 || calendar.Minute[0].Start != 0 || calendar.Year[0].Start != 2026 {
		t.Fatalf("unexpected calendar: %#v", calendar)
	}
	action, ok := options.Action.(*temporalclient.ScheduleWorkflowAction)
	if !ok {
		t.Fatalf("expected workflow action, got %T", options.Action)
	}
	if action.Workflow != scheduled.DispatchWorkflowName || action.TaskQueue != "opencto" {
		t.Fatalf("unexpected action: %#v", action)
	}
	input, ok := action.Args[0].(scheduled.DispatchWorkflowInput)
	if !ok {
		t.Fatalf("expected dispatch input, got %T", action.Args[0])
	}
	if input.Task != "deploy this app" || input.SourceEvent.ChannelID != "channel-1" || input.CreatedByEventID != "event-1" {
		t.Fatalf("unexpected dispatch input: %#v", input)
	}
}

func TestCreateRecurringScheduleUsesCronAndHostTimezone(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("Asia/Tbilisi", 4*60*60)
	client := &fakeScheduleClient{}
	executor := &TemporalExecutor{
		Client:    client,
		TaskQueue: "opencto",
		Now: func() time.Time {
			return time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
		},
		ResolveTimeZone: func() (*time.Location, string, error) {
			return location, "Asia/Tbilisi", nil
		},
	}

	result, err := executor.Run(context.Background(), Request{
		ProjectID:   "project-1",
		Operation:   "create",
		ScheduleID:  "daily-hello",
		Name:        "daily hello",
		Task:        "send hello",
		Cron:        "0 9 * * *",
		SourceEvent: domain.Event{ID: "event-1", ProjectID: "project-1"},
	})
	if err != nil {
		t.Fatalf("create recurring schedule: %v", err)
	}
	if result.ScheduleID != "opencto:project-1:schedule:daily-hello" || result.Kind != "recurring" {
		t.Fatalf("unexpected result: %#v", result)
	}
	options := client.created[0]
	if options.RemainingActions != 0 || options.Spec.TimeZoneName != "Asia/Tbilisi" || len(options.Spec.CronExpressions) != 1 || options.Spec.CronExpressions[0] != "0 9 * * *" {
		t.Fatalf("unexpected recurring spec: %#v", options)
	}
}

func TestCreateScheduleReturnsHostTimezoneResolutionError(t *testing.T) {
	t.Parallel()

	client := &fakeScheduleClient{}
	executor := &TemporalExecutor{
		Client:    client,
		TaskQueue: "opencto",
		ResolveTimeZone: func() (*time.Location, string, error) {
			return nil, "", errors.New("no timezone")
		},
	}

	_, err := executor.Run(context.Background(), Request{
		ProjectID:   "project-1",
		Operation:   "create",
		ScheduleID:  "daily-hello",
		Name:        "daily hello",
		Task:        "send hello",
		Cron:        "0 9 * * *",
		SourceEvent: domain.Event{ID: "event-1", ProjectID: "project-1"},
	})
	if err == nil || !errors.Is(err, err) || err.Error() != "no timezone" {
		t.Fatalf("expected timezone error, got %v", err)
	}
	if len(client.created) != 0 {
		t.Fatalf("create should not be called when timezone resolution fails")
	}
}

func TestScheduleCRUDOperationsUseProjectScopedIDs(t *testing.T) {
	t.Parallel()

	location := time.FixedZone("UTC", 0)
	handle := &fakeScheduleHandle{}
	client := &fakeScheduleClient{
		handle: handle,
		entries: []*temporalclient.ScheduleListEntry{
			{
				ID:              "opencto:project-1:schedule:daily",
				Note:            "daily note",
				NextActionTimes: []time.Time{time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC)},
			},
			{ID: "opencto:other:schedule:daily"},
		},
	}
	executor := &TemporalExecutor{
		Client:    client,
		TaskQueue: "opencto",
		Now: func() time.Time {
			return time.Date(2026, 5, 6, 10, 0, 0, 0, time.UTC)
		},
		ResolveTimeZone: func() (*time.Location, string, error) {
			return location, "UTC", nil
		},
	}

	list, err := executor.Run(context.Background(), Request{ProjectID: "project-1", Operation: "list"})
	if err != nil {
		t.Fatalf("list schedules: %v", err)
	}
	if len(list.Schedules) != 1 || list.Schedules[0].ID != "opencto:project-1:schedule:daily" {
		t.Fatalf("unexpected list result: %#v", list)
	}

	for _, operation := range []string{"pause", "resume", "trigger", "delete"} {
		_, err := executor.Run(context.Background(), Request{
			ProjectID:   "project-1",
			Operation:   operation,
			ScheduleID:  "daily",
			SourceEvent: domain.Event{ProjectID: "project-1"},
		})
		if err != nil {
			t.Fatalf("%s schedule: %v", operation, err)
		}
	}
	if handle.paused != 1 || handle.unpaused != 1 || handle.triggered != 1 || handle.deleted != 1 {
		t.Fatalf("unexpected handle calls: %#v", handle)
	}

	_, err = executor.Run(context.Background(), Request{
		ProjectID:   "project-1",
		Operation:   "update",
		ScheduleID:  "daily",
		Name:        "daily updated",
		Task:        "send updated hello",
		Cron:        "@every 24h",
		SourceEvent: domain.Event{ID: "event-1", ProjectID: "project-1"},
	})
	if err != nil {
		t.Fatalf("update schedule: %v", err)
	}
	if handle.updated != 1 {
		t.Fatalf("expected update call, got %#v", handle)
	}
}

type fakeScheduleClient struct {
	created []temporalclient.ScheduleOptions
	handle  *fakeScheduleHandle
	entries []*temporalclient.ScheduleListEntry
}

func (f *fakeScheduleClient) Create(_ context.Context, options temporalclient.ScheduleOptions) (temporalclient.ScheduleHandle, error) {
	f.created = append(f.created, options)
	handle := f.handle
	if handle == nil {
		handle = &fakeScheduleHandle{}
		f.handle = handle
	}
	handle.id = options.ID
	return handle, nil
}

func (f *fakeScheduleClient) List(context.Context, temporalclient.ScheduleListOptions) (temporalclient.ScheduleListIterator, error) {
	return &fakeScheduleIterator{entries: f.entries}, nil
}

func (f *fakeScheduleClient) GetHandle(_ context.Context, id string) temporalclient.ScheduleHandle {
	handle := f.handle
	if handle == nil {
		handle = &fakeScheduleHandle{}
		f.handle = handle
	}
	handle.id = id
	return handle
}

type fakeScheduleHandle struct {
	id        string
	deleted   int
	updated   int
	triggered int
	paused    int
	unpaused  int
}

func (f *fakeScheduleHandle) GetID() string {
	return f.id
}

func (f *fakeScheduleHandle) Delete(context.Context) error {
	f.deleted++
	return nil
}

func (f *fakeScheduleHandle) Backfill(context.Context, temporalclient.ScheduleBackfillOptions) error {
	return nil
}

func (f *fakeScheduleHandle) Update(_ context.Context, options temporalclient.ScheduleUpdateOptions) error {
	f.updated++
	if options.DoUpdate != nil {
		_, err := options.DoUpdate(temporalclient.ScheduleUpdateInput{})
		return err
	}
	return nil
}

func (f *fakeScheduleHandle) Describe(context.Context) (*temporalclient.ScheduleDescription, error) {
	return &temporalclient.ScheduleDescription{
		Schedule: temporalclient.Schedule{
			Spec: &temporalclient.ScheduleSpec{
				CronExpressions: []string{"0 9 * * *"},
				TimeZoneName:    "UTC",
			},
			State: &temporalclient.ScheduleState{},
		},
		Info: temporalclient.ScheduleInfo{
			NextActionTimes: []time.Time{time.Date(2026, 5, 7, 9, 0, 0, 0, time.UTC)},
		},
	}, nil
}

func (f *fakeScheduleHandle) Trigger(context.Context, temporalclient.ScheduleTriggerOptions) error {
	f.triggered++
	return nil
}

func (f *fakeScheduleHandle) Pause(context.Context, temporalclient.SchedulePauseOptions) error {
	f.paused++
	return nil
}

func (f *fakeScheduleHandle) Unpause(context.Context, temporalclient.ScheduleUnpauseOptions) error {
	f.unpaused++
	return nil
}

type fakeScheduleIterator struct {
	entries []*temporalclient.ScheduleListEntry
	index   int
}

func (f *fakeScheduleIterator) HasNext() bool {
	return f.index < len(f.entries)
}

func (f *fakeScheduleIterator) Next() (*temporalclient.ScheduleListEntry, error) {
	entry := f.entries[f.index]
	f.index++
	return entry, nil
}
