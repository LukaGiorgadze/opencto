package workflows

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

const (
	reportResponseActivityTimeout = time.Minute
	recentProjectEventIDLimit     = 1000
	maxActiveProjectTasks         = 4
)

func ProjectWorkflow(ctx workflow.Context, input ProjectWorkflowInput) error {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})

	state := ProjectWorkflowState{
		ProjectID: input.ProjectID,
	}
	if input.Snapshot != nil {
		state = *input.Snapshot
		trimRecentProjectEventIDs(&state)
	}

	eventSignal := workflow.GetSignalChannel(ctx, SignalEnqueueEvent)
	active := map[string]activeTask{}
	messageOwners := map[string]routeOwner{}
	threadOwners := map[string]routeOwner{}
	var pendingReports []TaskWorkflowResult

	for {
		for len(pendingReports) > 0 {
			report := pendingReports[0]
			pendingReports = pendingReports[1:]
			reportTaskResult(ctx, report)
		}

		if input.ContinueAsNewAfterEvents > 0 && state.ProcessedEvents >= input.ContinueAsNewAfterEvents && len(active) == 0 {
			snapshot := state
			return workflow.NewContinueAsNewError(ctx, ProjectWorkflow, ProjectWorkflowInput{
				ProjectID:                input.ProjectID,
				ContinueAsNewAfterEvents: input.ContinueAsNewAfterEvents,
				Snapshot:                 &snapshot,
			})
		}

		started := false
		for len(active) < maxActiveProjectTasks && len(state.Queue) > 0 {
			event := state.Queue[0]
			state.Queue = state.Queue[1:]
			workflowID := taskWorkflowID(input.ProjectID, event.ID)
			childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
				WorkflowID: workflowID,
			})
			future := workflow.ExecuteChildWorkflow(childCtx, TaskWorkflowName, TaskWorkflowInput{
				ProjectID: input.ProjectID,
				Event:     event,
			})
			if err := future.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
				pendingReports = append(pendingReports, failedTaskWorkflowResult(event, err))
				state.ProcessedEvents++
				continue
			}
			task := activeTask{Future: future, Event: event, WorkflowID: workflowID}
			active[event.ID] = task
			registerTaskEventOwnership(messageOwners, threadOwners, task)
			workflow.GetLogger(ctx).Info("project task ownership registered",
				"event_id", event.ID,
				"workflow_id", workflowID,
				"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
				"channel_id", strings.TrimSpace(event.ChannelID),
				"thread_id", strings.TrimSpace(event.ThreadID),
				"message_owners", len(messageOwners),
				"thread_owners", len(threadOwners),
			)
			started = true
		}
		if started {
			continue
		}

		selector := workflow.NewSelector(ctx)
		selector.AddReceive(eventSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal EnqueueEventSignal
			c.Receive(ctx, &signal)
			handleProjectEventSignal(ctx, &state, active, messageOwners, threadOwners, signal.Event)
		})
		for eventID, task := range active {
			selector.AddFuture(task.Future, func(f workflow.Future) {
				var result TaskWorkflowResult
				if err := f.Get(ctx, &result); err != nil {
					result = failedTaskWorkflowResult(task.Event, err)
				}
				if result.Report {
					pendingReports = append(pendingReports, result)
				}
				clearTaskOwnership(messageOwners, threadOwners, task.WorkflowID)
				delete(active, eventID)
				state.ProcessedEvents++
			})
		}
		selector.Select(ctx)
	}
}

type activeTask struct {
	Future     workflow.ChildWorkflowFuture
	Event      domain.Event
	WorkflowID string
}

type routeOwner struct {
	WorkflowID string
}

func reportTaskResult(ctx workflow.Context, result TaskWorkflowResult) {
	report := domain.ReportMessage{
		Text:        result.ResponseMessage,
		Attachments: result.ResponseAttachments,
	}
	if !result.Report || report.Empty() {
		return
	}
	reportCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		ScheduleToCloseTimeout: reportResponseActivityTimeout,
		StartToCloseTimeout:    reportResponseActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	})
	err := workflow.ExecuteActivity(reportCtx, "Activities.ReportResponse", activities.ReportResponseRequest{
		Event:       result.Event,
		Message:     report.Text,
		Attachments: report.Attachments,
		ReplyTo:     reportReplyFromEvent(result.Event),
	}).Get(reportCtx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Error(
			"report response activity failed",
			"project_id", result.Event.ProjectID,
			"event_id", result.Event.ID,
			"error", err.Error(),
		)
	}
}

func reportReplyFromEvent(event domain.Event) *domain.ReportReply {
	if len(event.Metadata) == 0 {
		return nil
	}
	reply := &domain.ReportReply{
		MessageID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]),
		ChannelID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToChannelID]),
		ContextID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToContextID]),
	}
	if reply.Empty() {
		return nil
	}
	return reply
}

func failedTaskWorkflowResult(event domain.Event, err error) TaskWorkflowResult {
	return TaskWorkflowResult{
		Completed:       false,
		Status:          activities.NextActionStatusFailed,
		Event:           event,
		ResponseMessage: taskWorkflowFailureMessage(err),
		Report:          true,
	}
}

func taskWorkflowFailureMessage(err error) string {
	detail := taskWorkflowFailureDetail(err)
	if detail == "" {
		return "I couldn't complete the task because the task workflow failed."
	}
	return "I couldn't complete the task: " + detail
}

func taskWorkflowFailureDetail(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if text == "" {
		return ""
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "contextwindowexceeded") || strings.Contains(lower, "context window") || strings.Contains(lower, "input tokens exceed") {
		return "the model context window was exceeded. Please reduce the amount of context and try again."
	}
	if index := strings.IndexByte(text, '\n'); index >= 0 {
		text = text[:index]
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxDetailLength = 500
	if len(text) > maxDetailLength {
		text = text[:maxDetailLength] + "..."
	}
	return "the task workflow failed (" + text + ")."
}

func handleProjectEventSignal(ctx workflow.Context, state *ProjectWorkflowState, active map[string]activeTask, messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, event domain.Event) {
	if rememberProjectEventID(state, event.ID) {
		workflow.GetLogger(ctx).Info("project route event ignored duplicate",
			"event_id", event.ID,
			"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
		)
		return
	}
	workflow.GetLogger(ctx).Info("project route event received",
		"event_id", event.ID,
		"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
		"channel_id", strings.TrimSpace(event.ChannelID),
		"thread_id", strings.TrimSpace(event.ThreadID),
		"reply_to_message_id", strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]),
		"active_tasks", len(active),
		"message_owners", len(messageOwners),
		"thread_owners", len(threadOwners),
		"body", truncateWorkflowLogText(event.Body, 180),
	)
	if scheduled.IsScheduledTaskEvent(event) {
		if scheduledTaskOverlaps(state, active, event) {
			workflow.GetLogger(ctx).Info("project route event ignored overlapping scheduled task",
				"event_id", event.ID,
				"schedule_id", scheduled.ScheduleID(event),
			)
			return
		}
		workflow.GetLogger(ctx).Info("project route event queued scheduled task",
			"event_id", event.ID,
			"schedule_id", scheduled.ScheduleID(event),
		)
		state.Queue = append(state.Queue, event)
		return
	}

	if owner := taskRouteOwnerByReply(messageOwners, event); owner.WorkflowID != "" {
		registerRoutedEventOwnership(messageOwners, threadOwners, event, routeOwner{WorkflowID: owner.WorkflowID})
		workflow.GetLogger(ctx).Info("project route event as task reply",
			"route", "reply",
			"event_id", event.ID,
			"workflow_id", owner.WorkflowID,
			"reply_to_message_id", strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]),
			"message_owners", len(messageOwners),
			"thread_owners", len(threadOwners),
		)
		signalRoutedEvent(ctx, owner, event)
		return
	}
	if owner := taskRouteOwnerByThread(threadOwners, event); owner.WorkflowID != "" {
		registerRoutedEventOwnership(messageOwners, threadOwners, event, routeOwner{WorkflowID: owner.WorkflowID})
		workflow.GetLogger(ctx).Info("project route event as task reply",
			"route", "thread",
			"event_id", event.ID,
			"workflow_id", owner.WorkflowID,
			"channel_id", strings.TrimSpace(event.ChannelID),
			"thread_id", strings.TrimSpace(event.ThreadID),
			"message_owners", len(messageOwners),
			"thread_owners", len(threadOwners),
		)
		signalRoutedEvent(ctx, owner, event)
		return
	}
	if isStandaloneChannelMainEvent(event) {
		workflow.GetLogger(ctx).Info("project route standalone channel message as new task",
			"event_id", event.ID,
			"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
			"channel_type", strings.TrimSpace(string(event.ChannelType)),
			"channel_id", strings.TrimSpace(event.ChannelID),
		)
		state.Queue = append(state.Queue, event)
		return
	}

	targetWorkflowID := firstActiveTaskWorkflowID(active)
	switch projectControlAction(event) {
	case "cancel":
		if targetWorkflowID != "" {
			workflow.GetLogger(ctx).Info("project route event as cancel",
				"event_id", event.ID,
				"workflow_id", targetWorkflowID,
			)
			_ = workflow.SignalExternalWorkflow(ctx, targetWorkflowID, "", SignalTaskCancel, TaskControlSignal{Event: event, Reason: "project message requested cancel"}).Get(ctx, nil)
		}
	case "interrupt":
		if targetWorkflowID != "" {
			workflow.GetLogger(ctx).Info("project route event as interrupt",
				"event_id", event.ID,
				"workflow_id", targetWorkflowID,
			)
			_ = workflow.SignalExternalWorkflow(ctx, targetWorkflowID, "", SignalTaskInterrupt, TaskControlSignal{Event: event, Reason: "project message requested interrupt"}).Get(ctx, nil)
		}
	default:
		workflow.GetLogger(ctx).Info("project route event queued new task",
			"event_id", event.ID,
			"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
			"channel_id", strings.TrimSpace(event.ChannelID),
			"thread_id", strings.TrimSpace(event.ThreadID),
		)
		state.Queue = append(state.Queue, event)
	}
}

func truncateWorkflowLogText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func isStandaloneChannelMainEvent(event domain.Event) bool {
	switch event.ChannelType {
	case domain.ChannelTypeDiscord, domain.ChannelTypeTelegram:
	default:
		return false
	}
	if strings.TrimSpace(event.ThreadID) != "" {
		return false
	}
	if strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]) != "" {
		return false
	}
	return true
}

func taskRouteOwnerByReply(messageOwners map[string]routeOwner, event domain.Event) routeOwner {
	messageID := strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID])
	if messageID == "" {
		return routeOwner{}
	}
	return cleanRouteOwner(messageOwners[messageID])
}

func taskRouteOwnerByThread(threadOwners map[string]routeOwner, event domain.Event) routeOwner {
	threadID := strings.TrimSpace(event.ThreadID)
	if threadID != "" {
		if owner := cleanRouteOwner(threadOwners[threadOwnerKey(event.ChannelID, threadID)]); owner.WorkflowID != "" {
			return owner
		}
		if owner := cleanRouteOwner(threadOwners[threadID]); owner.WorkflowID != "" {
			return owner
		}
	}
	channelID := strings.TrimSpace(event.ChannelID)
	if channelID == "" {
		return routeOwner{}
	}
	if owner := cleanRouteOwner(threadOwners[threadOwnerKey(channelID, channelID)]); owner.WorkflowID != "" {
		return owner
	}
	return cleanRouteOwner(threadOwners[channelID])
}

func shouldSignalRoutedEvent(event domain.Event) bool {
	return strings.TrimSpace(event.Body) != ""
}

func signalRoutedEvent(ctx workflow.Context, owner routeOwner, event domain.Event) {
	if owner.WorkflowID == "" || !shouldSignalRoutedEvent(event) {
		return
	}
	signalAdditionalContext(ctx, owner.WorkflowID, eventWithControl(event, domain.MetadataControlTaskReply))
}

func signalAdditionalContext(ctx workflow.Context, workflowID string, event domain.Event) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	err := workflow.SignalExternalWorkflow(ctx, workflowID, "", SignalTaskAdditionalContext, AdditionalContextSignal{Event: event}).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Warn("signal additional context failed", "workflow_id", workflowID, "error", err.Error())
	}
}

func eventWithControl(event domain.Event, control string) domain.Event {
	control = strings.TrimSpace(control)
	if control == "" {
		return event
	}
	metadata := domain.Metadata{}
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	metadata[domain.MetadataKeyControl] = control
	event.Metadata = metadata
	return event
}

func registerTaskEventOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, task activeTask) {
	registerRoutedEventOwnership(messageOwners, threadOwners, task.Event, routeOwner{WorkflowID: task.WorkflowID})
}

func registerRoutedEventOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, event domain.Event, owner routeOwner) {
	registerMessageOwner(messageOwners, event.Provenance.SourceID, owner)
	registerThreadOwner(threadOwners, event.ChannelID, event.ThreadID, owner)
	if strings.TrimSpace(event.ThreadID) != "" && strings.TrimSpace(event.ChannelID) == strings.TrimSpace(event.ThreadID) {
		registerThreadOwner(threadOwners, event.ChannelID, event.ChannelID, owner)
	}
}

func registerMessageOwner(messageOwners map[string]routeOwner, messageID string, owner routeOwner) {
	messageID = strings.TrimSpace(messageID)
	owner = cleanRouteOwner(owner)
	if messageID == "" || owner.WorkflowID == "" {
		return
	}
	messageOwners[messageID] = owner
}

func registerThreadOwner(threadOwners map[string]routeOwner, channelID, threadID string, owner routeOwner) {
	threadID = strings.TrimSpace(threadID)
	owner = cleanRouteOwner(owner)
	if threadID == "" || owner.WorkflowID == "" {
		return
	}
	threadOwners[threadOwnerKey(channelID, threadID)] = owner
}

func threadOwnerKey(channelID, threadID string) string {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return ""
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return threadID
	}
	return channelID + "\x00" + threadID
}

func clearTaskOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, workflowID string) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	for key, owner := range messageOwners {
		if owner.WorkflowID == workflowID {
			delete(messageOwners, key)
		}
	}
	for key, owner := range threadOwners {
		if owner.WorkflowID == workflowID {
			delete(threadOwners, key)
		}
	}
}

func cleanRouteOwner(owner routeOwner) routeOwner {
	owner.WorkflowID = strings.TrimSpace(owner.WorkflowID)
	return owner
}

func scheduledTaskOverlaps(state *ProjectWorkflowState, active map[string]activeTask, event domain.Event) bool {
	scheduleID := scheduled.ScheduleID(event)
	if scheduleID == "" {
		return false
	}
	for _, queued := range state.Queue {
		if scheduled.ScheduleID(queued) == scheduleID {
			return true
		}
	}
	for _, task := range active {
		if scheduled.ScheduleID(task.Event) == scheduleID {
			return true
		}
	}
	return false
}

func taskWorkflowID(projectID, eventID string) string {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		eventID = "event"
	}
	return fmt.Sprintf("%s:task:%s", projectID, eventID)
}

func firstActiveTaskWorkflowID(active map[string]activeTask) string {
	tasks := sortedActiveTasks(active)
	if len(tasks) == 0 {
		return ""
	}
	return tasks[0].WorkflowID
}

func sortedActiveTasks(active map[string]activeTask) []activeTask {
	eventIDs := make([]string, 0, len(active))
	for eventID := range active {
		eventIDs = append(eventIDs, eventID)
	}
	sort.Strings(eventIDs)
	tasks := make([]activeTask, 0, len(eventIDs))
	for _, eventID := range eventIDs {
		tasks = append(tasks, active[eventID])
	}
	return tasks
}

func rememberProjectEventID(state *ProjectWorkflowState, eventID string) bool {
	eventID = strings.TrimSpace(eventID)
	if eventID == "" {
		return false
	}
	for _, recentID := range state.RecentEventIDs {
		if recentID == eventID {
			return true
		}
	}
	state.RecentEventIDs = append(state.RecentEventIDs, eventID)
	trimRecentProjectEventIDs(state)
	return false
}

func trimRecentProjectEventIDs(state *ProjectWorkflowState) {
	if len(state.RecentEventIDs) <= recentProjectEventIDLimit {
		return
	}
	start := len(state.RecentEventIDs) - recentProjectEventIDLimit
	state.RecentEventIDs = append([]string(nil), state.RecentEventIDs[start:]...)
}

func projectControlAction(event domain.Event) string {
	if event.Metadata != nil {
		switch strings.ToLower(strings.TrimSpace(event.Metadata[domain.MetadataKeyControl])) {
		case "cancel", "stop":
			return "cancel"
		case "interrupt":
			return "interrupt"
		}
	}
	switch strings.ToLower(strings.TrimSpace(event.Body)) {
	case "cancel", "/cancel", "stop", "/stop":
		return "cancel"
	case "interrupt", "/interrupt":
		return "interrupt"
	default:
		return ""
	}
}
