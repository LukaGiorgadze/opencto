package activities

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

func ensureNextAction(nextAction *agent.NextAction, projectID string, event domain.Event, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	summary := firstNonEmpty(strings.TrimSpace(event.Body), "Handle inbound request.")
	if len(nextAction.WorkItems) == 0 {
		workItemID := stableActivityID("work-item", projectID, event.ID, "1")
		nextAction.WorkItems = []domain.WorkItem{{
			ID:          workItemID,
			ProjectID:   projectID,
			Title:       "Handle request",
			Description: summary,
			Status:      domain.WorkItemStatusReady,
			CreatedAt:   now,
			UpdatedAt:   now,
		}}
		return nil
	}
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ProjectID == "" {
			nextAction.WorkItems[index].ProjectID = projectID
		}
		if nextAction.WorkItems[index].Status == "" {
			nextAction.WorkItems[index].Status = domain.WorkItemStatusReady
		}
		if nextAction.WorkItems[index].CreatedAt.IsZero() {
			nextAction.WorkItems[index].CreatedAt = now
		}
		if nextAction.WorkItems[index].UpdatedAt.IsZero() {
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	return nil
}

func ensureToolWorkItem(nextAction *agent.NextAction, workItemID string, now time.Time) error {
	if nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID) >= 0 {
		return nil
	}
	if strings.TrimSpace(workItemID) == "" {
		return fmt.Errorf("work item id is required")
	}
	item := domain.WorkItem{
		ID:          workItemID,
		ProjectID:   nextActionProjectID(*nextAction),
		Title:       "Handle request",
		Description: "Handle inbound request.",
		Status:      domain.WorkItemStatusReady,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	nextAction.WorkItems = append(nextAction.WorkItems, item)
	return nil
}

func nextActionProjectID(nextAction agent.NextAction) string {
	for _, item := range nextAction.WorkItems {
		if projectID := strings.TrimSpace(item.ProjectID); projectID != "" {
			return projectID
		}
	}
	return ""
}

func currentNextActionWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	if observation != nil {
		workItemID := strings.TrimSpace(observation.WorkItemID)
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			return workItemID
		}
	}
	for _, item := range nextAction.WorkItems {
		if item.Status != domain.WorkItemStatusCompleted {
			return item.ID
		}
	}
	return ""
}

func nextActionToolWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	return currentNextActionWorkItemID(nextAction, observation)
}

func completePreviousWorkItemForNextAction(nextAction *agent.NextAction, nextWorkItemID string, observation *agent.ExecutionFeedback, now time.Time) error {
	if observation == nil {
		return nil
	}
	if observation.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(observation.Error) != "" {
		return nil
	}
	previousWorkItemID := strings.TrimSpace(observation.WorkItemID)
	if previousWorkItemID == "" || previousWorkItemID == strings.TrimSpace(nextWorkItemID) {
		return nil
	}
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, previousWorkItemID)
	if index < 0 {
		return fmt.Errorf("work item %q not found for status update", previousWorkItemID)
	}
	nextAction.WorkItems[index].Status = domain.WorkItemStatusCompleted
	nextAction.WorkItems[index].UpdatedAt = now
	return nil
}

func ensureToolChoiceMetadata(choice *agent.ToolChoice, workItemID string, cycle int, assistantText string) {
	if choice.Metadata == nil {
		choice.Metadata = map[string]string{}
	}
	toolCallID := strings.TrimSpace(choice.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(choice.Metadata["tool_call_id"])
	}
	if toolCallID == "" {
		toolCallID = "toolu_" + stableActivityID("tool-call", workItemID, strconv.Itoa(cycle))
	}
	choice.ToolCallID = toolCallID
	choice.Metadata["tool_call_id"] = toolCallID
	choice.Metadata["work_item_id"] = workItemID
	choice.Metadata["execution_cycle"] = strconv.Itoa(cycle)
	if strings.TrimSpace(assistantText) != "" {
		choice.Metadata["assistant_text"] = strings.TrimSpace(assistantText)
	}
	if choice.WorkingDir != "" {
		choice.Metadata["working_directory"] = choice.WorkingDir
	}
	if choice.TimeoutMs > 0 {
		choice.Metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
	if choice.RunMode == "" {
		choice.RunMode = domain.ToolRunModeWaitForExit
	}
	if choice.Idempotency == "" {
		choice.Idempotency = domain.ToolIdempotencyUnknown
	}
	if choice.ProcessScope == "" {
		choice.ProcessScope = domain.ProcessScopeStopOnFinish
	}
	choice.Metadata["run_mode"] = string(choice.RunMode)
	choice.Metadata["idempotency"] = string(choice.Idempotency)
	choice.Metadata["process_scope"] = string(choice.ProcessScope)
}

func toolProcessScope(scope domain.ProcessScope) domain.ProcessScope {
	if scope == domain.ProcessScopeProject {
		return domain.ProcessScopeProject
	}
	return domain.ProcessScopeStopOnFinish
}

func markFinalNextActionWorkItems(nextAction *agent.NextAction, status domain.WorkItemStatus, observation *agent.ExecutionFeedback, now time.Time) {
	if status == "" {
		status = domain.WorkItemStatusCompleted
	}
	if observation != nil && strings.TrimSpace(observation.WorkItemID) != "" {
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].Status == domain.WorkItemStatusCompleted {
			continue
		}
		nextAction.WorkItems[index].Status = status
		nextAction.WorkItems[index].UpdatedAt = now
	}
}

func terminalWorkItemStatus(status string) domain.WorkItemStatus {
	switch status {
	case NextActionStatusBlocked:
		return domain.WorkItemStatusBlocked
	case NextActionStatusFailed:
		return domain.WorkItemStatusFailed
	default:
		return domain.WorkItemStatusCompleted
	}
}

func cycleLimitResponseMessage(history []agent.ExecutionFeedback) string {
	if len(history) == 0 {
		return "Stopped after reaching the execution cycle limit before a response was produced."
	}

	var builder strings.Builder
	builder.WriteString("Stopped after reaching the execution cycle limit. Full execution history:")
	for _, feedback := range history {
		builder.WriteString("\n\n")
		builder.WriteString(fmt.Sprintf("cycle: %d", feedback.Cycle))
		if feedback.Tool != "" {
			builder.WriteString(fmt.Sprintf("\ntool: %s", feedback.Tool))
		}
		if feedback.Status != "" {
			builder.WriteString(fmt.Sprintf("\nstatus: %s", feedback.Status))
		}
		if feedback.RequestedAction != "" {
			builder.WriteString(fmt.Sprintf("\nrequested_action: %s", feedback.RequestedAction))
		}
		if feedback.Command != "" {
			builder.WriteString(fmt.Sprintf("\ncommand: %s", feedback.Command))
		}
		if len(feedback.Args) > 0 {
			builder.WriteString(fmt.Sprintf("\nargs: %s", strings.Join(feedback.Args, " ")))
		}
		if text := strings.TrimSpace(feedback.Observation); text != "" {
			builder.WriteString("\nobservation:\n")
			builder.WriteString(text)
		}
		if text := strings.TrimSpace(feedback.Error); text != "" {
			builder.WriteString("\nerror:\n")
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func applyObservationToNextAction(nextAction *agent.NextAction, observation agent.ExecutionFeedback, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	if observation.WorkItemID == "" {
		return nil
	}
	status := domain.WorkItemStatusReady
	if observation.Status == string(domain.ExecutionStatusCanceled) {
		status = domain.WorkItemStatusBlocked
	}
	if err := setNextActionWorkItemStatus(nextAction, observation.WorkItemID, status, now); err != nil {
		return err
	}
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
	if index >= 0 {
		if nextAction.WorkItems[index].Metadata == nil {
			nextAction.WorkItems[index].Metadata = map[string]string{}
		}
		nextAction.WorkItems[index].Metadata["last_execution_status"] = observation.Status
		if code := strings.TrimSpace(observation.Metadata["result_code"]); code != "" {
			nextAction.WorkItems[index].Metadata["last_result_code"] = code
		}
	}
	return nil
}

func setNextActionWorkItemStatus(nextAction *agent.NextAction, workItemID string, status domain.WorkItemStatus, now time.Time) error {
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ID == workItemID {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("work item %q not found for status update", workItemID)
}

func nextActionWorkItemIndexByID(items []domain.WorkItem, workItemID string) int {
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		return -1
	}
	for index, item := range items {
		if item.ID == workItemID {
			return index
		}
	}
	return -1
}

func executionCycle(metadata map[string]string) int {
	if len(metadata) == 0 {
		return 1
	}
	value := strings.TrimSpace(metadata["execution_cycle"])
	if value == "" {
		return 1
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 1
	}
	return parsed
}
