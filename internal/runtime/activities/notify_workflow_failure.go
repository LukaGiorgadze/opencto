package activities

import (
	"context"
	"strings"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
)

func (a *Activities) NotifyWorkflowFailure(ctx context.Context, request workflowrun.NotifyFailureRequest) error {
	if a.Reporter == nil {
		return nil
	}
	message := "Scheduled workflow failed."
	if name := strings.TrimSpace(request.Input.WorkflowName); name != "" {
		message = "Scheduled workflow failed: " + name
	}
	if runID := strings.TrimSpace(request.RunID); runID != "" {
		message += "\nrun_id: " + runID
	}
	if failure := strings.TrimSpace(request.FailureSummary); failure != "" {
		message += "\nerror: " + failure
	}
	_, err := a.Reporter.Report(ctx, request.Input.SourceEvent, domain.ReportMessage{Text: message})
	return err
}
