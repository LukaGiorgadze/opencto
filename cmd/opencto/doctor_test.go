package main

import (
	"bytes"
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

func TestWriteDoctorResultsUsesStatusIcons(t *testing.T) {
	var out bytes.Buffer

	writeDoctorResults(&out, []doctorResult{
		{status: doctorOK, name: "ok check", detail: "ready"},
		{status: doctorWarn, name: "warn check", detail: "review"},
		{status: doctorFail, name: "fail check", detail: "fix"},
	})

	output := out.String()
	for _, want := range []string{"✅", "⚠️", "❌"} {
		if !strings.Contains(output, want) {
			t.Fatalf("expected doctor output to contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "OK     ") || strings.Contains(output, "FAIL   ") {
		t.Fatalf("expected icon statuses instead of plain labels:\n%s", output)
	}
}
