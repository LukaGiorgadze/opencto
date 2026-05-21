package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/opencto/opencto/internal/channels/discord"
	"github.com/opencto/opencto/internal/channels/local"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
	"github.com/opencto/opencto/internal/runtime/activities"
)

type closeReporter interface {
	Close() error
}

type channelStarter interface {
	Start(context.Context) error
}

type channelReporterSet struct {
	Reporter activities.Reporter
	Starters []channelStarter
	Closers  []closeReporter
}

func (s channelReporterSet) Close() error {
	var errs []error
	for _, closer := range s.Closers {
		if closer == nil {
			continue
		}
		if err := closer.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func newConfiguredChannelReporter(cfg config.Config, dispatcher *runtime.Dispatcher, logger *slog.Logger, requested ...domain.ChannelType) (channelReporterSet, error) {
	localReporter := local.NewReporter(logger)
	reporters := map[domain.ChannelType]activities.Reporter{}
	set := channelReporterSet{
		Reporter: newChannelReporter(localReporter, reporters),
	}

	if cfg.Channels.Discord.Enabled && channelRequested(requested, domain.ChannelTypeDiscord) {
		token := strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
		if token == "" {
			return channelReporterSet{}, fmt.Errorf("DISCORD_TOKEN is required when discord channel is enabled")
		}
		appID := strings.TrimSpace(os.Getenv("DISCORD_APPLICATION_ID"))
		if dispatcher != nil && appID == "" && logger != nil {
			logger.Warn("discord application id is not set; continuing because the runtime does not require it yet", slog.String("env", "DISCORD_APPLICATION_ID"))
		}
		adapter, err := discord.New(defaultProject.ID, token, appID, dispatcher, logger, discord.Options{
			WorkspaceRoot: cfg.General.WorkspaceRoot,
			MessageLimits: discord.MessageLimits{
				MaxChars: cfg.Channels.Discord.OutboundMessages.MaxChars,
			},
			AttachmentLimits: discord.AttachmentLimits{
				MaxFiles:      cfg.Channels.Discord.OutboundAttachments.MaxFiles,
				MaxFileBytes:  cfg.Channels.Discord.OutboundAttachments.MaxFileBytes,
				MaxTotalBytes: cfg.Channels.Discord.OutboundAttachments.MaxTotalBytes,
			},
		})
		if err != nil {
			return channelReporterSet{}, fmt.Errorf("create discord adapter: %w", err)
		}
		reporters[domain.ChannelTypeDiscord] = adapter
		set.Starters = append(set.Starters, adapter)
		set.Closers = append(set.Closers, adapter)
	}

	return set, nil
}

func channelRequested(requested []domain.ChannelType, channelType domain.ChannelType) bool {
	if len(requested) == 0 {
		return true
	}
	for _, candidate := range requested {
		if candidate == channelType {
			return true
		}
	}
	return false
}
