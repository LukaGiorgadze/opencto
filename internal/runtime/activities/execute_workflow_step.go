package activities

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/temporal"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func (a *Activities) ExecuteWorkflowStep(ctx context.Context, request workflowrun.ExecuteStepRequest) (workflowrun.ExecuteStepResult, error) {
	stepID, err := workflowbundle.NormalizeStepID(request.Step.ID)
	if err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	runPath := strings.TrimSpace(request.RunPath)
	if runPath == "" {
		return workflowrun.ExecuteStepResult{}, fmt.Errorf("run_path is required")
	}
	if err := os.MkdirAll(workflowRunArtifactsDir(runPath), 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	dataDir, err := workflowbundle.WorkflowDataDir(a.WorkspaceRoot, request.WorkflowID)
	if err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(workflowStepOutputPath(runPath, stepID)), 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	startedAt := time.Now().UTC()
	attempt := activityAttempt(ctx)
	stdoutPath, stderrPath := workflowStepAttemptLogPaths(a.runtimeStateDir(), request.WorkflowID, request.RunID, stepID, attempt)
	if err := os.MkdirAll(filepath.Dir(stdoutPath), 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	stepRunID := stableActivityID("scheduled-workflow-step", request.RunID, stepID, strconv.Itoa(attempt))
	if a.Store != nil {
		_ = a.Store.UpsertScheduledWorkflowStepRun(ctx, domain.ScheduledWorkflowStepRun{
			ID:            stepRunID,
			ProjectID:     request.ProjectID,
			RunID:         request.RunID,
			WorkflowID:    request.WorkflowID,
			StepID:        stepID,
			Status:        domain.ExecutionStatusRunning,
			Attempt:       attempt,
			Command:       request.Step.Command,
			Args:          append([]string(nil), request.Step.Args...),
			StartedAt:     startedAt,
			StdoutLogPath: stdoutPath,
			StderrLogPath: stderrPath,
		})
	}

	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = stdoutFile.Close()
		return workflowrun.ExecuteStepResult{}, err
	}

	env, err := workflowStepEnvironment(a.WorkspaceRoot, request)
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return workflowrun.ExecuteStepResult{}, err
	}
	cmd := osexec.CommandContext(ctx, request.Step.Command, request.Step.Args...)
	cmd.Dir = runPath
	cmd.Env = env
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	err = cmd.Run()
	stdoutCloseErr := stdoutFile.Close()
	stderrCloseErr := stderrFile.Close()
	if err == nil {
		switch {
		case stdoutCloseErr != nil:
			err = stdoutCloseErr
		case stderrCloseErr != nil:
			err = stderrCloseErr
		}
	}
	completedAt := time.Now().UTC()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	stdoutTail, stdoutTruncated := tailWorkflowLog(stdoutPath, a.execTailBytes())
	stderrTail, stderrTruncated := tailWorkflowLog(stderrPath, a.execTailBytes())
	summary := workflowStepSummary(stdoutTail, stderrTail, stdoutTruncated, stderrTruncated)
	status := domain.ExecutionStatusSucceeded
	errorDetails := ""
	if err != nil {
		status = domain.ExecutionStatusFailed
		errorDetails = err.Error()
	}
	if a.Store != nil {
		_ = a.Store.UpsertScheduledWorkflowStepRun(ctx, domain.ScheduledWorkflowStepRun{
			ID:            stepRunID,
			ProjectID:     request.ProjectID,
			RunID:         request.RunID,
			WorkflowID:    request.WorkflowID,
			StepID:        stepID,
			Status:        status,
			Attempt:       attempt,
			Command:       request.Step.Command,
			Args:          append([]string(nil), request.Step.Args...),
			StartedAt:     startedAt,
			CompletedAt:   &completedAt,
			ExitCode:      exitCode,
			StdoutLogPath: stdoutPath,
			StderrLogPath: stderrPath,
			OutputSummary: summary,
			ErrorDetails:  errorDetails,
			Metadata: domain.Metadata{
				"commit_hash": strings.TrimSpace(request.CommitHash),
			},
		})
	}
	result := workflowrun.ExecuteStepResult{
		StepID:        stepID,
		ExitCode:      exitCode,
		StdoutLogPath: stdoutPath,
		StderrLogPath: stderrPath,
		OutputSummary: summary,
	}
	if err != nil {
		failure := workflowrun.StepFailure{
			StepID:        stepID,
			Command:       request.Step.Command,
			Args:          append([]string(nil), request.Step.Args...),
			ExitCode:      exitCode,
			Error:         err.Error(),
			OutputSummary: summary,
			StdoutLogPath: stdoutPath,
			StderrLogPath: stderrPath,
		}
		return result, temporal.NewApplicationError(workflowStepFailureMessage(failure), workflowrun.StepFailureErrorType, failure)
	}
	return result, nil
}

func workflowStepFailureMessage(failure workflowrun.StepFailure) string {
	var builder strings.Builder
	stepID := strings.TrimSpace(failure.StepID)
	if stepID == "" {
		stepID = "unknown"
	}
	fmt.Fprintf(&builder, "workflow step %q failed", stepID)
	if failure.ExitCode != 0 {
		fmt.Fprintf(&builder, " with exit code %d", failure.ExitCode)
	}
	command := strings.TrimSpace(strings.Join(append([]string{failure.Command}, failure.Args...), " "))
	if command != "" {
		builder.WriteString("\ncommand: ")
		builder.WriteString(command)
	}
	if err := strings.TrimSpace(failure.Error); err != "" {
		builder.WriteString("\nerror: ")
		builder.WriteString(err)
	}
	if output := strings.TrimSpace(failure.OutputSummary); output != "" {
		builder.WriteString("\n")
		builder.WriteString(output)
	}
	if stderrPath := strings.TrimSpace(failure.StderrLogPath); stderrPath != "" {
		builder.WriteString("\nstderr_log: ")
		builder.WriteString(stderrPath)
	}
	if stdoutPath := strings.TrimSpace(failure.StdoutLogPath); stdoutPath != "" {
		builder.WriteString("\nstdout_log: ")
		builder.WriteString(stdoutPath)
	}
	return builder.String()
}
