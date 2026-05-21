package main

import (
	"context"
	"fmt"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
)

type channelReporter struct {
	local     activities.Reporter
	reporters map[domain.ChannelType]activities.Reporter
}

func newChannelReporter(localReporter activities.Reporter, reporters map[domain.ChannelType]activities.Reporter) *channelReporter {
	if reporters == nil {
		reporters = map[domain.ChannelType]activities.Reporter{}
	}
	return &channelReporter{
		local:     localReporter,
		reporters: reporters,
	}
}

func (r *channelReporter) Report(ctx context.Context, event domain.Event, report domain.ReportMessage) ([]domain.ReportReceipt, error) {
	reporter, err := r.reporterFor(event.ChannelType)
	if err != nil {
		return nil, err
	}
	if reporter == nil {
		return nil, nil
	}
	return reporter.Report(ctx, event, report)
}

func (r *channelReporter) NotifyTyping(ctx context.Context, event domain.Event) error {
	if event.ChannelType == "" || event.ChannelType == domain.ChannelTypeCLI {
		return nil
	}
	reporter, ok := r.reporters[event.ChannelType].(activities.TypingReporter)
	if !ok || reporter == nil {
		return nil
	}
	return reporter.NotifyTyping(ctx, event)
}

func (r *channelReporter) reporterFor(channelType domain.ChannelType) (activities.Reporter, error) {
	switch channelType {
	case domain.ChannelTypeCLI, "":
		return r.local, nil
	}
	if reporter := r.reporters[channelType]; reporter != nil {
		return reporter, nil
	}
	return nil, fmt.Errorf("channel_type %q is not enabled in config", channelType)
}
