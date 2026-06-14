package activities

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.temporal.io/sdk/activity"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func activityAttempt(ctx context.Context) int {
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		if info.Attempt > 0 {
			return int(info.Attempt)
		}
	}
	return 1
}

func workflowStepAttemptLogPaths(stateDir, workflowID, runID, stepID string, attempt int) (string, string) {
	if attempt <= 0 {
		attempt = 1
	}
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		stateDir = os.TempDir()
	}
	attemptDir := filepath.Join(workflowRunLogDir(stateDir, workflowID, runID), strings.TrimSpace(stepID), fmt.Sprintf("attempt-%d", attempt))
	return filepath.Join(attemptDir, "stdout.log"), filepath.Join(attemptDir, "stderr.log")
}

func workflowRunLogDir(stateDir, workflowID, runID string) string {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" {
		stateDir = os.TempDir()
	}
	return filepath.Join(stateDir, "workflow-logs", strings.TrimSpace(workflowID), strings.TrimSpace(runID))
}

func workflowStepEnvironment(workspaceRoot string, request workflowrun.ExecuteStepRequest) ([]string, error) {
	env := withoutEnvPrefix(os.Environ(), "OPENCTO_")
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	dataDir, err := workflowbundle.WorkflowDataDir(workspaceRoot, request.WorkflowID)
	if err != nil {
		return nil, err
	}
	runDir := strings.TrimSpace(request.RunPath)
	if runDir == "" {
		return nil, fmt.Errorf("run_path is required")
	}
	stepID := strings.TrimSpace(request.Step.ID)
	env = upsertEnv(env, config.EnvOpenCTOWorkspace, workspaceRoot)
	env = upsertEnv(env, config.EnvOpenCTORunDir, runDir)
	env = upsertEnv(env, config.EnvOpenCTOArtifactsDir, workflowRunArtifactsDir(runDir))
	env = upsertEnv(env, config.EnvOpenCTODataDir, dataDir)
	env = upsertEnv(env, config.EnvOpenCTOStepID, stepID)
	env = upsertEnv(env, config.EnvOpenCTOStepOutput, workflowStepOutputPath(runDir, stepID))
	return env, nil
}

func workflowRunArtifactsDir(runDir string) string {
	return filepath.Join(strings.TrimSpace(runDir), "artifacts")
}

func workflowStepOutputPath(runDir, stepID string) string {
	return filepath.Join(workflowRunArtifactsDir(runDir), "steps", strings.TrimSpace(stepID), "output.json")
}

func withoutEnvPrefix(env []string, prefix string) []string {
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func upsertEnv(env []string, name, value string) []string {
	prefix := name + "="
	entry := prefix + value
	for index := range env {
		if strings.HasPrefix(env[index], prefix) {
			env[index] = entry
			return env
		}
	}
	return append(env, entry)
}

func tailWorkflowLog(path string, limit int64) (string, bool) {
	if limit <= 0 {
		limit = defaultExecTailBytes
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", false
	}
	return string(data), size > limit
}

func workflowStepSummary(stdout, stderr string, stdoutTruncated, stderrTruncated bool) string {
	var builder strings.Builder
	if stdout != "" {
		builder.WriteString("stdout:\n")
		builder.WriteString(stdout)
		if stdoutTruncated {
			builder.WriteString("\n[stdout truncated]")
		}
	}
	if stderr != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("stderr:\n")
		builder.WriteString(stderr)
		if stderrTruncated {
			builder.WriteString("\n[stderr truncated]")
		}
	}
	if builder.Len() == 0 {
		return "step produced no output"
	}
	return builder.String()
}
