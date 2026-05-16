package activities

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	exectool "github.com/opencto/opencto/internal/tools/exec"
)

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	if request.ToolChoice.Type == domain.ToolTypeExec && request.ToolChoice.RunMode == domain.ToolRunModeStartBackground {
		return a.startExecProcess(ctx, request)
	}
	a.logActivityStep(
		"ExecuteTool", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("tool_call_id", strings.TrimSpace(request.ToolChoice.ToolCallID)),
		slog.String("command", request.ToolChoice.Command),
		slog.Any("args", request.ToolChoice.Args),
		slog.Int("input_bytes", len(request.ToolChoice.Input)),
	)
	execution, err := newToolExecutionContext(request)
	if err != nil {
		a.logActivityStep(
			"ExecuteTool", "new_execution_context_error",
			slog.String("project_id", strings.TrimSpace(request.ProjectID)),
			slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
			slog.String("error", err.Error()),
		)
		return ExecuteToolResult{}, err
	}
	a.logActivityStep(
		"ExecuteTool", "new_execution_context_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.Int("cycle", execution.Cycle),
		slog.Duration("timeout", execution.Timeout),
		slog.Any("fallback_candidates", execution.FallbackCandidates),
	)
	stopHeartbeat := a.startToolActivityHeartbeat(ctx, request.ToolChoice, execution)
	defer stopHeartbeat()

	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       request.ToolChoice.Type,
		Summary:    request.ToolChoice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
		},
	}

	a.logActivityStep(
		"ExecuteTool", "tool_run_begin",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
	)
	toolResult, runErr := a.runChosenTool(ctx, request.ToolChoice, execution)
	a.logActivityStep(
		"ExecuteTool", "tool_run_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("result_code", toolResult.ResultCode),
		slog.Bool("tool_error", runErr != nil),
	)

	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	metadata := map[string]string{
		"started_at":   execution.StartedAt.UTC().Format(time.RFC3339Nano),
		"completed_at": completedAt.UTC().Format(time.RFC3339Nano),
		"tool_call_id": execution.ToolCallID,
	}
	for key, value := range request.ToolChoice.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	for key, value := range toolResult.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if request.ToolChoice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(request.ToolChoice.TimeoutMs)
	}
	if request.ToolChoice.Destructive {
		metadata["destructive"] = "true"
	}
	resultInput := cloneRawMessage(request.ToolChoice.Input)
	if len(strings.TrimSpace(string(toolResult.Input))) > 0 {
		resultInput = cloneRawMessage(toolResult.Input)
	}
	resultStatus := domain.ExecutionStatusSucceeded
	var errorMessage string
	if runErr != nil {
		resultStatus = domain.ExecutionStatusFailed
		errorMessage = runErr.Error()
		toolResult.Observation = firstNonEmpty(toolResult.Observation, "Tool execution failed.")
	} else {
		toolResult.Observation = firstNonEmpty(toolResult.Observation, "Execution completed.")
	}
	toolResult, metadata = a.applyToolResultPostProcessors(ctx, request.ToolChoice, execution, resultInput, resultStatus, errorMessage, toolResult, metadata)

	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   firstNonEmpty(toolResult.WorkingDirectory, request.ToolChoice.WorkingDir),
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		InputPayload:       cloneRawMessage(resultInput),
		OutputSummary:      toolResult.Observation,
		ResultCode:         firstNonEmpty(toolResult.ResultCode, "0"),
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}

	if runErr != nil {
		a.logActivityStep(
			"ExecuteTool", "tool_run_result_error",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("error", runErr.Error()),
		)
		attempt.Status = domain.ExecutionStatusFailed
		attempt.OutputSummary = toolResult.Observation
		invocation.ErrorDetails = runErr.Error()
		invocation.OutputSummary = attempt.OutputSummary
		if invocation.ResultCode == "" || invocation.ResultCode == "0" {
			invocation.ResultCode = "1"
		}
	} else {
		a.logActivityStep(
			"ExecuteTool", "tool_run_result_success",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
		)
		attempt.Status = domain.ExecutionStatusSucceeded
		attempt.OutputSummary = toolResult.Observation
		invocation.OutputSummary = attempt.OutputSummary
	}
	a.logActivityStep(
		"ExecuteTool", "done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("attempt_status", string(attempt.Status)),
		slog.String("result_code", invocation.ResultCode),
	)

	result := ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           attempt.Status,
		RequestedAction:  request.ToolChoice.Intent,
		Command:          request.ToolChoice.Command,
		Args:             request.ToolChoice.Args,
		Input:            resultInput,
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       invocation.ResultCode,
		Metadata:         invocation.Metadata,
		Processes:        toolResult.Processes,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = executeToolResultPayload(result)
	return result, nil
}

func (a *Activities) startExecProcess(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, err
	}
	choice := request.ToolChoice
	if choice.Type != domain.ToolTypeExec {
		return ExecuteToolResult{}, fmt.Errorf("start background process requires exec tool, got %q", choice.Type)
	}
	processScope := toolProcessScope(choice.ProcessScope)
	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       choice.Type,
		Summary:    choice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
			"run_mode":        string(domain.ToolRunModeStartBackground),
			"process_scope":   string(processScope),
		},
	}
	processID := stableActivityID("managed-process", execution.ProjectID, execution.WorkItemID, execution.ToolCallID)
	stateDir := a.runtimeStateDir()
	manager := exectool.NewProcessManager(a.activityLogger())
	process, runErr := manager.Start(ctx, exectool.StartProcessRequest{
		ProcessID:    processID,
		ProjectID:    execution.ProjectID,
		WorkItemID:   execution.WorkItemID,
		ToolCallID:   execution.ToolCallID,
		Intent:       choice.Intent,
		ProcessScope: processScope,
		Command:      choice.Command,
		Args:         choice.Args,
		WorkingDir:   resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		StateDir:     stateDir,
		Timeout:      execution.Timeout,
		Environment:  workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
	})
	metadata := map[string]string{
		"tool_call_id":                  execution.ToolCallID,
		"work_item_id":                  execution.WorkItemID,
		"execution_cycle":               strconv.Itoa(execution.Cycle),
		"run_mode":                      string(domain.ToolRunModeStartBackground),
		"idempotency":                   string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":                 string(processScope),
		"process_id":                    processID,
		"possible_long_running_process": "true",
	}
	if choice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
	if process.PID > 0 {
		metadata["pid"] = strconv.Itoa(process.PID)
	}
	if process.PGID > 0 {
		metadata["pgid"] = strconv.Itoa(process.PGID)
	}
	if process.StdoutLogPath != "" {
		metadata["stdout_log_path"] = process.StdoutLogPath
	}
	if process.StderrLogPath != "" {
		metadata["stderr_log_path"] = process.StderrLogPath
	}
	status := domain.ExecutionStatusSucceeded
	resultCode := "0"
	observation := processStartObservation(process)
	var errorMessage string
	if runErr != nil {
		status = domain.ExecutionStatusFailed
		resultCode = "1"
		errorMessage = runErr.Error()
		observation = backgroundStartFailureObservation(ctx, manager, stateDir, process, runErr)
	}
	toolResult := toolRunResult{
		Observation:      firstNonEmpty(observation, "Execution completed."),
		ResultCode:       resultCode,
		Input:            cloneRawMessage(choice.Input),
		WorkingDirectory: process.WorkingDirectory,
		Metadata:         metadata,
	}
	if status == domain.ExecutionStatusFailed {
		toolResult.Observation = firstNonEmpty(observation, "Tool execution failed.")
	}
	toolResult, metadata = a.applyToolResultPostProcessors(ctx, choice, execution, choice.Input, status, errorMessage, toolResult, metadata)
	observation = toolResult.Observation

	completedAt := time.Now().UTC()
	attempt.Status = status
	attempt.OutputSummary = observation
	attempt.CompletedAt = &completedAt
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    choice.Intent,
		ChosenTool:         choice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   process.WorkingDirectory,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       choice.InputSummary,
		InputPayload:       cloneRawMessage(choice.Input),
		OutputSummary:      observation,
		ResultCode:         resultCode,
		ErrorDetails:       errorMessage,
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}
	processes := []domain.ProcessReference(nil)
	if status == domain.ExecutionStatusSucceeded && strings.TrimSpace(process.ID) != "" {
		processes = []domain.ProcessReference{{
			ID:          process.ID,
			Description: firstNonEmpty(choice.Intent, choice.Command),
			Status:      process.Status,
			Scope:       processScope,
		}}
	}
	result := ExecuteToolResult{
		Cycle:            execution.Cycle,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             choice.Type,
		Status:           status,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Input:            cloneRawMessage(choice.Input),
		Observation:      observation,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       resultCode,
		Metadata:         metadata,
		Processes:        processes,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = executeToolResultPayload(result)
	return result, nil
}
