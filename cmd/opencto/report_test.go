package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
)

func TestParseReportCommandAllowsFlagsAfterMessage(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{
		"Metric reached 1000.",
		"-channel_type", "discord",
		"-channel_id", "1234567890",
	}, "/opencto/config.json")
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "Metric reached 1000." ||
		options.ChannelType != "discord" ||
		options.ChannelID != "1234567890" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseReportCommandRequiresChannelID(t *testing.T) {
	t.Parallel()

	_, err := parseReportCommandArgs([]string{"hello", "-channel_type", "discord"}, "/opencto/config.json")
	if err == nil {
		t.Fatal("expected channel_id error")
	}
}

func TestParseReportCommandAllowsRepeatableFileAttachments(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{
		"see attached",
		"-channel_type", "discord",
		"-channel_id", "1234567890",
		"-file", "reports/chart.png",
		"-file=logs/output.txt",
	}, "/opencto/config.json")
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "see attached" || len(options.Attachments) != 2 {
		t.Fatalf("unexpected options: %#v", options)
	}
	if options.Attachments[0].Path != "reports/chart.png" || options.Attachments[1].Path != "logs/output.txt" {
		t.Fatalf("unexpected attachments: %#v", options.Attachments)
	}
}

func TestParseReportCommandAllowsAttachmentOnlyReport(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{
		"-channel_type", "discord",
		"-channel_id", "1234567890",
		"-file", "reports/chart.png",
	}, "/opencto/config.json")
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "" || len(options.Attachments) != 1 || options.Attachments[0].Path != "reports/chart.png" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseReportCommandRequiresMessageOrAttachment(t *testing.T) {
	t.Parallel()

	_, err := parseReportCommandArgs([]string{"-channel_type", "discord", "-channel_id", "1234567890"}, "/opencto/config.json")
	if err == nil {
		t.Fatal("expected message or file error")
	}
}

func TestParseReportCommandUsesRootConfigPath(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{"hello", "-channel_type", "cli", "-channel_id", "default"}, "/opencto/config.json")
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.ConfigPath != "/opencto/config.json" {
		t.Fatalf("expected root config path, got %q", options.ConfigPath)
	}
}

func TestLoadConfigRequiresConfigFile(t *testing.T) {
	t.Parallel()

	_, _, err := loadConfig(filepath.Join(t.TempDir(), "config.json"), t.TempDir())
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestResolveDefaultConfigPathFallsBackToResolvedRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path, err := resolveDefaultConfigPath(root)
	if err != nil {
		t.Fatalf("resolve default config path: %v", err)
	}
	if want := filepath.Join(root, "config.json"); path != want {
		t.Fatalf("expected config path %q, got %q", want, path)
	}
}

func TestParseReportCommandAllowsConfigOverride(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{
		"hello",
		"-config", "/tmp/opencto/config.json",
		"-channel_type", "cli",
		"-channel_id", "default",
	}, "/opencto/config.json")
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.ConfigPath != "/tmp/opencto/config.json" {
		t.Fatalf("expected overridden config path, got %q", options.ConfigPath)
	}
}

func TestLoadOpenCTORootDotEnvSetsMissingValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`
DISCORD_TOKEN=test-token
export OPENCTO_REPORT_TEST_QUOTED="quoted value"
`), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("DISCORD_TOKEN", "")
	if err := os.Unsetenv("DISCORD_TOKEN"); err != nil {
		t.Fatalf("unset DISCORD_TOKEN: %v", err)
	}
	t.Setenv("OPENCTO_REPORT_TEST_QUOTED", "")
	if err := os.Unsetenv("OPENCTO_REPORT_TEST_QUOTED"); err != nil {
		t.Fatalf("unset quoted env: %v", err)
	}

	if err := loadOpenCTORootDotEnv(dir); err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if got := os.Getenv("DISCORD_TOKEN"); got != "test-token" {
		t.Fatalf("expected DISCORD_TOKEN from .env, got %q", got)
	}
	if got := os.Getenv("OPENCTO_REPORT_TEST_QUOTED"); got != "quoted value" {
		t.Fatalf("expected quoted env from .env, got %q", got)
	}
}

func TestLoadOpenCTORootDotEnvDoesNotOverrideExistingValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DISCORD_TOKEN=from-file\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("DISCORD_TOKEN", "existing")

	if err := loadOpenCTORootDotEnv(dir); err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if got := os.Getenv("DISCORD_TOKEN"); got != "existing" {
		t.Fatalf("expected existing DISCORD_TOKEN to be preserved, got %q", got)
	}
}

func TestReportCommandCLIReporterDoesNotRequireDiscordToken(t *testing.T) {
	t.Setenv("DISCORD_TOKEN", "")
	if err := os.Unsetenv("DISCORD_TOKEN"); err != nil {
		t.Fatalf("unset DISCORD_TOKEN: %v", err)
	}

	cfg := config.Config{
		General: config.GeneralConfig{
			WorkspaceRoot: t.TempDir(),
		},
		Channels: config.ChannelsConfig{
			Discord: config.DiscordConfig{
				Enabled: true,
			},
		},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reporters, err := newConfiguredChannelReporter(cfg, nil, logger, domain.ChannelTypeCLI)
	if err != nil {
		t.Fatalf("create cli reporter: %v", err)
	}
	defer reporters.Close()

	if _, err := reporters.Reporter.Report(context.Background(), domain.Event{ChannelType: domain.ChannelTypeCLI}, domain.ReportMessage{Text: "hello"}); err != nil {
		t.Fatalf("report cli: %v", err)
	}
}
