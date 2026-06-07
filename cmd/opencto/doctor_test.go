package main

import (
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/config"
)

func TestCheckTelegramEnvRequiresWebhookSecret(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:token")
	t.Setenv("TELEGRAM_WEBHOOK_SECRET", "")

	result := checkTelegramEnv(config.Config{
		Channels: config.ChannelsConfig{
			Telegram: config.TelegramConfig{
				Enabled: true,
				Webhook: config.TelegramWebhookConfig{
					URL: "https://example.com/telegram/webhook",
				},
			},
		},
	})

	if result.status != doctorFail {
		t.Fatalf("expected telegram env failure, got %#v", result)
	}
	if !strings.Contains(result.detail, "TELEGRAM_WEBHOOK_SECRET") {
		t.Fatalf("expected missing webhook secret detail, got %#v", result)
	}
}
