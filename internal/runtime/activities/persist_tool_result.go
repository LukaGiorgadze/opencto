package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) PersistToolResult(ctx context.Context, request PersistToolResultRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	result := request.Result
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(result.ExecutionAttempt.ProjectID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(result.ToolInvocation.ProjectID)
	}
	records, err := toolPersistenceRecords(event, result)
	if err != nil {
		return err
	}
	a.logActivityStep(
		"PersistToolResult", "begin",
		slog.String("project_id", records.Attempt.ProjectID),
		slog.String("work_item_id", records.Attempt.WorkItemID),
		slog.String("tool_call_id", result.ToolCallID),
		slog.String("tool_type", string(result.Tool)),
	)
	if err := a.Store.UpsertExecutionAttempt(ctx, records.Attempt); err != nil {
		return err
	}
	if err := a.Store.UpsertToolInvocation(ctx, records.Invocation); err != nil {
		return err
	}
	if !request.SkipConversation && strings.TrimSpace(records.Conversation.Body) != "" {
		if err := a.Store.UpsertConversationMessage(ctx, records.Conversation); err != nil {
			return err
		}
	}
	a.logActivityStep(
		"PersistToolResult", "done",
		slog.String("project_id", records.Attempt.ProjectID),
		slog.String("work_item_id", records.Attempt.WorkItemID),
		slog.String("tool_call_id", result.ToolCallID),
		slog.String("tool_type", string(result.Tool)),
	)
	return nil
}

type toolPersistenceRecordSet struct {
	Attempt      domain.ExecutionAttempt
	Invocation   domain.ToolInvocation
	Conversation domain.ConversationMessage
}

func toolPersistenceRecords(event domain.Event, result ExecuteToolResult) (toolPersistenceRecordSet, error) {
	projectID := firstNonEmpty(event.ProjectID, result.ExecutionAttempt.ProjectID, result.ToolInvocation.ProjectID)
	if projectID == "" {
		return toolPersistenceRecordSet{}, fmt.Errorf("project_id is required for tool persistence")
	}
	workItemID := firstNonEmpty(result.WorkItemID, result.ExecutionAttempt.WorkItemID)
	toolCallID := firstNonEmpty(result.ToolCallID, result.ToolInvocation.Metadata["tool_call_id"], result.ExecutionAttempt.Metadata["tool_call_id"])
	if toolCallID == "" {
		return toolPersistenceRecordSet{}, fmt.Errorf("tool_call_id is required for tool persistence")
	}
	now := time.Now().UTC()
	attempt := result.ExecutionAttempt
	if strings.TrimSpace(attempt.ID) == "" {
		startedAt := now
		if value := strings.TrimSpace(result.Metadata["started_at"]); value != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				startedAt = parsed
			}
		}
		completedAt := now
		attempt = domain.ExecutionAttempt{
			ID:            stableActivityID("execution-attempt", projectID, workItemID, toolCallID),
			ProjectID:     projectID,
			WorkItemID:    workItemID,
			Status:        result.Status,
			Attempt:       result.Cycle,
			Tool:          result.Tool,
			Summary:       result.RequestedAction,
			OutputSummary: firstNonEmpty(result.Observation, result.Error),
			Metadata:      cloneMetadata(result.Metadata),
			StartedAt:     startedAt,
			CompletedAt:   &completedAt,
		}
	}
	if attempt.CompletedAt == nil && result.Status != domain.ExecutionStatusRunning {
		completedAt := now
		attempt.CompletedAt = &completedAt
	}
	if attempt.Metadata == nil {
		attempt.Metadata = cloneMetadata(result.Metadata)
	}
	invocation := result.ToolInvocation
	if strings.TrimSpace(invocation.ID) == "" {
		invocation = domain.ToolInvocation{
			ID:                 stableActivityID("tool-invocation", projectID, workItemID, toolCallID),
			ProjectID:          projectID,
			ExecutionAttemptID: attempt.ID,
			RequestedIntent:    result.RequestedAction,
			ChosenTool:         result.Tool,
			WorkingDirectory:   result.WorkingDirectory,
			InputPayload:       cloneRawMessage(result.Input),
			OutputSummary:      firstNonEmpty(result.Observation, result.Error),
			OutputPayload:      executeToolResultPayload(result),
			ResultCode:         result.ResultCode,
			ErrorDetails:       result.Error,
			Metadata:           cloneMetadata(result.Metadata),
			CreatedAt:          firstNonZeroTime(attempt.StartedAt, now),
			CompletedAt:        attempt.CompletedAt,
		}
	}
	if len(strings.TrimSpace(string(invocation.InputPayload))) == 0 {
		invocation.InputPayload = cloneRawMessage(result.Input)
	}
	if len(strings.TrimSpace(string(invocation.OutputPayload))) == 0 {
		invocation.OutputPayload = executeToolResultPayload(result)
	}
	if invocation.Metadata == nil {
		invocation.Metadata = cloneMetadata(result.Metadata)
	}
	if invocation.Metadata == nil {
		invocation.Metadata = domain.Metadata{}
	}
	invocation.Metadata["tool_call_id"] = toolCallID
	conversation := domain.ConversationMessage{
		ID:          stableActivityID("conversation-tool", projectID, event.ID, toolCallID),
		ProjectID:   projectID,
		EventID:     event.ID,
		Role:        domain.ConversationRoleTool,
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		Body:        toolConversationBody(result),
		ToolCallID:  toolCallID,
		Metadata:    toolConversationMetadata(result),
		CreatedAt:   firstNonZeroTime(timeFromMetadata(result.Metadata, "completed_at"), now),
	}
	return toolPersistenceRecordSet{Attempt: attempt, Invocation: invocation, Conversation: conversation}, nil
}

func toolConversationMetadata(result ExecuteToolResult) domain.Metadata {
	metadata := domain.Metadata{}
	for key, value := range result.Metadata {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		metadata[key] = value
	}
	metadata["tool"] = string(result.Tool)
	metadata["status"] = string(result.Status)
	if code := strings.TrimSpace(result.ResultCode); code != "" {
		metadata["result_code"] = code
	}
	return metadata
}

func toolConversationBody(result ExecuteToolResult) string {
	var parts []string
	if result.RequestedAction != "" {
		parts = append(parts, "requested_action: "+result.RequestedAction)
	}
	if result.Observation != "" {
		parts = append(parts, "observation:\n"+result.Observation)
	}
	if result.Error != "" {
		parts = append(parts, "error:\n"+result.Error)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func executeToolResultPayload(result ExecuteToolResult) json.RawMessage {
	payload := struct {
		Cycle            int                       `json:"cycle"`
		WorkItemID       string                    `json:"work_item_id,omitempty"`
		ToolCallID       string                    `json:"tool_call_id,omitempty"`
		Tool             domain.ToolType           `json:"tool,omitempty"`
		Status           domain.ExecutionStatus    `json:"status"`
		RequestedAction  string                    `json:"requested_action,omitempty"`
		Command          string                    `json:"command,omitempty"`
		Args             []string                  `json:"args,omitempty"`
		Input            json.RawMessage           `json:"input,omitempty"`
		Observation      string                    `json:"observation,omitempty"`
		Error            string                    `json:"error,omitempty"`
		WorkingDirectory string                    `json:"working_directory,omitempty"`
		ResultCode       string                    `json:"result_code,omitempty"`
		Metadata         map[string]string         `json:"metadata,omitempty"`
		Processes        []domain.ProcessReference `json:"processes,omitempty"`
	}{
		Cycle:            result.Cycle,
		WorkItemID:       result.WorkItemID,
		ToolCallID:       result.ToolCallID,
		Tool:             result.Tool,
		Status:           result.Status,
		RequestedAction:  result.RequestedAction,
		Command:          result.Command,
		Args:             result.Args,
		Input:            cloneRawMessage(result.Input),
		Observation:      result.Observation,
		Error:            result.Error,
		WorkingDirectory: result.WorkingDirectory,
		ResultCode:       result.ResultCode,
		Metadata:         result.Metadata,
		Processes:        result.Processes,
	}
	return mustJSON(payload)
}
