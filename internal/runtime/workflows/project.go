package workflows

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/approval"
	"github.com/opencto/opencto/internal/runtime/scheduled"
)

const (
	reportResponseActivityTimeout = time.Minute
	recentProjectEventIDLimit     = 1000
	maxActiveProjectTasks         = 4
)

var planningTokenPattern = regexp.MustCompile(`(?i)\b[QP]-[0-9a-f]{8}\b`)

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
	waitingSignal := workflow.GetSignalChannel(ctx, SignalProjectTaskWaiting)
	outputSignal := workflow.GetSignalChannel(ctx, SignalProjectTaskOutput)
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
		selector.AddReceive(waitingSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal PlanningWaitSignal
			c.Receive(ctx, &signal)
			handleTaskWaitingSignal(active, signal)
			registerTaskWaitingOwnership(threadOwners, active, signal)
			workflow.GetLogger(ctx).Info("project task waiting signal",
				"workflow_id", strings.TrimSpace(signal.WorkflowID),
				"event_id", strings.TrimSpace(signal.EventID),
				"waiting_token", normalizePlanningToken(signal.Token),
				"waiting_kind", strings.TrimSpace(signal.Kind),
				"thread_id", strings.TrimSpace(signal.Event.ThreadID),
				"thread_owners", len(threadOwners),
			)
		})
		selector.AddReceive(outputSignal, func(c workflow.ReceiveChannel, more bool) {
			var signal TaskOutputSignal
			c.Receive(ctx, &signal)
			registerTaskOutputOwnership(messageOwners, threadOwners, active, signal)
			workflow.GetLogger(ctx).Info("project task output ownership signal",
				"workflow_id", strings.TrimSpace(signal.WorkflowID),
				"event_id", strings.TrimSpace(signal.EventID),
				"receipts", len(signal.Receipts),
				"message_owners", len(messageOwners),
				"thread_owners", len(threadOwners),
			)
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
	Future       workflow.ChildWorkflowFuture
	Event        domain.Event
	WorkflowID   string
	WaitingToken string
	WaitingKind  string
}

type routeOwner struct {
	WorkflowID   string
	WaitingKind  string
	WaitingEvent string
}

func (o routeOwner) waiting() bool {
	return strings.TrimSpace(o.WaitingKind) != ""
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
		"planning_token", normalizePlanningToken(event.Metadata[domain.MetadataKeyPlanningToken]),
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
			"waiting_kind", strings.TrimSpace(owner.WaitingKind),
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
			"waiting_kind", strings.TrimSpace(owner.WaitingKind),
			"message_owners", len(messageOwners),
			"thread_owners", len(threadOwners),
		)
		signalRoutedEvent(ctx, owner, event)
		return
	}

	if isStandaloneDiscordMainEvent(event) {
		workflow.GetLogger(ctx).Info("project route standalone discord message as new task",
			"event_id", event.ID,
			"source_message_id", strings.TrimSpace(event.Provenance.SourceID),
			"channel_id", strings.TrimSpace(event.ChannelID),
		)
		state.Queue = append(state.Queue, event)
		return
	}

	if event.ChannelType != domain.ChannelTypeDiscord {
		if token := planningTokenFromEvent(event); token != "" {
			if routePlanningAnswer(ctx, active, token, event) {
				workflow.GetLogger(ctx).Info("project route event as planning answer",
					"event_id", event.ID,
					"planning_token", token,
				)
				return
			}
			workflow.GetLogger(ctx).Info("project route event stale planning token",
				"event_id", event.ID,
				"planning_token", token,
			)
			reportStalePlanningToken(ctx, event, token)
			return
		}
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

func isStandaloneDiscordMainEvent(event domain.Event) bool {
	if event.ChannelType != domain.ChannelTypeDiscord {
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
		if owner := cleanRouteOwner(threadOwners[threadID]); owner.WorkflowID != "" {
			return owner
		}
	}
	channelID := strings.TrimSpace(event.ChannelID)
	if channelID == "" {
		return routeOwner{}
	}
	return cleanRouteOwner(threadOwners[channelID])
}

func shouldSignalRoutedEvent(event domain.Event) bool {
	return strings.TrimSpace(event.Body) != "" || planningTokenFromEvent(event) != ""
}

func signalRoutedEvent(ctx workflow.Context, owner routeOwner, event domain.Event) {
	if owner.WorkflowID == "" || !shouldSignalRoutedEvent(event) {
		return
	}
	if owner.waiting() {
		signalPlanningAnswer(ctx, owner.WorkflowID, owner.WaitingKind, planningAnswerDecision(owner.WaitingKind, event.Body), event)
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

func handleTaskWaitingSignal(active map[string]activeTask, signal PlanningWaitSignal) {
	token := normalizePlanningToken(signal.Token)
	kind := strings.TrimSpace(signal.Kind)
	if kind == "" {
		return
	}
	if strings.TrimSpace(signal.WorkflowID) != "" {
		for eventID, task := range active {
			if task.WorkflowID != signal.WorkflowID {
				continue
			}
			task.WaitingToken = token
			task.WaitingKind = kind
			active[eventID] = task
			return
		}
	}
	if strings.TrimSpace(signal.EventID) == "" {
		return
	}
	for eventID, task := range active {
		if eventID != signal.EventID {
			continue
		}
		task.WaitingToken = token
		task.WaitingKind = kind
		active[eventID] = task
		return
	}
}

func registerTaskWaitingOwnership(threadOwners map[string]routeOwner, active map[string]activeTask, signal PlanningWaitSignal) {
	task := activeTaskByWorkflowOrEvent(active, signal.WorkflowID, signal.EventID)
	if task.WorkflowID == "" {
		return
	}
	registerThreadOwner(threadOwners, signal.Event.ThreadID, routeOwner{
		WorkflowID:   task.WorkflowID,
		WaitingKind:  strings.TrimSpace(signal.Kind),
		WaitingEvent: strings.TrimSpace(signal.EventID),
	})
}

func registerTaskOutputOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, active map[string]activeTask, signal TaskOutputSignal) {
	task := activeTaskByWorkflowOrEvent(active, signal.WorkflowID, signal.EventID)
	if task.WorkflowID == "" {
		return
	}
	waitingKind := strings.TrimSpace(signal.WaitingKind)
	if waitingKind != "" {
		downgradeWaitingOwners(messageOwners, threadOwners, task.WorkflowID)
	}
	for _, receipt := range signal.Receipts {
		owner := routeOwner{
			WorkflowID:   task.WorkflowID,
			WaitingKind:  waitingKind,
			WaitingEvent: strings.TrimSpace(signal.EventID),
		}
		registerMessageOwner(messageOwners, receipt.MessageID, owner)
		registerThreadOwner(threadOwners, receipt.ThreadID, owner)
		registerThreadOwner(threadOwners, receipt.MessageID, owner)
	}
}

func registerTaskEventOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, task activeTask) {
	registerRoutedEventOwnership(messageOwners, threadOwners, task.Event, routeOwner{WorkflowID: task.WorkflowID})
}

func registerRoutedEventOwnership(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, event domain.Event, owner routeOwner) {
	registerMessageOwner(messageOwners, event.Provenance.SourceID, owner)
	registerThreadOwner(threadOwners, event.ThreadID, owner)
	if strings.TrimSpace(event.ThreadID) != "" {
		registerThreadOwner(threadOwners, event.ChannelID, owner)
	}
}

func activeTaskByWorkflowOrEvent(active map[string]activeTask, workflowID string, eventID string) activeTask {
	workflowID = strings.TrimSpace(workflowID)
	eventID = strings.TrimSpace(eventID)
	for currentEventID, task := range active {
		if workflowID != "" && task.WorkflowID == workflowID {
			return task
		}
		if eventID != "" && currentEventID == eventID {
			return task
		}
	}
	return activeTask{}
}

func registerMessageOwner(messageOwners map[string]routeOwner, messageID string, owner routeOwner) {
	messageID = strings.TrimSpace(messageID)
	owner = cleanRouteOwner(owner)
	if messageID == "" || owner.WorkflowID == "" {
		return
	}
	messageOwners[messageID] = owner
}

func registerThreadOwner(threadOwners map[string]routeOwner, threadID string, owner routeOwner) {
	threadID = strings.TrimSpace(threadID)
	owner = cleanRouteOwner(owner)
	if threadID == "" || owner.WorkflowID == "" {
		return
	}
	threadOwners[threadID] = owner
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

func downgradeWaitingOwners(messageOwners map[string]routeOwner, threadOwners map[string]routeOwner, workflowID string) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	for key, owner := range messageOwners {
		if owner.WorkflowID == workflowID && owner.waiting() {
			owner.WaitingKind = ""
			owner.WaitingEvent = ""
			messageOwners[key] = owner
		}
	}
	for key, owner := range threadOwners {
		if owner.WorkflowID == workflowID && owner.waiting() {
			owner.WaitingKind = ""
			owner.WaitingEvent = ""
			threadOwners[key] = owner
		}
	}
}

func cleanRouteOwner(owner routeOwner) routeOwner {
	owner.WorkflowID = strings.TrimSpace(owner.WorkflowID)
	owner.WaitingKind = strings.TrimSpace(owner.WaitingKind)
	owner.WaitingEvent = strings.TrimSpace(owner.WaitingEvent)
	return owner
}

func routePlanningAnswer(ctx workflow.Context, active map[string]activeTask, token string, event domain.Event) bool {
	token = normalizePlanningToken(token)
	if token == "" || len(active) == 0 {
		return false
	}
	task := activeTaskByWaitToken(active, token)
	if task.WorkflowID != "" {
		signalPlanningAnswer(ctx, task.WorkflowID, task.WaitingKind, planningAnswerDecision(task.WaitingKind, event.Body), event)
		clearActiveWaitingToken(active, token)
		return true
	}
	return false
}

func planningAnswerDecision(waitingKind string, body string) string {
	if strings.TrimSpace(waitingKind) != "plan" {
		return ""
	}
	if approval.IsApprovalPhrase(body) {
		return domain.MetadataApprovalApproved
	}
	return domain.MetadataApprovalRevision
}

func reportStalePlanningToken(ctx workflow.Context, event domain.Event, token string) {
	token = normalizePlanningToken(token)
	if token == "" {
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
		Event:   event,
		Message: "No active task is waiting for `" + token + "`. That planning reply looks stale, so I did not attach it to another task.",
	}).Get(reportCtx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Warn("report stale planning token failed", "token", token, "error", err.Error())
	}
}

func activeTaskByWaitToken(active map[string]activeTask, token string) activeTask {
	token = normalizePlanningToken(token)
	for _, task := range active {
		if normalizePlanningToken(task.WaitingToken) == token {
			return task
		}
	}
	return activeTask{}
}

func signalPlanningAnswer(ctx workflow.Context, workflowID string, waitingKind string, decision string, event domain.Event) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	waitingKind = strings.TrimSpace(waitingKind)
	decision = strings.TrimSpace(decision)
	event = eventWithPlanningMetadata(event, waitingKind, decision)
	err := workflow.SignalExternalWorkflow(ctx, workflowID, "", SignalTaskPlanningAnswer, PlanningAnswerSignal{
		Event:       event,
		WaitingKind: waitingKind,
		Decision:    decision,
	}).Get(ctx, nil)
	if err != nil {
		workflow.GetLogger(ctx).Warn("signal planning answer failed", "workflow_id", workflowID, "waiting_kind", waitingKind, "error", err.Error())
	}
}

func eventWithPlanningMetadata(event domain.Event, waitingKind string, decision string) domain.Event {
	metadata := domain.Metadata{}
	for key, value := range event.Metadata {
		metadata[key] = value
	}
	metadata[domain.MetadataKeyControl] = domain.MetadataControlPlanningAnswer
	if waitingKind != "" {
		metadata[domain.MetadataKeyWaitingKind] = waitingKind
	}
	if decision != "" {
		metadata[domain.MetadataKeyApprovalDecision] = decision
	}
	event.Metadata = metadata
	return event
}

func clearActiveWaitingToken(active map[string]activeTask, token string) {
	token = normalizePlanningToken(token)
	for eventID, task := range active {
		if normalizePlanningToken(task.WaitingToken) != token {
			continue
		}
		task.WaitingToken = ""
		task.WaitingKind = ""
		active[eventID] = task
	}
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

func planningTokenFromEvent(event domain.Event) string {
	if event.Metadata != nil {
		if token := normalizePlanningToken(event.Metadata[domain.MetadataKeyPlanningToken]); token != "" {
			return token
		}
	}
	return normalizePlanningToken(planningTokenPattern.FindString(event.Body))
}

func normalizePlanningToken(token string) string {
	token = strings.Trim(strings.TrimSpace(token), "`.,:;()[]{}")
	return strings.ToUpper(token)
}
