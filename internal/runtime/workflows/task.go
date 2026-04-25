package workflows

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
	"github.com/opencto/opencto/internal/runtime/activities"
)

const maxExecutionCycles = 20

func TaskWorkflow(ctx workflow.Context, input TaskWorkflowInput) (TaskWorkflowResult, error) {
	ao := workflow.ActivityOptions{
		StartToCloseTimeout: 10 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}
	ctx = workflow.WithActivityOptions(ctx, ao)

	if !input.ResumedFromPause {
		if err := workflow.ExecuteActivity(ctx, "Activities.PersistEvent", input.Event).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}

		var classification agent.Classification
		if err := workflow.ExecuteActivity(ctx, "Activities.Classify", input.Event).Get(ctx, &classification); err != nil {
			return TaskWorkflowResult{}, err
		}

		if classification.RoutedTo == agent.ClassificationRouteIgnore {
			return TaskWorkflowResult{
				Completed: true,
				Decision: agent.DecisionOutput{
					Classification: classification,
				},
			}, nil
		}

		var decision agent.DecisionOutput
		switch {
		case classification.RequiresClarification():
			if err := workflow.ExecuteActivity(ctx, "Activities.Clarify", input.Event, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
		case classification.RoutedTo == agent.ClassificationRouteIngest:
			if err := workflow.ExecuteActivity(ctx, "Activities.Ingest", input.Event, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		case classification.RoutedTo == agent.ClassificationRouteExecute, classification.RoutedTo == agent.ClassificationRouteAnswer:
			if err := workflow.ExecuteActivity(ctx, "Activities.PrepareReadyDecision", input.Event, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			selection, err := selectTool(ctx, activities.ToolSelectionRequest{
				ProjectID:         input.ProjectID,
				Event:             input.Event,
				Decision:          selectionSnapshot(decision),
				CurrentWorkItemID: currentSelectionWorkItemID(decision),
				ExecutionCycle:    1,
			})
			if err != nil {
				return TaskWorkflowResult{}, err
			}
			if err := applyToolSelection(&decision, selection); err != nil {
				return TaskWorkflowResult{}, err
			}
		default:
			if err := workflow.ExecuteActivity(ctx, "Activities.Plan", input.Event, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			if directReplyMessage(decision) == "" {
				selection, err := selectTool(ctx, activities.ToolSelectionRequest{
					ProjectID:         input.ProjectID,
					Event:             input.Event,
					Decision:          selectionSnapshot(decision),
					CurrentWorkItemID: currentSelectionWorkItemID(decision),
					ExecutionCycle:    1,
				})
				if err != nil {
					return TaskWorkflowResult{}, err
				}
				if err := applyToolSelection(&decision, selection); err != nil {
					return TaskWorkflowResult{}, err
				}
			}
		}

		if decision.Classification.RequiresClarification() {
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(decision)).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			message := decision.Classification.Summary
			if decision.Clarification != nil {
				message = clarificationMessage(decision.Clarification)
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", input.Event, message).Get(ctx, nil)
			reportResult(ctx, input.Event, message)
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}
		if message := directReplyMessage(decision); message != "" {
			applyResponseOutcome(&decision, workflow.Now(ctx))
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(decision)).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", input.Event, message).Get(ctx, nil)
			reportResult(ctx, input.Event, message)
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}

		return executeDecision(ctx, input.Event, input.ProjectID, decision, nil)
	}

	if input.Decision == nil {
		return TaskWorkflowResult{}, fmt.Errorf("resumed task is missing decision payload")
	}
	approvedApproval, err := approvedApprovalForResume(ctx, input)
	if err != nil {
		return TaskWorkflowResult{}, err
	}

	return executeDecision(ctx, input.Event, input.ProjectID, *input.Decision, approvedApproval)
}

func executeDecision(ctx workflow.Context, event domain.Event, projectID string, decision agent.DecisionOutput, approvedApproval *domain.ApprovalRequest) (TaskWorkflowResult, error) {
	currentDecision := decision
	var observationHistory []agent.ExecutionFeedback
	for cycle := executionCycleFromChoice(currentDecision.ToolChoice); cycle <= maxExecutionCycles; cycle++ {
		if err := prepareDecisionForExecution(&currentDecision, cycle, workflow.Now(ctx)); err != nil {
			return TaskWorkflowResult{}, err
		}
		if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}

		var policyResult policy.Result
		if err := workflow.ExecuteActivity(ctx, "Activities.EvaluatePolicy", event, currentDecision.ToolChoice).Get(ctx, &policyResult); err != nil {
			return TaskWorkflowResult{}, err
		}
		if !policyResult.Allowed {
			message := policyDeniedMessage(policyResult, currentDecision)
			currentDecision.ResponseMessage = message
			markCurrentWorkItemStatus(&currentDecision, domain.WorkItemStatusBlocked, workflow.Now(ctx))
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, message).Get(ctx, nil)
			reportResult(ctx, event, message)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}
		if policyResult.RequiresApproval {
			workItem, err := currentExecutionWorkItem(currentDecision)
			if err != nil {
				return TaskWorkflowResult{}, err
			}
			if approvalCoversWorkItem(approvedApproval, workItem, policyResult) {
				approvedApproval = nil
			} else {
				markCurrentWorkItemStatus(&currentDecision, domain.WorkItemStatusAwaitingApproval, workflow.Now(ctx))
				if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
					return TaskWorkflowResult{}, err
				}

				var approval domain.ApprovalRequest
				if err := workflow.ExecuteActivity(ctx, "Activities.CreateApprovalRequest", currentDecision, policyResult).Get(ctx, &approval); err != nil {
					return TaskWorkflowResult{}, err
				}
				reportResult(ctx, event, approvalMessage(approval, currentDecision, policyResult))
				return TaskWorkflowResult{
					AwaitingApproval: true,
					ApprovalRequest:  &approval,
					Decision:         currentDecision,
				}, nil
			}
		}

		var executionResult activities.ExecuteToolResult
		workItem, err := currentExecutionWorkItem(currentDecision)
		if err != nil {
			return TaskWorkflowResult{}, err
		}
		if err := workflow.ExecuteActivity(ctx, "Activities.ExecuteTool", activities.ExecuteToolRequest{
			ProjectID:  firstNonEmpty(currentDecision.Plan.ProjectID, projectID, event.ProjectID),
			WorkItemID: workItem.ID,
			RiskTier:   workItem.RiskTier,
			ToolChoice: currentDecision.ToolChoice,
		}).Get(ctx, &executionResult); err != nil {
			executionResult = failedExecutionActivityResult(currentDecision.ToolChoice, workItem, cycle, err)
		}

		if cycle == maxExecutionCycles {
			summary := cycleLimitSummary(executionResult, cycle)
			markCurrentWorkItemStatus(&currentDecision, domain.WorkItemStatusBlocked, workflow.Now(ctx))
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, summary).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.WriteADR", currentDecision.Plan.ProjectID, "Execution Summary", summary, []string{currentDecision.Plan.Summary}).Get(ctx, nil)
			reportResult(ctx, event, summary)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}

		feedback := executionFeedback(executionResult)
		observationHistory = append(observationHistory, *feedback)
		if dequeueNextToolChoice(&currentDecision, *feedback) {
			continue
		}
		advanceCompletedObservationForSelection(&currentDecision, *feedback, workflow.Now(ctx))
		if shouldCompleteAfterObservation(currentDecision, *feedback) {
			summary := executionSummary(executionResult, cycle)
			if err := setWorkItemStatus(&currentDecision, feedback.WorkItemID, domain.WorkItemStatusCompleted, workflow.Now(ctx)); err != nil {
				return TaskWorkflowResult{}, err
			}
			currentDecision.ResponseMessage = summary
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, summary).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.WriteADR", currentDecision.Plan.ProjectID, "Execution Summary", summary, []string{currentDecision.Plan.Summary}).Get(ctx, nil)
			reportResult(ctx, event, summary)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}

		selection, err := selectTool(ctx, activities.ToolSelectionRequest{
			ProjectID:          projectID,
			Event:              event,
			Decision:           selectionSnapshot(currentDecision),
			CurrentWorkItemID:  currentSelectionWorkItemID(currentDecision),
			Feedback:           feedback,
			ExecutionCycle:     cycle + 1,
			ObservationHistory: observationHistory,
		})
		if err != nil {
			return TaskWorkflowResult{}, err
		}
		if err := applyNextToolSelection(&currentDecision, selection); err != nil {
			return TaskWorkflowResult{}, err
		}

		if allWorkItemsCompleted(currentDecision.WorkItems) {
			summary := executionSummary(executionResult, cycle)
			currentDecision.ResponseMessage = summary
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, summary).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.WriteADR", currentDecision.Plan.ProjectID, "Execution Summary", summary, []string{currentDecision.Plan.Summary}).Get(ctx, nil)
			reportResult(ctx, event, summary)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}

		if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", persistenceSnapshot(currentDecision)).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}
	}

	return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
}

func approvedApprovalForResume(ctx workflow.Context, input TaskWorkflowInput) (*domain.ApprovalRequest, error) {
	if input.ApprovalRequest != nil {
		approval := *input.ApprovalRequest
		if err := validateApprovedApproval(input.ProjectID, input.ApprovalID, approval); err != nil {
			return nil, err
		}
		return &approval, nil
	}

	var approval domain.ApprovalRequest
	if err := workflow.ExecuteActivity(ctx, "Activities.RevalidateApproval", input.ProjectID, input.ApprovalID).Get(ctx, &approval); err != nil {
		return nil, err
	}
	return &approval, nil
}

func validateApprovedApproval(projectID, approvalID string, approval domain.ApprovalRequest) error {
	if strings.TrimSpace(approval.ID) == "" {
		return fmt.Errorf("approved resume is missing approval payload")
	}
	if approval.ID != approvalID {
		return fmt.Errorf("approval payload mismatch: expected %q, got %q", approvalID, approval.ID)
	}
	if approval.ProjectID != projectID {
		return fmt.Errorf("approval project mismatch: expected %q, got %q", projectID, approval.ProjectID)
	}
	if approval.Status != domain.ApprovalStatusApproved {
		return domain.ErrApprovalRequired
	}
	return nil
}

func approvalCoversWorkItem(approval *domain.ApprovalRequest, workItem domain.WorkItem, result policy.Result) bool {
	if approval == nil {
		return false
	}
	if approval.Status != domain.ApprovalStatusApproved {
		return false
	}
	if approval.WorkItemID != workItem.ID {
		return false
	}
	return approval.RiskTier >= result.Tier
}

func failedExecutionActivityResult(choice agent.ToolChoice, workItem domain.WorkItem, cycle int, err error) activities.ExecuteToolResult {
	message := "tool execution activity failed"
	if err != nil {
		message = err.Error()
	}
	return activities.ExecuteToolResult{
		Cycle:            cycle,
		WorkItemID:       workItem.ID,
		Tool:             choice.Type,
		Status:           domain.ExecutionStatusFailed,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Observation:      "Tool execution failed before producing a structured result.",
		Error:            message,
		WorkingDirectory: choice.WorkingDir,
		ResultCode:       "activity_error",
	}
}

func executionFeedback(result activities.ExecuteToolResult) *agent.ExecutionFeedback {
	metadata := map[string]string{}
	if result.WorkingDirectory != "" {
		metadata["working_directory"] = result.WorkingDirectory
	}
	if result.ResultCode != "" {
		metadata["result_code"] = result.ResultCode
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return &agent.ExecutionFeedback{
		Cycle:           result.Cycle,
		WorkItemID:      result.WorkItemID,
		Tool:            result.Tool,
		Status:          string(result.Status),
		RequestedAction: result.RequestedAction,
		Command:         result.Command,
		Args:            result.Args,
		Observation:     result.Observation,
		Error:           result.Error,
		Metadata:        metadata,
	}
}

func executionSummary(result activities.ExecuteToolResult, attempt int) string {
	output := firstNonEmpty(
		result.Observation,
		result.Error,
	)
	if output == "" {
		output = "Execution completed."
	}
	if result.Status == domain.ExecutionStatusFailed {
		return fmt.Sprintf("Attempt %d failed: %s", attempt, output)
	}
	return output
}

func cycleLimitSummary(result activities.ExecuteToolResult, cycle int) string {
	return fmt.Sprintf("Stopped after %d execution cycles. Last observation: %s", cycle, executionSummary(result, cycle))
}

func policyDeniedMessage(result policy.Result, decision agent.DecisionOutput) string {
	action := firstNonEmpty(decision.ToolChoice.Intent, decision.ToolChoice.InputSummary, decision.Plan.Summary, "the selected action")
	action = strings.Join(strings.Fields(action), " ")
	reason := policyReason(result)
	if reason == "" {
		return fmt.Sprintf("I couldn't run `%s` because policy blocked it.", action)
	}
	return fmt.Sprintf("I couldn't run `%s` because policy blocked it: %s.", action, reason)
}

func policyReason(result policy.Result) string {
	parts := make([]string, 0, len(result.Violations)+len(result.Reasons))
	seen := map[string]struct{}{}
	parts = appendPolicyReasons(parts, seen, result.Violations)
	parts = appendPolicyReasons(parts, seen, result.Reasons)
	return strings.Join(parts, "; ")
}

func appendPolicyReasons(parts []string, seen map[string]struct{}, values []string) []string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		parts = append(parts, value)
	}
	return parts
}

func clarificationMessage(clarification *agent.ClarificationRequest) string {
	if clarification == nil {
		return ""
	}
	if message := strings.TrimSpace(clarification.Message); message != "" {
		return message
	}
	parts := make([]string, 0, len(clarification.Questions))
	for _, question := range clarification.Questions {
		if question = strings.TrimSpace(question); question != "" {
			parts = append(parts, question)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}
	return strings.TrimSpace(clarification.Reason)
}

func directReplyMessage(decision agent.DecisionOutput) string {
	if message := strings.TrimSpace(decision.ResponseMessage); message != "" {
		return message
	}
	return ""
}

func applyToolSelection(decision *agent.DecisionOutput, selection activities.ToolSelectionResult) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	if selection.ToolChoice == nil {
		return fmt.Errorf("tool selection returned no tool")
	}
	decision.ResponseMessage = ""
	decision.ToolChoice = *selection.ToolChoice
	decision.ToolChoices = append([]agent.ToolChoice(nil), selection.ToolChoices...)
	return nil
}

func applyNextToolSelection(decision *agent.DecisionOutput, selection activities.ToolSelectionResult) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	return applyToolSelection(decision, selection)
}

func setWorkItemStatus(decision *agent.DecisionOutput, workItemID string, status domain.WorkItemStatus, now time.Time) error {
	if status == "" {
		return nil
	}
	if workItemID == "" {
		markCurrentWorkItemStatus(decision, status, now)
		return nil
	}
	for index := range decision.WorkItems {
		if decision.WorkItems[index].ID == workItemID {
			decision.WorkItems[index].Status = status
			decision.WorkItems[index].UpdatedAt = now
			syncPlanStatus(decision, now)
			return nil
		}
	}
	return fmt.Errorf("work item %q not found for status update", workItemID)
}

func selectionSnapshot(decision agent.DecisionOutput) agent.DecisionOutput {
	snapshot := decision
	snapshot.ToolChoice = agent.ToolChoice{}
	snapshot.ToolChoices = nil
	snapshot.ResponseMessage = ""
	snapshot.Plan.Steps = nil
	snapshot.Plan.WorkItemIDs = nil
	snapshot.Plan.Metadata = keepPlanMetadata(snapshot.Plan.Metadata, "execution_order_json", "test_strategy")
	for idx := range snapshot.WorkItems {
		snapshot.WorkItems[idx].Metadata = keepPlanMetadata(snapshot.WorkItems[idx].Metadata, "depends_on_json")
	}
	return snapshot
}

func persistenceSnapshot(decision agent.DecisionOutput) agent.DecisionOutput {
	snapshot := decision
	snapshot.Classification = agent.Classification{}
	snapshot.ToolChoice = agent.ToolChoice{}
	snapshot.ToolChoices = nil
	snapshot.ResponseMessage = ""
	snapshot.DependencyAudit = nil
	snapshot.Plan.Metadata = dropPlanMetadata(snapshot.Plan.Metadata, "discord_message")
	return snapshot
}

func keepPlanMetadata(source map[string]string, keys ...string) map[string]string {
	if len(source) == 0 || len(keys) == 0 {
		return nil
	}
	kept := make(map[string]string, len(keys))
	for _, key := range keys {
		if value := strings.TrimSpace(source[key]); value != "" {
			kept[key] = value
		}
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func dropPlanMetadata(source map[string]string, keys ...string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	drop := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		drop[key] = struct{}{}
	}
	kept := make(map[string]string, len(source))
	for key, value := range source {
		if _, ok := drop[key]; ok {
			continue
		}
		kept[key] = value
	}
	if len(kept) == 0 {
		return nil
	}
	return kept
}

func prepareDecisionForExecution(decision *agent.DecisionOutput, cycle int, now time.Time) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	if decision.ToolChoice.Type == "" {
		return fmt.Errorf("no tool selected for execution")
	}

	index, err := executionWorkItemIndex(*decision)
	if err != nil {
		return err
	}
	if index < 0 {
		return fmt.Errorf("no incomplete work item available for execution")
	}

	if decision.ToolChoice.Metadata == nil {
		decision.ToolChoice.Metadata = map[string]string{}
	}
	decision.ToolChoice.Metadata["work_item_id"] = decision.WorkItems[index].ID
	decision.ToolChoice.Metadata["execution_cycle"] = strconv.Itoa(cycle)
	decision.WorkItems[index].Status = domain.WorkItemStatusRunning
	decision.WorkItems[index].UpdatedAt = now
	syncPlanStatus(decision, now)
	return nil
}

func executionWorkItemIndex(decision agent.DecisionOutput) (int, error) {
	requestedID := strings.TrimSpace(decision.ToolChoice.Metadata["work_item_id"])
	if requestedID != "" {
		for index, item := range decision.WorkItems {
			if item.ID == requestedID {
				return index, nil
			}
		}
		return -1, fmt.Errorf("work item %q not found for tool execution", requestedID)
	}
	return firstIncompleteWorkItemIndex(decision.WorkItems), nil
}

func currentExecutionWorkItem(decision agent.DecisionOutput) (domain.WorkItem, error) {
	requestedID := strings.TrimSpace(decision.ToolChoice.Metadata["work_item_id"])
	if requestedID != "" {
		for _, item := range decision.WorkItems {
			if item.ID == requestedID {
				return item, nil
			}
		}
		return domain.WorkItem{}, fmt.Errorf("work item %q not found for tool execution", requestedID)
	}

	index := firstIncompleteWorkItemIndex(decision.WorkItems)
	if index < 0 {
		return domain.WorkItem{}, fmt.Errorf("no incomplete work item available for execution")
	}
	return decision.WorkItems[index], nil
}

func dequeueNextToolChoice(decision *agent.DecisionOutput, feedback agent.ExecutionFeedback) bool {
	if decision == nil {
		return false
	}
	if feedback.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(feedback.Error) != "" {
		decision.ToolChoices = nil
		return false
	}
	if len(decision.ToolChoices) == 0 {
		return false
	}
	next := decision.ToolChoices[0]
	decision.ToolChoices = append([]agent.ToolChoice(nil), decision.ToolChoices[1:]...)
	if next.Metadata == nil {
		next.Metadata = map[string]string{}
	}
	if workItemID := strings.TrimSpace(feedback.WorkItemID); workItemID != "" {
		next.Metadata["work_item_id"] = workItemID
	}
	decision.ToolChoice = next
	decision.ResponseMessage = ""
	return true
}

func currentSelectionWorkItemID(decision agent.DecisionOutput) string {
	index := firstIncompleteWorkItemIndex(decision.WorkItems)
	if index < 0 {
		return ""
	}
	return decision.WorkItems[index].ID
}

func advanceSuccessfulWorkItem(decision *agent.DecisionOutput, now time.Time, markCompleted bool) {
	index := firstIncompleteWorkItemIndex(decision.WorkItems)
	if index < 0 {
		syncPlanStatus(decision, now)
		return
	}

	if markCompleted {
		decision.WorkItems[index].Status = domain.WorkItemStatusCompleted
	}
	decision.WorkItems[index].UpdatedAt = now
	syncPlanStatus(decision, now)
}

func advanceCompletedObservationForSelection(decision *agent.DecisionOutput, feedback agent.ExecutionFeedback, now time.Time) {
	if feedback.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(feedback.Error) != "" {
		return
	}
	index := workItemIndexByID(decision.WorkItems, feedback.WorkItemID)
	if index < 0 || decision.WorkItems[index].Status == domain.WorkItemStatusCompleted {
		return
	}
	if nextIncompleteWorkItemIndexAfter(decision.WorkItems, index) < 0 {
		return
	}
	decision.WorkItems[index].Status = domain.WorkItemStatusCompleted
	decision.WorkItems[index].UpdatedAt = now
	syncPlanStatus(decision, now)
}

func shouldCompleteAfterObservation(decision agent.DecisionOutput, feedback agent.ExecutionFeedback) bool {
	if feedback.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(feedback.Error) != "" {
		return false
	}
	if len(decision.WorkItems) == 0 {
		return false
	}
	index := workItemIndexByID(decision.WorkItems, feedback.WorkItemID)
	if index < 0 {
		index = firstIncompleteWorkItemIndex(decision.WorkItems)
	}
	if index < 0 {
		return true
	}
	return nextIncompleteWorkItemIndexAfter(decision.WorkItems, index) < 0
}

func applyResponseOutcome(decision *agent.DecisionOutput, now time.Time) {
	isClarification := looksLikeClarificationMessage(directReplyMessage(*decision))

	terminalStatus := domain.WorkItemStatusCompleted
	if isClarification {
		terminalStatus = domain.WorkItemStatusBlocked
	}

	for i := range decision.WorkItems {
		if decision.WorkItems[i].Status == domain.WorkItemStatusCompleted {
			continue
		}
		decision.WorkItems[i].Status = terminalStatus
		decision.WorkItems[i].UpdatedAt = now
	}
	syncPlanStatus(decision, now)
}

func markCurrentWorkItemStatus(decision *agent.DecisionOutput, status domain.WorkItemStatus, now time.Time) {
	index := firstIncompleteWorkItemIndex(decision.WorkItems)
	if index < 0 {
		syncPlanStatus(decision, now)
		return
	}
	decision.WorkItems[index].Status = status
	decision.WorkItems[index].UpdatedAt = now
	syncPlanStatus(decision, now)
}

func firstIncompleteWorkItemIndex(items []domain.WorkItem) int {
	for index, item := range items {
		if item.Status != domain.WorkItemStatusCompleted {
			return index
		}
	}
	return -1
}

func nextIncompleteWorkItemIndexAfter(items []domain.WorkItem, index int) int {
	for next := index + 1; next < len(items); next++ {
		if items[next].Status != domain.WorkItemStatusCompleted {
			return next
		}
	}
	return -1
}

func workItemIndexByID(items []domain.WorkItem, workItemID string) int {
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

func allWorkItemsCompleted(items []domain.WorkItem) bool {
	if len(items) == 0 {
		return false
	}
	return firstIncompleteWorkItemIndex(items) < 0
}

func shouldDeferCompletionForSelector(decision agent.DecisionOutput, workItem domain.WorkItem) bool {
	return len(decision.WorkItems) == 1 && workItem.RiskTier == domain.RiskTierObserve
}

func syncPlanStatus(decision *agent.DecisionOutput, now time.Time) {
	if decision == nil {
		return
	}

	hasBlocked := false
	allCompleted := len(decision.WorkItems) > 0
	for _, item := range decision.WorkItems {
		switch item.Status {
		case domain.WorkItemStatusAwaitingApproval, domain.WorkItemStatusBlocked, domain.WorkItemStatusFailed:
			hasBlocked = true
		}
		if item.Status != domain.WorkItemStatusCompleted {
			allCompleted = false
		}
	}

	switch {
	case allCompleted:
		decision.Plan.Status = domain.PlanStatusExecuted
	case hasBlocked:
		decision.Plan.Status = domain.PlanStatusBlocked
	default:
		decision.Plan.Status = domain.PlanStatusReady
	}
	decision.Plan.UpdatedAt = now
}

func looksLikeClarificationMessage(message string) bool {
	message = strings.TrimSpace(strings.ToLower(message))
	if message == "" {
		return false
	}
	if strings.Contains(message, "?") {
		return true
	}
	for _, prefix := range []string{
		"can you ",
		"could you ",
		"do you want ",
		"should i ",
		"which ",
		"what ",
		"where ",
		"when ",
		"who ",
		"please confirm ",
		"confirm ",
	} {
		if strings.HasPrefix(message, prefix) {
			return true
		}
	}
	return false
}

func executionCycleFromChoice(choice agent.ToolChoice) int {
	if len(choice.Metadata) == 0 {
		return 1
	}
	value := strings.TrimSpace(choice.Metadata["execution_cycle"])
	if value == "" {
		return 1
	}
	cycle, err := strconv.Atoi(value)
	if err != nil || cycle <= 0 {
		return 1
	}
	return cycle
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func approvalMessage(approval domain.ApprovalRequest, decision agent.DecisionOutput, result policy.Result) string {
	reasons := policyReason(result)
	if reasons == "" {
		reasons = "policy requires approval for this action"
	}
	reviewMessage := approvalReviewMessage(decision)
	return fmt.Sprintf("%s\n\nApproval ID: `%s`\nRisk tier: `%d`\nReason: %s\nReply with `approve %s` or `reject %s optional comment`.",
		reviewMessage,
		approval.ID,
		approval.RiskTier,
		reasons,
		approval.ID,
		approval.ID,
	)
}

func approvalReviewMessage(decision agent.DecisionOutput) string {
	summary := firstNonEmpty(decision.Plan.Summary, decision.ToolChoice.Intent, "planned execution")
	lines := []string{
		"Approval required before I continue.",
		"",
		summary,
	}
	if len(decision.WorkItems) == 0 {
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "")
	for index, item := range decision.WorkItems {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = fmt.Sprintf("Work item %d", index+1)
		}
		suffix := ""
		if item.Status == domain.WorkItemStatusAwaitingApproval {
			suffix = " (awaiting approval)"
		} else if workItemRequiresApproval(item) {
			suffix = " (approval required)"
		}
		lines = append(lines, fmt.Sprintf("%d. [T%d] %s%s", index+1, item.RiskTier, title, suffix))
	}
	return strings.Join(lines, "\n")
}

func workItemRequiresApproval(item domain.WorkItem) bool {
	if item.RiskTier >= domain.RiskTierConsequential {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(item.Metadata["requires_approval"]), "true")
}
