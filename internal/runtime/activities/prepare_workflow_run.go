package activities

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func (a *Activities) PrepareWorkflowRun(ctx context.Context, request workflowrun.PrepareRequest) (workflowrun.PrepareResult, error) {
	input := request.Input
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	workflowID, err := workflowbundle.NormalizeWorkflowID(input.WorkflowID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	commitHash := strings.TrimSpace(input.CommitHash)
	if commitHash == "" {
		return workflowrun.PrepareResult{}, fmt.Errorf("commit_hash is required")
	}
	repoDir, err := workflowbundle.WorkflowDir(a.WorkspaceRoot, workflowID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	scheduledAt := input.ScheduledAt
	if scheduledAt.IsZero() {
		scheduledAt = time.Now().UTC()
	}
	runID := strings.TrimSpace(request.TemporalRunID)
	if runID == "" {
		runID = stableActivityID("scheduled-workflow-run", projectID, workflowID, request.TemporalWorkflowID, scheduledAt.UTC().Format(time.RFC3339Nano))
	}
	runDir, err := workflowbundle.WorkflowRunDir(a.WorkspaceRoot, workflowID, runID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	dataDir, err := workflowbundle.WorkflowDataDir(a.WorkspaceRoot, workflowID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := os.RemoveAll(runDir); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := workflowbundle.ArchiveCommit(ctx, repoDir, commitHash, runDir); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(runDir, "artifacts"), 0o755); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	manifest, err := workflowbundle.LoadManifest(runDir)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	startedAt := time.Now().UTC()
	if a.Store != nil {
		if err := a.Store.UpsertScheduledWorkflowRun(ctx, domain.ScheduledWorkflowRun{
			ID:                 runID,
			ProjectID:          projectID,
			WorkflowID:         workflowID,
			CommitHash:         commitHash,
			TemporalWorkflowID: strings.TrimSpace(request.TemporalWorkflowID),
			TemporalRunID:      strings.TrimSpace(request.TemporalRunID),
			Status:             domain.ExecutionStatusRunning,
			ScheduledAt:        scheduledAt.UTC(),
			StartedAt:          startedAt,
			RunPath:            runDir,
			Metadata: domain.Metadata{
				"schedule_id": strings.TrimSpace(input.ScheduleID),
			},
		}); err != nil {
			return workflowrun.PrepareResult{}, err
		}
	}
	return workflowrun.PrepareResult{RunID: runID, RunPath: runDir, Manifest: manifest}, nil
}
