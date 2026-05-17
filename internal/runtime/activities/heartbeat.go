package activities

import (
	"context"
	"time"

	"go.temporal.io/sdk/activity"

	"github.com/opencto/opencto/internal/agent"
)

func startResponseSessionHeartbeat(ctx context.Context, gap time.Duration, details any) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	if gap <= 0 {
		gap = defaultResponseSessionRefresh
	}
	recordResponseSessionHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(gap)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recordResponseSessionHeartbeat(ctx, details)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func recordResponseSessionHeartbeat(ctx context.Context, details any) {
	defer func() {
		_ = recover()
	}()
	activity.RecordHeartbeat(ctx, details)
}

func (a *Activities) startToolActivityHeartbeat(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	gap := a.HeartbeatGap
	if gap <= 0 {
		gap = defaultToolHeartbeatGap
	}
	details := map[string]string{
		"command":      choice.Command,
		"intent":       choice.Intent,
		"tool_call_id": execution.ToolCallID,
	}
	recordToolHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(gap)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recordToolHeartbeat(ctx, details)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func recordToolHeartbeat(ctx context.Context, details any) {
	defer func() {
		_ = recover()
	}()
	activity.RecordHeartbeat(ctx, details)
}
