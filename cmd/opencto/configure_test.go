package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunConfigureUpdatesDiscordEnvAndConfig(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	input := strings.NewReader("sk-test\ndiscord\ndiscord-token\ndiscord-app\n")
	var output bytes.Buffer

	if err := runConfigure(workspaceRoot, input, &output); err != nil {
		t.Fatalf("configure: %v", err)
	}

	envText := readConfigureTestFile(t, filepath.Join(workspaceRoot, ".env"))
	for _, want := range []string{
		"OPENAI_API_KEY=sk-test",
		"DISCORD_TOKEN=discord-token",
		"DISCORD_APPLICATION_ID=discord-app",
	} {
		if !strings.Contains(envText, want) {
			t.Fatalf("expected .env to contain %q:\n%s", want, envText)
		}
	}

	assertChannelConfig(t, workspaceRoot, true, false)
}

func TestRunConfigureUpdatesTelegramEnvAndConfig(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	input := strings.NewReader("sk-test\ntelegram\ntelegram-token\nhttps://example.com/telegram/webhook\ntelegram-secret\n")
	var output bytes.Buffer

	if err := runConfigure(workspaceRoot, input, &output); err != nil {
		t.Fatalf("configure: %v", err)
	}

	envText := readConfigureTestFile(t, filepath.Join(workspaceRoot, ".env"))
	for _, want := range []string{
		"OPENAI_API_KEY=sk-test",
		"TELEGRAM_BOT_TOKEN=telegram-token",
		"TELEGRAM_WEBHOOK_URL=https://example.com/telegram/webhook",
		"TELEGRAM_WEBHOOK_SECRET=telegram-secret",
	} {
		if !strings.Contains(envText, want) {
			t.Fatalf("expected .env to contain %q:\n%s", want, envText)
		}
	}

	assertChannelConfig(t, workspaceRoot, false, true)
}

func TestRunConfigureKeepsExistingSecretOnBlankInput(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	if _, err := ensureStarterFiles(workspaceRoot); err != nil {
		t.Fatalf("starter files: %v", err)
	}
	envPath := filepath.Join(workspaceRoot, ".env")
	if err := os.WriteFile(envPath, []byte("OPENAI_API_KEY=old-key\nDISCORD_TOKEN=old-token\nDISCORD_APPLICATION_ID=old-app\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}

	input := strings.NewReader("\ndiscord\n\n\n")
	if err := runConfigure(workspaceRoot, input, ioDiscard{}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	envText := readConfigureTestFile(t, envPath)
	for _, want := range []string{
		"OPENAI_API_KEY=old-key",
		"DISCORD_TOKEN=old-token",
		"DISCORD_APPLICATION_ID=old-app",
	} {
		if !strings.Contains(envText, want) {
			t.Fatalf("expected .env to keep %q:\n%s", want, envText)
		}
	}
}

func TestRunConfigureDefaultsInterfaceFromExistingConfig(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	if _, err := ensureStarterFiles(workspaceRoot); err != nil {
		t.Fatalf("starter files: %v", err)
	}
	if err := writeConfiguredInterface(filepath.Join(workspaceRoot, "config.json"), "telegram"); err != nil {
		t.Fatalf("write configured interface: %v", err)
	}

	input := strings.NewReader("\n\n\n\n\n")
	if err := runConfigure(workspaceRoot, input, ioDiscard{}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	assertChannelConfig(t, workspaceRoot, false, true)
}

func TestRunConfigureSwitchesChannelConfig(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	if _, err := ensureStarterFiles(workspaceRoot); err != nil {
		t.Fatalf("starter files: %v", err)
	}
	if err := writeConfiguredInterface(filepath.Join(workspaceRoot, "config.json"), "discord"); err != nil {
		t.Fatalf("write configured interface: %v", err)
	}

	input := strings.NewReader("\ntelegram\ntelegram-token\nhttps://example.com/telegram/webhook\ntelegram-secret\n")
	if err := runConfigure(workspaceRoot, input, ioDiscard{}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	assertChannelConfig(t, workspaceRoot, false, true)
}

func TestRunConfigureAllowsSkippingAllValues(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	input := strings.NewReader("\nnone\n")

	if err := runConfigure(workspaceRoot, input, ioDiscard{}); err != nil {
		t.Fatalf("configure: %v", err)
	}

	envText := readConfigureTestFile(t, filepath.Join(workspaceRoot, ".env"))
	if envText != string(defaultEnvFile()) {
		t.Fatalf("expected default .env when all values are skipped:\n%s", envText)
	}
	assertDefaultConfig(t, workspaceRoot)
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func readConfigureTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func assertDefaultConfig(t *testing.T, workspaceRoot string) {
	t.Helper()
	configText := readConfigureTestFile(t, filepath.Join(workspaceRoot, "config.json"))
	if configText != string(defaultConfigJSON()) {
		t.Fatalf("expected config.json to keep default values:\n%s", configText)
	}
}

func assertChannelConfig(t *testing.T, workspaceRoot string, wantDiscord, wantTelegram bool) {
	t.Helper()
	configText := readConfigureTestFile(t, filepath.Join(workspaceRoot, "config.json"))
	var parsed struct {
		Channels struct {
			Discord struct {
				Enabled bool `json:"enabled"`
			} `json:"discord"`
			Telegram struct {
				Enabled bool `json:"enabled"`
			} `json:"telegram"`
		} `json:"channels"`
	}
	if err := json.Unmarshal([]byte(configText), &parsed); err != nil {
		t.Fatalf("parse config.json: %v", err)
	}
	if parsed.Channels.Discord.Enabled != wantDiscord || parsed.Channels.Telegram.Enabled != wantTelegram {
		t.Fatalf("expected discord=%t telegram=%t, got discord=%t telegram=%t:\n%s",
			wantDiscord,
			wantTelegram,
			parsed.Channels.Discord.Enabled,
			parsed.Channels.Telegram.Enabled,
			configText,
		)
	}
}
