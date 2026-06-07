package activities

import (
	"context"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
)

func (a *Activities) CompleteWorkflowRun(ctx context.Context, request workflowrun.CompleteRequest) error {
	completedAt := time.Now().UTC()
	if a.Store == nil {
		return nil
	}
	return a.Store.UpsertScheduledWorkflowRun(ctx, domain.ScheduledWorkflowRun{
		ID:             strings.TrimSpace(request.RunID),
		ProjectID:      strings.TrimSpace(request.ProjectID),
		WorkflowID:     strings.TrimSpace(request.WorkflowID),
		RunPath:        strings.TrimSpace(request.RunPath),
		Status:         request.Status,
		StartedAt:      completedAt,
		ScheduledAt:    completedAt,
		CompletedAt:    &completedAt,
		FailureSummary: strings.TrimSpace(request.FailureSummary),
	})
}
