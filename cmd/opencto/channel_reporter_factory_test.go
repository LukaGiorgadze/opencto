package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
)

func TestConfiguredTelegramRuntimeRequiresWebhookSecret(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:token")
	t.Setenv("TELEGRAM_WEBHOOK_URL", "https://example.com/telegram/webhook")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")

	cfg := config.Config{
		Channels: config.ChannelsConfig{
			Telegram: config.TelegramConfig{
				Enabled: true,
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	_, err := newConfiguredChannelReporter(cfg, &runtime.Dispatcher{}, logger, domain.ChannelTypeTelegram)
	if err == nil {
		t.Fatal("expected missing telegram webhook secret error")
	}
	if !strings.Contains(err.Error(), "TELEGRAM_WEBHOOK_SECRET") {
		t.Fatalf("expected TELEGRAM_WEBHOOK_SECRET error, got %v", err)
	}
}
