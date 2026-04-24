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

		var loaded agent.Context
		if err := workflow.ExecuteActivity(ctx, "Activities.LoadContext", input.Event).Get(ctx, &loaded); err != nil {
			return TaskWorkflowResult{}, err
		}

		decisionInput := agent.DecisionInput{
			ProjectID: input.ProjectID,
			Context:   loaded,
		}

		var classification agent.Classification
		if err := workflow.ExecuteActivity(ctx, "Activities.Classify", decisionInput).Get(ctx, &classification); err != nil {
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
			if err := workflow.ExecuteActivity(ctx, "Activities.Clarify", decisionInput, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
		case classification.RoutedTo == agent.ClassificationRouteIngest:
			if err := workflow.ExecuteActivity(ctx, "Activities.Ingest", decisionInput, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		case classification.RoutedTo == agent.ClassificationRouteExecute, classification.RoutedTo == agent.ClassificationRouteAnswer:
			if err := workflow.ExecuteActivity(ctx, "Activities.PrepareReadyDecision", decisionInput, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			if err := workflow.ExecuteActivity(ctx, "Activities.SelectTool", decisionInput, decision, (*agent.ExecutionFeedback)(nil), 1, []agent.ExecutionFeedback(nil)).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
		default:
			if err := workflow.ExecuteActivity(ctx, "Activities.Plan", decisionInput, classification).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
			if err := workflow.ExecuteActivity(ctx, "Activities.SelectTool", decisionInput, decision, (*agent.ExecutionFeedback)(nil), 1, []agent.ExecutionFeedback(nil)).Get(ctx, &decision); err != nil {
				return TaskWorkflowResult{}, err
			}
		}

		if decision.Classification.RequiresClarification() {
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", decision).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			message := decision.Classification.Summary
			if decision.Clarification != nil {
				message = clarificationMessage(decision.Clarification)
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", input.Event, message).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", input.Event, message).Get(ctx, nil)
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}
		if message := directReplyMessage(decision); message != "" {
			applyResponseOutcome(&decision, workflow.Now(ctx))
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", decision).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", input.Event, message).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", input.Event, message).Get(ctx, nil)
			return TaskWorkflowResult{Completed: true, Decision: decision}, nil
		}

		return executeDecision(ctx, input.Event, decisionInput, decision)
	}

	if input.Decision == nil {
		return TaskWorkflowResult{}, fmt.Errorf("resumed task is missing decision payload")
	}
	if err := workflow.ExecuteActivity(ctx, "Activities.RevalidateApproval", input.ProjectID, input.ApprovalID).Get(ctx, nil); err != nil {
		return TaskWorkflowResult{}, err
	}

	var loaded agent.Context
	if err := workflow.ExecuteActivity(ctx, "Activities.LoadContext", input.Event).Get(ctx, &loaded); err != nil {
		return TaskWorkflowResult{}, err
	}

	return executeDecision(ctx, input.Event, agent.DecisionInput{
		ProjectID: input.ProjectID,
		Context:   loaded,
	}, *input.Decision)
}

func executeDecision(ctx workflow.Context, event domain.Event, decisionInput agent.DecisionInput, decision agent.DecisionOutput) (TaskWorkflowResult, error) {
	currentDecision := decision
	var observationHistory []agent.ExecutionFeedback
	for cycle := executionCycleFromChoice(currentDecision.ToolChoice); cycle <= maxExecutionCycles; cycle++ {
		if err := prepareDecisionForExecution(&currentDecision, cycle, workflow.Now(ctx)); err != nil {
			return TaskWorkflowResult{}, err
		}
		if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}

		var policyResult policy.Result
		if err := workflow.ExecuteActivity(ctx, "Activities.EvaluatePolicy", event, currentDecision.ToolChoice).Get(ctx, &policyResult); err != nil {
			return TaskWorkflowResult{}, err
		}
		if !policyResult.Allowed {
			return TaskWorkflowResult{}, domain.ErrPolicyDenied
		}
		if policyResult.RequiresApproval {
			markCurrentWorkItemStatus(&currentDecision, domain.WorkItemStatusAwaitingApproval, workflow.Now(ctx))
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}

			var approval domain.ApprovalRequest
			if err := workflow.ExecuteActivity(ctx, "Activities.CreateApprovalRequest", currentDecision, policyResult).Get(ctx, &approval); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", event, approvalMessage(approval, currentDecision, policyResult)).Get(ctx, nil)
			return TaskWorkflowResult{
				AwaitingApproval: true,
				ApprovalRequest:  &approval,
				Decision:         currentDecision,
			}, nil
		}

		var executionResult activities.ExecuteToolResult
		if err := workflow.ExecuteActivity(ctx, "Activities.ExecuteTool", currentDecision).Get(ctx, &executionResult); err != nil {
			return TaskWorkflowResult{}, err
		}

		if executionResult.Attempt.Status == domain.ExecutionStatusSucceeded {
			advanceSuccessfulWorkItem(&currentDecision, workflow.Now(ctx))
		} else {
			markCurrentWorkItemStatus(&currentDecision, domain.WorkItemStatusFailed, workflow.Now(ctx))
		}
		if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}

		if cycle == maxExecutionCycles {
			summary := cycleLimitSummary(executionResult, cycle)
			applyResponseOutcome(&currentDecision, workflow.Now(ctx))
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, summary).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.WriteADR", currentDecision.Plan.ProjectID, "Execution Summary", summary, []string{currentDecision.Plan.Summary}).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", event, summary).Get(ctx, nil)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}

		feedback := executionFeedback(executionResult)
		if feedback.Metadata == nil {
			feedback.Metadata = map[string]string{}
		}
		feedback.Metadata["working_directory"] = executionResult.Invocation.WorkingDirectory
		observationHistory = append(observationHistory, *feedback)

		if err := workflow.ExecuteActivity(ctx, "Activities.SelectTool", decisionInput, currentDecision, feedback, cycle+1, observationHistory).Get(ctx, &currentDecision); err != nil {
			return TaskWorkflowResult{}, err
		}

		if message := directReplyMessage(currentDecision); message != "" {
			applyResponseOutcome(&currentDecision, workflow.Now(ctx))
			if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil); err != nil {
				return TaskWorkflowResult{}, err
			}
			_ = workflow.ExecuteActivity(ctx, "Activities.PersistConversationMemory", event, message).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.WriteADR", currentDecision.Plan.ProjectID, "Execution Summary", message, []string{currentDecision.Plan.Summary}).Get(ctx, nil)
			_ = workflow.ExecuteActivity(ctx, "Activities.ReportResult", event, message).Get(ctx, nil)
			return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
		}

		if err := workflow.ExecuteActivity(ctx, "Activities.PersistDecision", currentDecision).Get(ctx, nil); err != nil {
			return TaskWorkflowResult{}, err
		}
	}

	return TaskWorkflowResult{Completed: true, Decision: currentDecision}, nil
}

func executionFeedback(result activities.ExecuteToolResult) *agent.ExecutionFeedback {
	return &agent.ExecutionFeedback{
		Cycle:           result.Attempt.Attempt,
		WorkItemID:      result.Attempt.WorkItemID,
		Tool:            result.Invocation.ChosenTool,
		Status:          result.Observation.Status,
		RequestedAction: result.Invocation.RequestedIntent,
		Observation:     result.Observation.Summary,
		Error:           result.Observation.Error,
		Metadata:        copyMetadata(result.Observation.Metadata),
	}
}

func executionSummary(result activities.ExecuteToolResult, attempt int) string {
	output := firstNonEmpty(
		result.Observation.Summary,
		result.Invocation.OutputSummary,
		result.Attempt.OutputSummary,
		result.Observation.Error,
		result.Invocation.ErrorDetails,
	)
	if output == "" {
		output = "Execution completed."
	}
	if result.Attempt.Status == domain.ExecutionStatusFailed {
		return fmt.Sprintf("Attempt %d failed: %s", attempt, output)
	}
	return output
}

func cycleLimitSummary(result activities.ExecuteToolResult, cycle int) string {
	return fmt.Sprintf("Stopped after %d execution cycles. Last observation: %s", cycle, executionSummary(result, cycle))
}

func clarificationMessage(clarification *agent.ClarificationRequest) string {
	if clarification == nil {
		return ""
	}
	parts := make([]string, 0, 1+len(clarification.Questions))
	if message := strings.TrimSpace(clarification.Message); message != "" {
		parts = append(parts, message)
	} else if reason := strings.TrimSpace(clarification.Reason); reason != "" {
		parts = append(parts, reason)
	}
	for _, question := range clarification.Questions {
		if question = strings.TrimSpace(question); question != "" {
			parts = append(parts, question)
		}
	}
	return strings.Join(parts, " ")
}

func directReplyMessage(decision agent.DecisionOutput) string {
	if message := strings.TrimSpace(decision.ToolChoice.ResponseMessage); message != "" {
		return message
	}
	return ""
}

func prepareDecisionForExecution(decision *agent.DecisionOutput, cycle int, now time.Time) error {
	if decision == nil {
		return fmt.Errorf("decision is required")
	}
	if decision.ToolChoice.Type == "" {
		return fmt.Errorf("no tool selected for execution")
	}

	index := firstIncompleteWorkItemIndex(decision.WorkItems)
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

func advanceSuccessfulWorkItem(decision *agent.DecisionOutput, now time.Time) {
	index := firstIncompleteWorkItemIndex(decision.WorkItems)
	if index < 0 {
		syncPlanStatus(decision, now)
		return
	}

	if hasLaterIncompleteWorkItems(decision.WorkItems, index) {
		decision.WorkItems[index].Status = domain.WorkItemStatusCompleted
	}
	decision.WorkItems[index].UpdatedAt = now
	syncPlanStatus(decision, now)
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

func hasLaterIncompleteWorkItems(items []domain.WorkItem, current int) bool {
	for index := current + 1; index < len(items); index++ {
		if items[index].Status != domain.WorkItemStatusCompleted {
			return true
		}
	}
	return false
}

func syncPlanStatus(decision *agent.DecisionOutput, now time.Time) {
	if decision == nil {
		return
	}

	hasBlocked := false
	allCompleted := len(decision.WorkItems) > 0
	for _, item := range decision.WorkItems {
		switch item.Status {
		case domain.WorkItemStatusAwaitingApproval, domain.WorkItemStatusBlocked:
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

func copyMetadata(source map[string]string) map[string]string {
	if len(source) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
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
	reasons := strings.Join(result.Reasons, "; ")
	if reasons == "" {
		reasons = "policy requires approval for this action"
	}
	reviewMessage := strings.TrimSpace(decision.Plan.Metadata["discord_message"])
	if reviewMessage == "" {
		summary := strings.TrimSpace(decision.Plan.Summary)
		if summary == "" {
			summary = strings.TrimSpace(decision.ToolChoice.Intent)
		}
		if summary == "" {
			summary = "planned execution"
		}
		reviewMessage = fmt.Sprintf("Approval required for `%s`.", summary)
	}
	return fmt.Sprintf("%s\n\nApproval ID: `%s`\nRisk tier: `%d`\nReason: %s\nReply with `approve %s` or `reject %s optional comment`.",
		reviewMessage,
		approval.ID,
		approval.RiskTier,
		reasons,
		approval.ID,
		approval.ID,
	)
}
