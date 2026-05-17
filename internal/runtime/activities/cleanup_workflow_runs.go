package activities

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	"github.com/opencto/opencto/internal/workflowbundle"
)

func (a *Activities) CleanupWorkflowRuns(ctx context.Context, request workflowrun.CleanupRunsRequest) (workflowrun.CleanupRunsResult, error) {
	workflowID, err := workflowbundle.NormalizeWorkflowID(request.WorkflowID)
	if err != nil {
		return workflowrun.CleanupRunsResult{}, err
	}
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	keep := request.KeepLast
	if keep <= 0 {
		keep = workflowrun.DefaultRunRetention
	}
	runsDir, err := workflowbundle.WorkflowRunsDir(a.WorkspaceRoot, workflowID)
	if err != nil {
		return workflowrun.CleanupRunsResult{}, err
	}
	entries, err := os.ReadDir(runsDir)
	if errors.Is(err, os.ErrNotExist) {
		return workflowrun.CleanupRunsResult{}, nil
	}
	if err != nil {
		return workflowrun.CleanupRunsResult{}, err
	}

	runs := make([]workflowRunDirectory, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return workflowrun.CleanupRunsResult{}, err
		}
		runs = append(runs, workflowRunDirectory{
			id:      entry.Name(),
			path:    filepath.Join(runsDir, entry.Name()),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].modTime.Equal(runs[j].modTime) {
			return runs[i].id > runs[j].id
		}
		return runs[i].modTime.After(runs[j].modTime)
	})

	keepIDs := make(map[string]bool, keep+1)
	currentRunID := strings.TrimSpace(request.CurrentRunID)
	for index, run := range runs {
		if index < keep || run.id == currentRunID {
			keepIDs[run.id] = true
		}
	}
	var deleted []string
	for _, run := range runs {
		if keepIDs[run.id] {
			continue
		}
		active, err := a.scheduledWorkflowRunActive(ctx, projectID, run.id)
		if err != nil {
			return workflowrun.CleanupRunsResult{DeletedRunIDs: deleted}, err
		}
		if active {
			continue
		}
		if err := ctx.Err(); err != nil {
			return workflowrun.CleanupRunsResult{DeletedRunIDs: deleted}, err
		}
		if err := os.RemoveAll(run.path); err != nil {
			return workflowrun.CleanupRunsResult{DeletedRunIDs: deleted}, err
		}
		deleted = append(deleted, run.id)
	}
	return workflowrun.CleanupRunsResult{DeletedRunIDs: deleted}, nil
}

func (a *Activities) scheduledWorkflowRunActive(ctx context.Context, projectID, runID string) (bool, error) {
	if a.Store == nil || strings.TrimSpace(projectID) == "" || strings.TrimSpace(runID) == "" {
		return false, nil
	}
	run, ok, err := a.Store.GetScheduledWorkflowRun(ctx, projectID, runID)
	if err != nil || !ok {
		return false, err
	}
	switch run.Status {
	case domain.ExecutionStatusPending, domain.ExecutionStatusRunning:
		return true, nil
	default:
		return false, nil
	}
}

type workflowRunDirectory struct {
	id      string
	path    string
	modTime time.Time
}
