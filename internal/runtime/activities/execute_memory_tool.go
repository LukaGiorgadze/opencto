package activities

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) ExecuteMemoryTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	a.logActivityStep(
		"ExecuteMemoryTool", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("tool_call_id", strings.TrimSpace(request.ToolChoice.ToolCallID)),
	)
	if a.Store == nil || !a.MemoryEnabled {
		return ExecuteToolResult{}, temporal.NewNonRetryableApplicationError("memory store is not configured", "MemoryUnavailable", nil)
	}
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
	}
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
	run, runErr := a.runMemoryTool(ctx, request.ToolChoice, execution)
	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool completed.")
	status := domain.ExecutionStatusSucceeded
	resultCode := "0"
	errorMessage := ""
	if run.Status != "" {
		status = run.Status
	}
	if strings.TrimSpace(run.ResultCode) != "" {
		resultCode = strings.TrimSpace(run.ResultCode)
	}
	if strings.TrimSpace(run.Error) != "" {
		errorMessage = strings.TrimSpace(run.Error)
	}
	if runErr != nil {
		status = domain.ExecutionStatusFailed
		resultCode = "1"
		errorMessage = runErr.Error()
		attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool failed.")
	} else if status == domain.ExecutionStatusFailed {
		attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool failed.")
	}
	attempt.Status = status
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
	for key, value := range run.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	attempt.Metadata = metadata
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   request.ToolChoice.WorkingDir,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		InputPayload:       cloneRawMessage(request.ToolChoice.Input),
		OutputSummary:      attempt.OutputSummary,
		OutputPayload:      cloneRawMessage(run.Payload),
		ResultCode:         resultCode,
		ErrorDetails:       errorMessage,
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}
	result := ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           status,
		RequestedAction:  request.ToolChoice.Intent,
		Input:            cloneRawMessage(request.ToolChoice.Input),
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       invocation.ResultCode,
		Metadata:         metadata,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = firstRawMessage(run.Payload, executeToolResultPayload(result))
	a.logActivityStep(
		"ExecuteMemoryTool", "done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("status", string(status)),
	)
	return result, runErr
}
