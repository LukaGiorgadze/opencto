package activities

import (
	"context"

	"go.temporal.io/sdk/activity"

	"github.com/opencto/opencto/internal/agent"
)

func contextWithActivityLLMSession(ctx context.Context, projectID string, requestKind string) context.Context {
	if !activity.IsActivity(ctx) {
		return ctx
	}
	execution := activity.GetInfo(ctx).WorkflowExecution
	return agent.ContextWithLLMSession(ctx, agent.LLMSession{
		ProjectID:     projectID,
		WorkflowID:    execution.ID,
		WorkflowRunID: execution.RunID,
		RequestKind:   requestKind,
	})
}
