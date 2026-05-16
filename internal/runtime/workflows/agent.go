package workflows

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	toolregistry "github.com/opencto/opencto/internal/tools"
	agenttool "github.com/opencto/opencto/internal/tools/agenttool"
)

func AgentWorkflow(ctx workflow.Context, input AgentWorkflowInput) (AgentWorkflowResult, error) {
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(input.Event.ProjectID)
	}
	event := input.Event
	event.ProjectID = projectID

	req, allowedTools, maxTurns, err := decodeAgentWorkflowRequest(input.ToolChoice)
	if err != nil {
		return failedAgentWorkflowResult(err), nil
	}

	workflowID := strings.TrimSpace(input.AgentWorkflowID)
	if workflowID == "" {
		workflowID = workflow.GetInfo(ctx).WorkflowExecution.ID
	}
	lineage := agentLineage{
		ParentWorkItemID: strings.TrimSpace(input.ParentWorkItemID),
		ParentToolCallID: strings.TrimSpace(input.ParentToolCallID),
		ParentWorkflowID: strings.TrimSpace(input.ParentWorkflowID),
		AgentWorkflowID:  workflowID,
		Goal:             req.Goal,
	}

	nextActionAO := workflow.ActivityOptions{
		StartToCloseTimeout: nextActionActivityTimeout,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    5,
		},
	}
	toolAO := workflow.ActivityOptions{
		StartToCloseTimeout: toolActivityTimeout,
		HeartbeatTimeout:    toolActivityHeartbeatTimeout,
		WaitForCancellation: true,
		RetryPolicy: &temporal.RetryPolicy{
			MaximumAttempts: 1,
		},
	}
	persistenceAO := workflow.ActivityOptions{
		StartToCloseTimeout: time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    30 * time.Second,
		},
	}
	compressionAO := workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    2 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumAttempts:    3,
		},
	}

	nextActionCtx := workflow.WithActivityOptions(ctx, nextActionAO)
	toolCtx := workflow.WithActivityOptions(ctx, toolAO)
	persistenceCtx := workflow.WithActivityOptions(ctx, persistenceAO)
	compressionCtx := workflow.WithActivityOptions(ctx, compressionAO)

	currentAction := agent.NextAction{}
	if input.InitialNextAction != nil {
		currentAction = *input.InitialNextAction
	}
	additionalEvents := append([]domain.Event(nil), input.AdditionalEvents...)
	observationHistory := append([]agent.ExecutionFeedback(nil), input.InitialObservation...)
	runSummary := strings.TrimSpace(input.InitialRunSummary)
	var lastResults []activities.ExecuteToolResult
	var processes []domain.ProcessReference
	var toolsUsed []domain.ToolType
	var filesTouched []string

	for turn := 1; turn <= maxTurns; turn++ {
		signals, canceled, interrupted := drainAgentWorkflowSignals(ctx, &additionalEvents)
		if err := persistTaskSignalEvents(persistenceCtx, signals); err != nil {
			return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
		}
		if canceled {
			return AgentWorkflowResult{Status: domain.ExecutionStatusCanceled, Message: "Agent canceled.", TurnCount: turn - 1, FilesTouched: filesTouched, ToolsUsed: toolsUsed, Processes: processes}, nil
		}
		if interrupted {
			lastResults = nil
		}

		compressed, err := compressAgentObservations(compressionCtx, activities.CompressAgentObservationsRequest{
			ProjectID:       projectID,
			Goal:            req.Goal,
			PreviousSummary: runSummary,
			Observations:    observationHistory,
		})
		if err != nil {
			return failedAgentWorkflowResultWithState(turn-1, toolsUsed, filesTouched, processes, err), nil
		}
		runSummary = strings.TrimSpace(compressed.Summary)
		if compressed.RemainingObservations != nil {
			observationHistory = compressed.RemainingObservations
		}

		next, err := nextAction(nextActionCtx, activities.NextActionRequest{
			ProjectID:          projectID,
			Event:              event,
			AdditionalEvents:   additionalEvents,
			NextAction:         currentAction,
			LastResults:        lastResults,
			ObservationHistory: observationHistory,
			Processes:          processes,
			ExecutionCycle:     turn,
			ResumedFromPause:   input.ResumedFromPause,
			SubAgent: &agent.SubAgentContext{
				Goal:       req.Goal,
				Prompt:     req.Prompt,
				RunSummary: runSummary,
				RunID:      workflowID,
			},
			ToolAllowlist: allowedTools,
			RestrictTools: true,
		})
		if err != nil {
			return failedAgentWorkflowResultWithState(turn-1, toolsUsed, filesTouched, processes, err), nil
		}

		currentAction = next.NextAction
		mergeAgentProcessReferences(&processes, next.Processes)
		if len(next.Observations) > 0 {
			observationHistory = append(observationHistory, next.Observations...)
		} else if next.Observation != nil {
			observationHistory = append(observationHistory, *next.Observation)
		}
		next.NextAction = annotateAgentNextAction(next.NextAction, lineage)
		if err := persistNextAction(persistenceCtx, activities.PersistNextActionRequest{
			Event:            event,
			NextAction:       next.NextAction,
			Status:           next.Status,
			SkipConversation: true,
		}); err != nil {
			return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
		}

		if next.IsTerminal() {
			if next.Status == activities.NextActionStatusCompleted && input.CompletionTool != nil {
				workItemID := firstNonEmpty(next.WorkItemID, firstAgentWorkItemID(next.NextAction), lineage.ParentWorkItemID)
				execResult, canceled, interrupted, signalEvents, err := executeAgentCompletionTool(ctx, toolCtx, persistenceCtx, projectID, workItemID, event, *input.CompletionTool, turn, lineage, &additionalEvents)
				mergeAgentProcessReferences(&processes, execResult.Processes)
				var traceErrors []error
				if err := persistTaskSignalEvents(persistenceCtx, signalEvents); err != nil {
					traceErrors = append(traceErrors, err)
				}
				if err != nil {
					execResult = failedExecutionActivityResult(*input.CompletionTool, workItemID, turn, err)
					execResult = annotateAgentToolResult(execResult, lineage)
				}
				if err := persistToolResult(persistenceCtx, activities.PersistToolResultRequest{
					Event:            event,
					Result:           execResult,
					SkipConversation: true,
				}); err != nil {
					traceErrors = append(traceErrors, err)
				}
				toolsUsed = appendUniqueAgentToolType(toolsUsed, execResult.Tool)
				if execResult.Status == domain.ExecutionStatusSucceeded {
					return agentWorkflowResultFromCompletion(next, turn, toolsUsed, filesTouched, processes, execResult, traceErrors...), nil
				}
				if canceled {
					if err := firstAgentTraceError(traceErrors); err != nil {
						return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
					}
					return AgentWorkflowResult{Status: domain.ExecutionStatusCanceled, Message: "Agent canceled.", TurnCount: turn, FilesTouched: filesTouched, ToolsUsed: toolsUsed, Processes: processes}, nil
				}
				if err := firstAgentTraceError(traceErrors); err != nil {
					return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
				}
				lastResults = []activities.ExecuteToolResult{execResult}
				currentAction.ResponseMessage = ""
				currentAction.ResponseAttachments = nil
				if interrupted {
					continue
				}
				continue
			}
			return agentWorkflowResultFromNextAction(next, turn, toolsUsed, filesTouched, processes), nil
		}

		toolChoices := append([]agent.ToolChoice(nil), next.ToolChoices...)
		if len(toolChoices) == 0 && next.ToolChoice != nil {
			toolChoices = []agent.ToolChoice{*next.ToolChoice}
		}
		if len(toolChoices) == 0 {
			return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, fmt.Errorf("Activities.NextAction returned tool status without a tool choice")), nil
		}
		if strings.TrimSpace(next.WorkItemID) == "" {
			return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, fmt.Errorf("Activities.NextAction returned a tool choice without a work item id")), nil
		}

		lastResults = nil
		for index := range toolChoices {
			if err := validateAgentChildTool(toolChoices[index], allowedTools); err != nil {
				return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), nil
			}
			toolChoices[index] = annotateAgentToolChoice(toolChoices[index], lineage)
			execResult, canceled, interrupted, signalEvents, err := executeToolStep(ctx, toolCtx, persistenceCtx, projectID, next.WorkItemID, event, toolChoices[index], turn, &additionalEvents)
			mergeAgentProcessReferences(&processes, execResult.Processes)
			if err := persistTaskSignalEvents(persistenceCtx, signalEvents); err != nil {
				return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
			}
			if canceled {
				return AgentWorkflowResult{Status: domain.ExecutionStatusCanceled, Message: "Agent canceled.", TurnCount: turn, FilesTouched: filesTouched, ToolsUsed: toolsUsed, Processes: processes}, nil
			}
			if err != nil {
				execResult = failedExecutionActivityResult(toolChoices[index], next.WorkItemID, turn, err)
			}
			execResult = annotateAgentToolResult(execResult, lineage)
			if err := persistToolResult(persistenceCtx, activities.PersistToolResultRequest{
				Event:            event,
				Result:           execResult,
				SkipConversation: true,
			}); err != nil {
				return failedAgentWorkflowResultWithState(turn, toolsUsed, filesTouched, processes, err), err
			}
			lastResults = append(lastResults, execResult)
			toolsUsed = appendUniqueAgentToolType(toolsUsed, execResult.Tool)
			filesTouched = appendUniqueAgentStrings(filesTouched, changedFilesFromAgentToolResult(execResult)...)
			if interrupted {
				break
			}
		}
	}

	final, err := nextAction(nextActionCtx, activities.NextActionRequest{
		ProjectID:          projectID,
		Event:              event,
		AdditionalEvents:   additionalEvents,
		NextAction:         currentAction,
		LastResults:        lastResults,
		ObservationHistory: observationHistory,
		Processes:          processes,
		ExecutionCycle:     maxTurns + 1,
		ForceFinal:         true,
		ResumedFromPause:   input.ResumedFromPause,
		SubAgent: &agent.SubAgentContext{
			Goal:       req.Goal,
			Prompt:     req.Prompt,
			RunSummary: runSummary,
			RunID:      workflowID,
		},
		ToolAllowlist: allowedTools,
		RestrictTools: true,
	})
	if err != nil {
		return failedAgentWorkflowResultWithState(maxTurns, toolsUsed, filesTouched, processes, err), nil
	}
	mergeAgentProcessReferences(&processes, final.Processes)
	if len(final.Observations) > 0 {
		observationHistory = append(observationHistory, final.Observations...)
	} else if final.Observation != nil {
		observationHistory = append(observationHistory, *final.Observation)
	}
	final.NextAction = annotateAgentNextAction(final.NextAction, lineage)
	if err := persistNextAction(persistenceCtx, activities.PersistNextActionRequest{
		Event:            event,
		NextAction:       final.NextAction,
		Status:           final.Status,
		SkipConversation: true,
	}); err != nil {
		return failedAgentWorkflowResultWithState(maxTurns+1, toolsUsed, filesTouched, processes, err), err
	}
	if !final.IsTerminal() {
		return AgentWorkflowResult{
			Status:       domain.ExecutionStatusFailed,
			Message:      agentCycleLimitResponseMessage(observationHistory),
			TurnCount:    maxTurns + 1,
			FilesTouched: filesTouched,
			ToolsUsed:    toolsUsed,
			Processes:    processes,
			Error:        "agent reached max turns without a terminal response",
		}, nil
	}
	if final.Status == activities.NextActionStatusCompleted && input.CompletionTool != nil {
		workItemID := firstNonEmpty(final.WorkItemID, firstAgentWorkItemID(final.NextAction), lineage.ParentWorkItemID)
		execResult, canceled, _, signalEvents, err := executeAgentCompletionTool(ctx, toolCtx, persistenceCtx, projectID, workItemID, event, *input.CompletionTool, maxTurns+1, lineage, &additionalEvents)
		mergeAgentProcessReferences(&processes, execResult.Processes)
		var traceErrors []error
		if err := persistTaskSignalEvents(persistenceCtx, signalEvents); err != nil {
			traceErrors = append(traceErrors, err)
		}
		if err != nil {
			execResult = failedExecutionActivityResult(*input.CompletionTool, workItemID, maxTurns+1, err)
			execResult = annotateAgentToolResult(execResult, lineage)
		}
		if err := persistToolResult(persistenceCtx, activities.PersistToolResultRequest{
			Event:            event,
			Result:           execResult,
			SkipConversation: true,
		}); err != nil {
			traceErrors = append(traceErrors, err)
		}
		toolsUsed = appendUniqueAgentToolType(toolsUsed, execResult.Tool)
		if execResult.Status == domain.ExecutionStatusSucceeded {
			return agentWorkflowResultFromCompletion(final, maxTurns+1, toolsUsed, filesTouched, processes, execResult, traceErrors...), nil
		}
		if canceled {
			if err := firstAgentTraceError(traceErrors); err != nil {
				return failedAgentWorkflowResultWithState(maxTurns+1, toolsUsed, filesTouched, processes, err), err
			}
			return AgentWorkflowResult{Status: domain.ExecutionStatusCanceled, Message: "Agent canceled.", TurnCount: maxTurns + 1, FilesTouched: filesTouched, ToolsUsed: toolsUsed, Processes: processes}, nil
		}
		if err := firstAgentTraceError(traceErrors); err != nil {
			return failedAgentWorkflowResultWithState(maxTurns+1, toolsUsed, filesTouched, processes, err), err
		}
		return AgentWorkflowResult{
			Status:       domain.ExecutionStatusFailed,
			Message:      agentCycleLimitResponseMessage(append(observationHistory, executionFeedbackFromToolResult(execResult))),
			TurnCount:    maxTurns + 1,
			FilesTouched: filesTouched,
			ToolsUsed:    toolsUsed,
			Processes:    processes,
			Error:        firstNonEmpty(execResult.Error, execResult.Observation, "workflow authoring validation failed"),
		}, nil
	}
	return agentWorkflowResultFromNextAction(final, maxTurns+1, toolsUsed, filesTouched, processes), nil
}

func decodeAgentWorkflowRequest(choice agent.ToolChoice) (agenttool.Request, []domain.ToolType, int, error) {
	var req agenttool.Request
	if err := json.Unmarshal(choice.Input, &req); err != nil {
		return agenttool.Request{}, nil, 0, fmt.Errorf("decode agent input: %w", err)
	}
	req.Goal = strings.TrimSpace(req.Goal)
	req.Prompt = strings.TrimSpace(req.Prompt)
	if req.Goal == "" {
		return agenttool.Request{}, nil, 0, fmt.Errorf("agent goal is required")
	}
	if req.Prompt == "" {
		return agenttool.Request{}, nil, 0, fmt.Errorf("agent prompt is required")
	}
	allowedTools, err := agentWorkflowAllowedTools(req)
	if err != nil {
		return agenttool.Request{}, nil, 0, err
	}
	return req, allowedTools, agenttool.NormalizeMaxTurns(req.MaxTurns), nil
}

func agentWorkflowAllowedTools(req agenttool.Request) ([]domain.ToolType, error) {
	if req.AllowedTools == nil {
		return toolregistry.ModelToolTypes(false), nil
	}
	return agenttool.ValidateAllowedTools(*req.AllowedTools, func(toolType domain.ToolType) bool {
		return toolregistry.SupportsToolType(toolType) && toolType != domain.ToolTypeAgent
	})
}

func validateAgentChildTool(choice agent.ToolChoice, allowedTools []domain.ToolType) error {
	if choice.Type == domain.ToolTypeAgent {
		return fmt.Errorf("Agent tool recursion is not allowed")
	}
	for _, toolType := range allowedTools {
		if choice.Type == toolType {
			return nil
		}
	}
	return fmt.Errorf("tool %q is not allowed in this agent context", choice.Type)
}

type agentLineage struct {
	ParentWorkItemID string
	ParentToolCallID string
	ParentWorkflowID string
	AgentWorkflowID  string
	Goal             string
}

func (l agentLineage) Metadata() domain.Metadata {
	metadata := domain.Metadata{
		"agent_workflow_id":   strings.TrimSpace(l.AgentWorkflowID),
		"agent_goal":          strings.TrimSpace(l.Goal),
		"parent_work_item_id": strings.TrimSpace(l.ParentWorkItemID),
		"parent_tool_call_id": strings.TrimSpace(l.ParentToolCallID),
		"parent_workflow_id":  strings.TrimSpace(l.ParentWorkflowID),
		"agent_child_trace":   "true",
	}
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			delete(metadata, key)
		}
	}
	return metadata
}

func annotateAgentNextAction(nextAction agent.NextAction, lineage agentLineage) agent.NextAction {
	metadata := lineage.Metadata()
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].Metadata == nil {
			nextAction.WorkItems[index].Metadata = domain.Metadata{}
		}
		for key, value := range metadata {
			nextAction.WorkItems[index].Metadata[key] = value
		}
	}
	return nextAction
}

func annotateAgentToolChoice(choice agent.ToolChoice, lineage agentLineage) agent.ToolChoice {
	if choice.Metadata == nil {
		choice.Metadata = domain.Metadata{}
	}
	for key, value := range lineage.Metadata() {
		choice.Metadata[key] = value
	}
	return choice
}

func annotateAgentToolResult(result activities.ExecuteToolResult, lineage agentLineage) activities.ExecuteToolResult {
	if result.Metadata == nil {
		result.Metadata = map[string]string{}
	}
	for key, value := range lineage.Metadata() {
		result.Metadata[key] = value
	}
	return result
}

func compressAgentObservations(ctx workflow.Context, request activities.CompressAgentObservationsRequest) (activities.CompressAgentObservationsResult, error) {
	var result activities.CompressAgentObservationsResult
	err := workflow.ExecuteActivity(ctx, "Activities.CompressAgentObservations", request).Get(ctx, &result)
	return result, err
}

func executeAgentCompletionTool(ctx workflow.Context, toolCtx workflow.Context, persistenceCtx workflow.Context, projectID, workItemID string, event domain.Event, choice agent.ToolChoice, turn int, lineage agentLineage, additionalEvents *[]domain.Event) (activities.ExecuteToolResult, bool, bool, []taskSignalEvent, error) {
	if choice.Metadata == nil {
		choice.Metadata = domain.Metadata{}
	}
	baseToolCallID := firstNonEmpty(choice.ToolCallID, choice.Metadata["tool_call_id"], lineage.ParentToolCallID)
	choice.ToolCallID = fmt.Sprintf("%s:completion:%d", baseToolCallID, turn)
	choice.Metadata["tool_call_id"] = choice.ToolCallID
	choice = annotateAgentToolChoice(choice, lineage)
	result, canceled, interrupted, signalEvents, err := executeToolStep(ctx, toolCtx, persistenceCtx, projectID, workItemID, event, choice, turn, additionalEvents)
	if err != nil {
		return result, canceled, interrupted, signalEvents, err
	}
	return annotateAgentToolResult(result, lineage), canceled, interrupted, signalEvents, nil
}

func executeAgentWorkflowStep(ctx workflow.Context, projectID, workItemID string, event domain.Event, choice agent.ToolChoice, cycle int, additionalEvents *[]domain.Event, completionTool *agent.ToolChoice) (activities.ExecuteToolResult, bool, bool, []taskSignalEvent, error) {
	toolCallID := firstNonEmpty(choice.ToolCallID, choice.Metadata["tool_call_id"])
	workflowID := agentWorkflowID(projectID, workItemID, toolCallID)
	if choice.Metadata == nil {
		choice.Metadata = domain.Metadata{}
	}
	choice.Metadata["agent_workflow_id"] = workflowID
	if goal := agentGoalFromChoice(choice); goal != "" {
		choice.Metadata["agent_goal"] = goal
	}
	childCtx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID: workflowID,
	})
	future := workflow.ExecuteChildWorkflow(childCtx, AgentWorkflowName, AgentWorkflowInput{
		ProjectID:        projectID,
		Event:            event,
		ParentWorkItemID: workItemID,
		ParentToolCallID: toolCallID,
		ParentWorkflowID: workflow.GetInfo(ctx).WorkflowExecution.ID,
		AgentWorkflowID:  workflowID,
		ToolChoice:       choice,
		AdditionalEvents: append([]domain.Event(nil), *additionalEvents...),
		CompletionTool:   completionTool,
	})
	if err := future.GetChildWorkflowExecution().Get(ctx, nil); err != nil {
		return agentWorkflowToolResult(choice, workItemID, cycle, AgentWorkflowResult{}, workflowID, err), false, false, nil, nil
	}

	var run AgentWorkflowResult
	var childErr error
	var signalEvents []taskSignalEvent
	interrupted := false
	canceled := false
	completed := false
	for !completed {
		selector := workflow.NewSelector(ctx)
		selector.AddFuture(future, func(f workflow.Future) {
			childErr = f.Get(ctx, &run)
			completed = true
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskCancel), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "cancel"})
			canceled = true
			signalAgentWorkflow(ctx, workflowID, SignalTaskCancel, signal)
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskInterrupt), func(c workflow.ReceiveChannel, more bool) {
			var signal TaskControlSignal
			c.Receive(ctx, &signal)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "interrupt"})
			if strings.TrimSpace(signal.Event.Body) != "" {
				*additionalEvents = append(*additionalEvents, signal.Event)
			}
			interrupted = true
			signalAgentWorkflow(ctx, workflowID, SignalTaskInterrupt, signal)
		})
		selector.AddReceive(workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext), func(c workflow.ReceiveChannel, more bool) {
			var signal AdditionalContextSignal
			c.Receive(ctx, &signal)
			*additionalEvents = append(*additionalEvents, signal.Event)
			signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event})
			signalAgentWorkflow(ctx, workflowID, SignalTaskAdditionalContext, signal)
		})
		selector.Select(ctx)
	}
	if childErr != nil {
		run = AgentWorkflowResult{Status: domain.ExecutionStatusFailed, Error: childErr.Error(), Message: childErr.Error()}
	}
	if run.Status == domain.ExecutionStatusCanceled {
		canceled = true
	}
	result := agentWorkflowToolResult(choice, workItemID, cycle, run, workflowID, childErr)
	return result, canceled, interrupted, signalEvents, nil
}

func signalAgentWorkflow(ctx workflow.Context, workflowID, signalName string, payload any) {
	workflowID = strings.TrimSpace(workflowID)
	if workflowID == "" {
		return
	}
	if err := workflow.SignalExternalWorkflow(ctx, workflowID, "", signalName, payload).Get(ctx, nil); err != nil {
		workflow.GetLogger(ctx).Warn("signal agent workflow failed", "workflow_id", workflowID, "signal", signalName, "error", err.Error())
	}
}

func drainAgentWorkflowSignals(ctx workflow.Context, additionalEvents *[]domain.Event) ([]taskSignalEvent, bool, bool) {
	var signalEvents []taskSignalEvent
	canceled := false
	interrupted := false
	for {
		var signal AdditionalContextSignal
		if !workflow.GetSignalChannel(ctx, SignalTaskAdditionalContext).ReceiveAsync(&signal) {
			break
		}
		*additionalEvents = append(*additionalEvents, signal.Event)
		signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event})
	}
	for {
		var signal TaskControlSignal
		if !workflow.GetSignalChannel(ctx, SignalTaskCancel).ReceiveAsync(&signal) {
			break
		}
		signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "cancel"})
		canceled = true
	}
	for {
		var signal TaskControlSignal
		if !workflow.GetSignalChannel(ctx, SignalTaskInterrupt).ReceiveAsync(&signal) {
			break
		}
		signalEvents = append(signalEvents, taskSignalEvent{Event: signal.Event, Control: "interrupt"})
		if strings.TrimSpace(signal.Event.Body) != "" {
			*additionalEvents = append(*additionalEvents, signal.Event)
		}
		interrupted = true
	}
	return signalEvents, canceled, interrupted
}

func agentWorkflowResultFromNextAction(next activities.NextActionResult, turn int, toolsUsed []domain.ToolType, filesTouched []string, processes []domain.ProcessReference) AgentWorkflowResult {
	status := domain.ExecutionStatusSucceeded
	if next.Status != activities.NextActionStatusCompleted {
		status = domain.ExecutionStatusFailed
	}
	message := strings.TrimSpace(next.NextAction.ResponseMessage)
	errText := ""
	if status == domain.ExecutionStatusFailed {
		errText = firstNonEmpty(message, "agent finished without completing the task")
	}
	return AgentWorkflowResult{
		Status:       status,
		Message:      message,
		TurnCount:    turn,
		FilesTouched: filesTouched,
		ToolsUsed:    toolsUsed,
		Processes:    processes,
		Error:        errText,
	}
}

func agentWorkflowResultFromCompletion(next activities.NextActionResult, turn int, toolsUsed []domain.ToolType, filesTouched []string, processes []domain.ProcessReference, completion activities.ExecuteToolResult, traceErrors ...error) AgentWorkflowResult {
	run := agentWorkflowResultFromNextAction(next, turn, toolsUsed, filesTouched, processes)
	run.Message = agentCompletionSuccessMessage(completion, run.Message)
	run.Metadata = cloneAgentResultMetadata(completion.Metadata)
	run.Metadata = addAgentTraceErrors(run.Metadata, traceErrors)
	return run
}

func agentCompletionSuccessMessage(result activities.ExecuteToolResult, childMessage string) string {
	publishMessage := strings.TrimSpace(result.Observation)
	childMessage = strings.TrimSpace(childMessage)
	if publishMessage == "" {
		return childMessage
	}
	if childMessage == "" || childMessage == publishMessage {
		return publishMessage
	}
	return publishMessage + "\n\nAuthoring notes:\n" + childMessage
}

func firstAgentTraceError(errs []error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func addAgentTraceErrors(metadata map[string]string, errs []error) map[string]string {
	messages := make([]string, 0, len(errs))
	for _, err := range errs {
		if err == nil {
			continue
		}
		if message := strings.TrimSpace(err.Error()); message != "" {
			messages = append(messages, message)
		}
	}
	if len(messages) == 0 {
		return metadata
	}
	if metadata == nil {
		metadata = map[string]string{}
	}
	metadata["agent_trace_persist_error"] = strings.Join(messages, "\n")
	return metadata
}

func failedAgentWorkflowResult(err error) AgentWorkflowResult {
	return failedAgentWorkflowResultWithState(0, nil, nil, nil, err)
}

func failedAgentWorkflowResultWithState(turn int, toolsUsed []domain.ToolType, filesTouched []string, processes []domain.ProcessReference, err error) AgentWorkflowResult {
	message := "Agent failed."
	errText := ""
	if err != nil {
		errText = err.Error()
		message = errText
	}
	return AgentWorkflowResult{
		Status:       domain.ExecutionStatusFailed,
		Message:      message,
		TurnCount:    turn,
		FilesTouched: filesTouched,
		ToolsUsed:    toolsUsed,
		Processes:    processes,
		Error:        errText,
	}
}

func agentWorkflowToolResult(choice agent.ToolChoice, workItemID string, cycle int, run AgentWorkflowResult, workflowID string, err error) activities.ExecuteToolResult {
	status := run.Status
	if status == "" {
		status = domain.ExecutionStatusSucceeded
	}
	errorMessage := strings.TrimSpace(run.Error)
	if err != nil {
		status = domain.ExecutionStatusFailed
		errorMessage = err.Error()
	}
	resultCode := "0"
	if status != domain.ExecutionStatusSucceeded {
		resultCode = "1"
	}
	metadata := map[string]string{
		"tool_call_id":      firstNonEmpty(choice.ToolCallID, choice.Metadata["tool_call_id"]),
		"work_item_id":      workItemID,
		"execution_cycle":   strconv.Itoa(cycle),
		"agent_workflow_id": strings.TrimSpace(workflowID),
		"agent_turn_count":  strconv.Itoa(run.TurnCount),
	}
	if len(run.ToolsUsed) > 0 {
		metadata["agent_tools_used"] = strings.Join(agentToolTypeStrings(run.ToolsUsed), ",")
	}
	if len(run.FilesTouched) > 0 {
		metadata["agent_files_touched"] = strings.Join(run.FilesTouched, "\n")
	}
	for key, value := range run.Metadata {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if goal := strings.TrimSpace(choice.Metadata["agent_goal"]); goal != "" {
		metadata["agent_goal"] = goal
	}
	return activities.ExecuteToolResult{
		Cycle:           cycle,
		WorkItemID:      workItemID,
		ToolCallID:      firstNonEmpty(choice.ToolCallID, choice.Metadata["tool_call_id"]),
		Tool:            domain.ToolTypeAgent,
		Status:          status,
		RequestedAction: choice.Intent,
		Input:           cloneWorkflowRawMessage(choice.Input),
		Observation:     agentWorkflowObservation(run, err),
		Error:           errorMessage,
		ResultCode:      resultCode,
		Metadata:        metadata,
		Processes:       run.Processes,
	}
}

func agentGoalFromChoice(choice agent.ToolChoice) string {
	var req agenttool.Request
	if err := json.Unmarshal(choice.Input, &req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Goal)
}

func agentWorkflowObservation(run AgentWorkflowResult, err error) string {
	var builder strings.Builder
	if err != nil {
		builder.WriteString("Agent failed.")
		builder.WriteString("\nerror: ")
		builder.WriteString(err.Error())
		return builder.String()
	}
	if run.Status == domain.ExecutionStatusSucceeded {
		builder.WriteString("Agent completed.")
	} else if run.Status == domain.ExecutionStatusCanceled {
		builder.WriteString("Agent canceled.")
	} else {
		builder.WriteString("Agent failed.")
	}
	builder.WriteString("\nturn_count: ")
	builder.WriteString(strconv.Itoa(run.TurnCount))
	if len(run.ToolsUsed) > 0 {
		builder.WriteString("\ntools_used: ")
		builder.WriteString(strings.Join(agentToolTypeStrings(run.ToolsUsed), ", "))
	}
	if len(run.FilesTouched) > 0 {
		builder.WriteString("\nfiles_touched:")
		for _, filePath := range run.FilesTouched {
			builder.WriteString("\n- ")
			builder.WriteString(filePath)
		}
	}
	if errText := strings.TrimSpace(run.Error); errText != "" {
		builder.WriteString("\nerror: ")
		builder.WriteString(errText)
	}
	if message := strings.TrimSpace(run.Message); message != "" {
		builder.WriteString("\n\nmessage:\n")
		builder.WriteString(message)
	}
	return builder.String()
}

func changedFilesFromAgentToolResult(result activities.ExecuteToolResult) []string {
	switch result.Tool {
	case domain.ToolTypeEdit, domain.ToolTypeWrite:
		if filePath := strings.TrimSpace(result.Metadata["file_path"]); filePath != "" {
			return []string{filePath}
		}
	default:
		return nil
	}
	return nil
}

func executionFeedbackFromToolResult(result activities.ExecuteToolResult) agent.ExecutionFeedback {
	metadata := domain.Metadata{}
	for key, value := range result.Metadata {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return agent.ExecutionFeedback{
		Cycle:           result.Cycle,
		WorkItemID:      result.WorkItemID,
		ToolCallID:      result.ToolCallID,
		Tool:            result.Tool,
		Status:          string(result.Status),
		RequestedAction: result.RequestedAction,
		Command:         result.Command,
		Args:            result.Args,
		Input:           cloneWorkflowRawMessage(result.Input),
		Observation:     result.Observation,
		Error:           result.Error,
		Metadata:        metadata,
	}
}

func cloneAgentResultMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(metadata))
	for key, value := range metadata {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			cloned[key] = value
		}
	}
	if len(cloned) == 0 {
		return nil
	}
	return cloned
}

func appendUniqueAgentToolType(values []domain.ToolType, value domain.ToolType) []domain.ToolType {
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func firstAgentWorkItemID(nextAction agent.NextAction) string {
	for _, item := range nextAction.WorkItems {
		if strings.TrimSpace(item.ID) != "" {
			return strings.TrimSpace(item.ID)
		}
	}
	return ""
}

func appendUniqueAgentStrings(values []string, additions ...string) []string {
	for _, addition := range additions {
		addition = strings.TrimSpace(addition)
		if addition == "" {
			continue
		}
		exists := false
		for _, existing := range values {
			if existing == addition {
				exists = true
				break
			}
		}
		if !exists {
			values = append(values, addition)
		}
	}
	sort.Strings(values)
	return values
}

func mergeAgentProcessReferences(target *[]domain.ProcessReference, additions []domain.ProcessReference) {
	if len(additions) == 0 {
		return
	}
	for _, addition := range additions {
		id := strings.TrimSpace(addition.ID)
		if id == "" {
			continue
		}
		replaced := false
		for index := range *target {
			if strings.TrimSpace((*target)[index].ID) == id {
				(*target)[index] = addition
				replaced = true
				break
			}
		}
		if !replaced {
			*target = append(*target, addition)
		}
	}
}

func agentToolTypeStrings(values []domain.ToolType) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			out = append(out, string(value))
		}
	}
	return out
}

func agentCycleLimitResponseMessage(history []agent.ExecutionFeedback) string {
	if len(history) == 0 {
		return "Agent stopped because it reached the maximum number of turns before completing the task."
	}
	last := history[len(history)-1]
	if strings.TrimSpace(last.Error) != "" {
		return "Agent stopped because it reached the maximum number of turns. Last error: " + strings.TrimSpace(last.Error)
	}
	if strings.TrimSpace(last.Observation) != "" {
		return "Agent stopped because it reached the maximum number of turns. Last observation: " + strings.TrimSpace(last.Observation)
	}
	return "Agent stopped because it reached the maximum number of turns before completing the task."
}

func cloneWorkflowRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func agentWorkflowID(projectID, workItemID, toolCallID string) string {
	token := stableWorkflowToken(projectID, workItemID, toolCallID)
	return fmt.Sprintf("%s:agent:%s", strings.TrimSpace(projectID), token)
}

func stableWorkflowToken(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(strings.TrimSpace(part)))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))[:16]
}
