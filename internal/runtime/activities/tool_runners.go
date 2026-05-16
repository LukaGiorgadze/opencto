package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	"github.com/opencto/opencto/internal/tools/postprocess"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

func (a *Activities) runChosenTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeExec:
		return a.runExecTool(ctx, choice, execution)
	case domain.ToolTypeRead:
		return a.runReadTool(ctx, choice)
	case domain.ToolTypeEdit:
		return a.runEditTool(ctx, choice)
	case domain.ToolTypeWrite:
		return a.runWriteTool(ctx, choice, execution)
	case domain.ToolTypeGlob:
		return a.runGlobTool(ctx, choice, execution)
	case domain.ToolTypeGrep:
		return a.runGrepTool(ctx, choice, execution)
	case domain.ToolTypeWorkflowCreate, domain.ToolTypeWorkflowUpdate, domain.ToolTypeWorkflowDelete, domain.ToolTypeWorkflowOperation:
		return a.runWorkflowTool(ctx, choice, execution)
	case domain.ToolTypeSkill:
		return a.runSkillTool(ctx, choice)
	default:
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("unsupported tool type %q", choice.Type)
	}
}

func toolTypeInList(toolType domain.ToolType, values []domain.ToolType) bool {
	for _, value := range values {
		if value == toolType {
			return true
		}
	}
	return false
}

func (a *Activities) applyToolResultPostProcessors(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext, input json.RawMessage, status domain.ExecutionStatus, errorMessage string, result toolRunResult, metadata map[string]string) (toolRunResult, map[string]string) {
	processors := a.ToolResultProcessors
	if processors == nil {
		processors = toolregistry.ToolResultProcessors()
	}
	resultCode := firstNonEmpty(result.ResultCode, "0")
	if status == domain.ExecutionStatusFailed && resultCode == "0" {
		resultCode = "1"
	}
	processed := postprocess.Apply(ctx, processors, postprocess.Request{
		ProjectID:        execution.ProjectID,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		WorkspaceRoot:    a.WorkspaceRoot,
		Tool:             choice.Type,
		Status:           status,
		Input:            cloneRawMessage(input),
		Error:            errorMessage,
		ResultCode:       resultCode,
		WorkingDirectory: firstNonEmpty(result.WorkingDirectory, choice.WorkingDir),
	}, postprocess.Result{
		Observation: result.Observation,
		Metadata:    metadata,
	})
	result.Observation = processed.Observation
	return result, processed.Metadata
}

func (a *Activities) runExecTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	if a.Exec == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("exec executor is not configured")
	}
	req := exectool.Request{
		ProjectID:          execution.ProjectID,
		WorkItemID:         execution.WorkItemID,
		ToolCallID:         execution.ToolCallID,
		ProcessID:          stableActivityID("managed-process", execution.ProjectID, execution.WorkItemID, execution.ToolCallID),
		Intent:             choice.Intent,
		Command:            choice.Command,
		Args:               choice.Args,
		WorkingDir:         resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		StateDir:           a.runtimeStateDir(),
		Timeout:            execution.Timeout,
		GracePeriod:        a.execGrace(execution.Timeout),
		TailBytes:          a.execTailBytes(),
		ProcessScope:       toolProcessScope(choice.ProcessScope),
		Environment:        workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
		FallbackCandidates: execution.FallbackCandidates,
	}
	result, err := a.Exec.Run(ctx, req)
	metadata := map[string]string{
		"exec_exit_status": strconv.Itoa(result.ExitCode),
		"run_mode":         string(firstNonEmpty(string(choice.RunMode), string(domain.ToolRunModeWaitForExit))),
		"idempotency":      string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":    string(toolProcessScope(choice.ProcessScope)),
	}
	if result.StdoutLogPath != "" {
		metadata["stdout_log_path"] = result.StdoutLogPath
	}
	if result.StderrLogPath != "" {
		metadata["stderr_log_path"] = result.StderrLogPath
	}
	if result.StdoutTruncated {
		metadata["stdout_truncated"] = "true"
	}
	if result.StderrTruncated {
		metadata["stderr_truncated"] = "true"
	}
	resultCode := strconv.Itoa(result.ExitCode)
	if errors.Is(err, context.DeadlineExceeded) {
		resultCode = "timeout"
		metadata["possible_long_running_process"] = "true"
		metadata["timeout"] = "true"
	}
	var processes []domain.ProcessReference
	if result.ManagedProcess != nil {
		process := *result.ManagedProcess
		metadata["process_id"] = process.ID
		metadata["possible_long_running_process"] = "true"
		metadata["promoted_to_managed_process"] = "true"
		if process.PID > 0 {
			metadata["pid"] = strconv.Itoa(process.PID)
		}
		if process.PGID > 0 {
			metadata["pgid"] = strconv.Itoa(process.PGID)
		}
		processes = []domain.ProcessReference{{
			ID:          process.ID,
			Description: firstNonEmpty(choice.Intent, choice.Command),
			Status:      process.Status,
			Scope:       toolProcessScope(choice.ProcessScope),
		}}
	}
	if !result.StartedAt.IsZero() {
		metadata["tool_started_at"] = result.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !result.CompletedAt.IsZero() {
		metadata["tool_completed_at"] = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      execObservation(result, err),
		ResultCode:       resultCode,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
		Processes:        processes,
	}, err
}

func execObservation(result exectool.Result, err error) string {
	observation := fullObservation(result.Stdout, result.Stderr, err)
	var notes []string
	if result.ManagedProcess != nil {
		notes = append(
			notes,
			"status: running",
			"process_id: "+result.ManagedProcess.ID,
			"possible_long_running_process: true",
		)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		notes = append(notes, "output_truncated: true")
	}
	if result.StdoutLogPath != "" {
		notes = append(notes, "stdout_log_path: "+result.StdoutLogPath)
	}
	if result.StderrLogPath != "" {
		notes = append(notes, "stderr_log_path: "+result.StderrLogPath)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		notes = append(notes, "result_code: timeout", "possible_long_running_process: true")
	}
	if len(notes) == 0 {
		return observation
	}
	return observation + "\n\n" + strings.Join(notes, "\n")
}

func (a *Activities) runReadTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req readtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Read
	if executor == nil {
		executor = readtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"file_path":   result.FilePath,
		"lines_read":  strconv.Itoa(result.LinesRead),
		"total_lines": strconv.Itoa(result.TotalLines),
		"bytes_read":  strconv.Itoa(result.BytesRead),
		"truncated":   strconv.FormatBool(result.Truncated),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	return toolRunResult{
		Observation: readObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runEditTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req edittool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Edit
	if executor == nil {
		executor = edittool.NewTool()
	}
	result, err := executor.Run(ctx, req)
	observation := editObservation(result, err)
	metadata := map[string]string{
		"file_path":     result.FilePath,
		"replacements":  strconv.Itoa(result.Replacements),
		"bytes_written": strconv.Itoa(result.BytesWritten),
	}
	return toolRunResult{
		Observation: observation,
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runWriteTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req writetool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent

	executor := a.Write
	if executor == nil {
		executor = writetool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"file_path":     result.FilePath,
		"bytes_written": strconv.Itoa(result.BytesWritten),
		"overwritten":   strconv.FormatBool(result.Overwritten),
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	observation := writeObservation(result, err)
	return toolRunResult{
		Observation: observation,
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runGlobTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req globtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.Cwd = resolveRelativeToolPath(firstNonEmpty(req.Cwd, choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot)
	req.Path = resolveRelativeToolPath(req.Path, req.Cwd)
	for index := range req.Actions {
		req.Actions[index].Cwd = resolveRelativeToolPath(firstNonEmpty(req.Actions[index].Cwd, req.Cwd), a.WorkspaceRoot)
		req.Actions[index].Path = resolveRelativeToolPath(req.Actions[index].Path, req.Actions[index].Cwd)
	}
	req.Timeout = execution.Timeout

	executor := a.Glob
	if executor == nil {
		executor = globtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"pattern":     result.Pattern,
		"path":        result.Root,
		"cwd":         req.Cwd,
		"match_count": strconv.Itoa(len(result.Matches)),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      globObservation(result, err),
		ResultCode:       resultCodeForError(err),
		WorkingDirectory: req.Cwd,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runGrepTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req greptool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.WorkingDir = firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot)
	req.Timeout = execution.Timeout
	req.FallbackCandidates = execution.FallbackCandidates

	executor := a.Grep
	if executor == nil {
		executor = greptool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	code := strconv.Itoa(result.ExitCode)
	if err != nil && result.ExitCode == 0 && result.StartedAt.IsZero() {
		code = "1"
	}
	metadata := map[string]string{
		"grep_exit_status": strconv.Itoa(result.ExitCode),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      grepObservation(result, err),
		ResultCode:       code,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runWorkflowTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	executor := a.Schedule
	if executor == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("workflow schedule executor is not configured")
	}
	var result scheduletool.Result
	var err error
	switch choice.Type {
	case domain.ToolTypeWorkflowCreate:
		var req scheduletool.CreateRequest
		if decodeErr := decodeChoiceInput(choice, &req); decodeErr != nil {
			return toolRunResult{ResultCode: "1"}, decodeErr
		}
		req.ProjectID = execution.ProjectID
		req.WorkItemID = execution.WorkItemID
		req.ToolCallID = execution.ToolCallID
		req.Intent = choice.Intent
		req.SourceEvent = execution.SourceEvent
		result, err = executor.Create(ctx, req)
	case domain.ToolTypeWorkflowUpdate:
		var req scheduletool.UpdateRequest
		if decodeErr := decodeChoiceInput(choice, &req); decodeErr != nil {
			return toolRunResult{ResultCode: "1"}, decodeErr
		}
		req.ProjectID = execution.ProjectID
		req.WorkItemID = execution.WorkItemID
		req.ToolCallID = execution.ToolCallID
		req.Intent = choice.Intent
		req.SourceEvent = execution.SourceEvent
		result, err = executor.Update(ctx, req)
	case domain.ToolTypeWorkflowDelete:
		var req scheduletool.DeleteRequest
		if decodeErr := decodeChoiceInput(choice, &req); decodeErr != nil {
			return toolRunResult{ResultCode: "1"}, decodeErr
		}
		req.ProjectID = execution.ProjectID
		req.WorkItemID = execution.WorkItemID
		req.ToolCallID = execution.ToolCallID
		req.Intent = choice.Intent
		req.SourceEvent = execution.SourceEvent
		result, err = executor.Delete(ctx, req)
	case domain.ToolTypeWorkflowOperation:
		var req scheduletool.OperationRequest
		if decodeErr := decodeChoiceInput(choice, &req); decodeErr != nil {
			return toolRunResult{ResultCode: "1"}, decodeErr
		}
		req.ProjectID = execution.ProjectID
		req.WorkItemID = execution.WorkItemID
		req.ToolCallID = execution.ToolCallID
		req.Intent = choice.Intent
		req.SourceEvent = execution.SourceEvent
		result, err = executor.Operation(ctx, req)
	default:
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("unsupported workflow tool type %q", choice.Type)
	}
	metadata := map[string]string{
		"workflow_schedule_operation": result.Operation,
		"workflow_id":                 result.WorkflowID,
		"schedule_id":                 result.ScheduleID,
		"workflow_name":               result.Name,
		"workflow_description":        result.Description,
		"workflow_commit_hash":        result.CommitHash,
		"workflow_path":               result.WorkflowPath,
		"schedule_time_zone":          result.TimeZone,
	}
	if result.OneShotAt != "" {
		metadata["one_shot_at"] = result.OneShotAt
	}
	if result.Cron != "" {
		metadata["cron"] = result.Cron
	}
	if len(result.NextActionTimes) > 0 {
		metadata["next_action_times"] = strings.Join(result.NextActionTimes, "\n")
	}
	return toolRunResult{
		Observation: result.Observation(),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runSkillTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req skilltool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Skill
	if executor == nil {
		executor = skilltool.NewSafeExecutor(a.skillsRoots()...)
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: skillObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"skill_id":   result.SkillID,
			"skill_path": result.Path,
			"bytes_read": strconv.Itoa(result.BytesRead),
		},
	}, err
}

func decodeChoiceInput(choice agent.ToolChoice, target any) error {
	if len(strings.TrimSpace(string(choice.Input))) == 0 {
		return fmt.Errorf("%s tool input is required", choice.Type)
	}
	decoder := json.NewDecoder(strings.NewReader(string(choice.Input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return fmt.Errorf("tool input contains multiple JSON values")
	case io.EOF:
		return nil
	default:
		return err
	}
}
