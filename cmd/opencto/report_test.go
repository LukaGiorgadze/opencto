package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	})
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "Metric reached 1000." ||
		options.ChannelType != "discord" ||
		options.ChannelID != "1234567890" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseReportCommandAllowsThreadID(t *testing.T) {
	t.Parallel()

	options, err := parseReportCommandArgs([]string{
		"Metric reached 1000.",
		"-channel_type", "telegram",
		"-channel_id", "-1001234567890",
		"-thread_id", "42",
	})
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "Metric reached 1000." ||
		options.ChannelType != "telegram" ||
		options.ChannelID != "-1001234567890" ||
		options.ThreadID != "42" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseReportCommandRequiresChannelID(t *testing.T) {
	t.Parallel()

	_, err := parseReportCommandArgs([]string{"hello", "-channel_type", "discord"})
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
	})
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
	})
	if err != nil {
		t.Fatalf("parse report command: %v", err)
	}
	if options.Message != "" || len(options.Attachments) != 1 || options.Attachments[0].Path != "reports/chart.png" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseReportCommandRequiresMessageOrAttachment(t *testing.T) {
	t.Parallel()

	_, err := parseReportCommandArgs([]string{"-channel_type", "discord", "-channel_id", "1234567890"})
	if err == nil {
		t.Fatal("expected message or file error")
	}
}

func TestParseReportCommandRejectsConfigFlag(t *testing.T) {
	t.Parallel()

	_, err := parseReportCommandArgs([]string{
		"hello",
		"-config", "/tmp/opencto/config.json",
		"-channel_type", "cli",
		"-channel_id", "default",
	})
	if err == nil {
		t.Fatal("expected unknown config flag error")
	}
}

func TestLoadConfigRequiresConfigFile(t *testing.T) {
	t.Parallel()

	_, _, err := loadConfig(filepath.Join(t.TempDir(), "config.json"), t.TempDir())
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestEnsureStarterFilesCreatesDefaultConfigAndDotEnv(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	starter, err := ensureStarterFiles(workspaceRoot)
	if err != nil {
		t.Fatalf("ensure starter files: %v", err)
	}
	if len(starter.Created) != 3 {
		t.Fatalf("expected config, env, and skills to be created, got %#v", starter.Created)
	}
	if len(starter.UserEditableCreated) != 2 {
		t.Fatalf("expected config and env to require editing, got %#v", starter.UserEditableCreated)
	}
	configPath := filepath.Join(workspaceRoot, "config.json")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if strings.Contains(string(configData), "workspace_root") {
		t.Fatalf("generated config should not contain workspace_root:\n%s", string(configData))
	}
	envInfo, err := os.Stat(filepath.Join(workspaceRoot, ".env"))
	if err != nil {
		t.Fatalf("stat generated .env: %v", err)
	}
	if envInfo.Mode().Perm() != 0o600 {
		t.Fatalf("expected .env permissions 0600, got %s", envInfo.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(workspaceRoot, "skills", "codex-noninteractive", "SKILL.md")); err != nil {
		t.Fatalf("stat generated skills: %v", err)
	}
	_, cfg, err := loadConfig(filepath.Join(workspaceRoot, "config.json"), workspaceRoot)
	if err != nil {
		t.Fatalf("load generated config: %v", err)
	}
	if cfg.General.WorkspaceRoot != workspaceRoot {
		t.Fatalf("expected generated workspace root %q, got %q", workspaceRoot, cfg.General.WorkspaceRoot)
	}
}

func TestLoadCommandEnvironmentUsesRepoConfigInDevMode(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "workspace")
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	t.Setenv("OPENCTO_WORKSPACE", workspaceRoot)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), defaultConfigJSON(), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/opencto/opencto\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("OPENCTO_LOCAL_CONFIG_TEST=from-local\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".env"), []byte("OPENCTO_WORKSPACE_ENV_TEST=from-workspace\n"), 0o644); err != nil {
		t.Fatalf("write workspace .env: %v", err)
	}
	skillsRoot := filepath.Join(dir, "skills")
	if err := os.MkdirAll(filepath.Join(skillsRoot, "repo-skill"), 0o755); err != nil {
		t.Fatalf("mkdir local skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillsRoot, "repo-skill", "SKILL.md"), []byte("# Repo Skill\n\nUse when testing repo skills.\n"), 0o644); err != nil {
		t.Fatalf("write local skill: %v", err)
	}
	if err := os.Unsetenv("OPENCTO_LOCAL_CONFIG_TEST"); err != nil {
		t.Fatalf("unset test env: %v", err)
	}
	if err := os.Unsetenv("OPENCTO_WORKSPACE_ENV_TEST"); err != nil {
		t.Fatalf("unset workspace env: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Unsetenv("OPENCTO_LOCAL_CONFIG_TEST")
		_ = os.Unsetenv("OPENCTO_WORKSPACE_ENV_TEST")
	})

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	env, err := loadCommandEnvironment(io.Discard)
	if err != nil {
		t.Fatalf("load command environment: %v", err)
	}
	wantConfigPath, err := filepath.EvalSymlinks(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatalf("resolve expected config path: %v", err)
	}
	gotConfigPath, err := filepath.EvalSymlinks(env.ConfigPath)
	if err != nil {
		t.Fatalf("resolve actual config path: %v", err)
	}
	if gotConfigPath != wantConfigPath {
		t.Fatalf("expected local config path, got %q", env.ConfigPath)
	}
	if len(env.Created) != 0 {
		t.Fatalf("expected no generated starter files, got %#v", env.Created)
	}
	wantSkillsRoot, err := filepath.EvalSymlinks(skillsRoot)
	if err != nil {
		t.Fatalf("resolve expected skills root: %v", err)
	}
	gotSkillsRoot, err := filepath.EvalSymlinks(env.SkillsRoot)
	if err != nil {
		t.Fatalf("resolve actual skills root: %v", err)
	}
	if gotSkillsRoot != wantSkillsRoot {
		t.Fatalf("expected repo skills root %q, got %q", wantSkillsRoot, gotSkillsRoot)
	}
	if got := os.Getenv("OPENCTO_LOCAL_CONFIG_TEST"); got != "from-local" {
		t.Fatalf("expected env from local .env, got %q", got)
	}
	if got := os.Getenv("OPENCTO_WORKSPACE_ENV_TEST"); got != "" {
		t.Fatalf("expected workspace .env to be ignored in repo mode, got %q", got)
	}
}

func TestLoadCommandEnvironmentIgnoresNonRepoConfig(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "workspace")
	t.Setenv("OPENCTO_WORKSPACE", workspaceRoot)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"app":"not-opencto"}`), 0o644); err != nil {
		t.Fatalf("write app config: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	env, err := loadCommandEnvironment(io.Discard)
	if err != nil {
		t.Fatalf("load command environment: %v", err)
	}
	wantConfigPath, err := filepath.EvalSymlinks(filepath.Join(workspaceRoot, "config.json"))
	if err != nil {
		t.Fatalf("resolve expected config path: %v", err)
	}
	gotConfigPath, err := filepath.EvalSymlinks(env.ConfigPath)
	if err != nil {
		t.Fatalf("resolve actual config path: %v", err)
	}
	if gotConfigPath != wantConfigPath {
		t.Fatalf("expected workspace config path, got %q", env.ConfigPath)
	}
}

func TestDefaultWorkspaceRootUsesHomeWhenEnvMissing(t *testing.T) {
	oldWorkspace, hadWorkspace := os.LookupEnv("OPENCTO_WORKSPACE")
	if err := os.Unsetenv("OPENCTO_WORKSPACE"); err != nil {
		t.Fatalf("unset OPENCTO_WORKSPACE: %v", err)
	}
	t.Cleanup(func() {
		if hadWorkspace {
			_ = os.Setenv("OPENCTO_WORKSPACE", oldWorkspace)
		} else {
			_ = os.Unsetenv("OPENCTO_WORKSPACE")
		}
	})

	root, err := defaultWorkspaceRoot()
	if err != nil {
		t.Fatalf("default workspace root: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve home: %v", err)
	}
	if want := filepath.Join(home, ".opencto"); root != want {
		t.Fatalf("expected default workspace root %q, got %q", want, root)
	}
}

func TestLoadCommandEnvironmentGeneratesWorkspaceConfigWithoutLocalConfig(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "workspace")
	t.Setenv("OPENCTO_WORKSPACE", workspaceRoot)

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	env, err := loadCommandEnvironment(io.Discard)
	if err != nil {
		t.Fatalf("load command environment: %v", err)
	}
	wantConfigPath, err := filepath.EvalSymlinks(filepath.Join(workspaceRoot, "config.json"))
	if err != nil {
		t.Fatalf("resolve expected config path: %v", err)
	}
	gotConfigPath, err := filepath.EvalSymlinks(env.ConfigPath)
	if err != nil {
		t.Fatalf("resolve actual config path: %v", err)
	}
	if gotConfigPath != wantConfigPath {
		t.Fatalf("expected workspace config path, got %q", env.ConfigPath)
	}
	if len(env.Created) != 3 {
		t.Fatalf("expected generated config, env, and skills, got %#v", env.Created)
	}
	if len(env.UserEditableCreated) != 2 {
		t.Fatalf("expected generated config and env to require editing, got %#v", env.UserEditableCreated)
	}
}

func TestLoadCommandEnvironmentCopiesSkillsWithoutRequiringUserEdit(t *testing.T) {
	dir := t.TempDir()
	workspaceRoot := filepath.Join(dir, "workspace")
	t.Setenv("OPENCTO_WORKSPACE", workspaceRoot)
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, "config.json"), defaultConfigJSON(), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspaceRoot, ".env"), nil, 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	env, err := loadCommandEnvironment(io.Discard)
	if err != nil {
		t.Fatalf("load command environment: %v", err)
	}
	if len(env.Created) != 1 || filepath.Base(env.Created[0]) != "skills" {
		t.Fatalf("expected only skills to be created, got %#v", env.Created)
	}
	if len(env.UserEditableCreated) != 0 {
		t.Fatalf("expected no user-editable files to be created, got %#v", env.UserEditableCreated)
	}
}

func TestLoadDotEnvSetsMissingValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(`
DISCORD_TOKEN=test-token
TELEGRAM_BOT_TOKEN=telegram-token
export OPENCTO_REPORT_TEST_QUOTED="quoted value"
`), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("DISCORD_TOKEN", "")
	if err := os.Unsetenv("DISCORD_TOKEN"); err != nil {
		t.Fatalf("unset DISCORD_TOKEN: %v", err)
	}
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if err := os.Unsetenv("TELEGRAM_BOT_TOKEN"); err != nil {
		t.Fatalf("unset TELEGRAM_BOT_TOKEN: %v", err)
	}
	t.Setenv("OPENCTO_REPORT_TEST_QUOTED", "")
	if err := os.Unsetenv("OPENCTO_REPORT_TEST_QUOTED"); err != nil {
		t.Fatalf("unset quoted env: %v", err)
	}

	if err := loadDotEnv(dir); err != nil {
		t.Fatalf("load .env: %v", err)
	}
	if got := os.Getenv("DISCORD_TOKEN"); got != "test-token" {
		t.Fatalf("expected DISCORD_TOKEN from .env, got %q", got)
	}
	if got := os.Getenv("TELEGRAM_BOT_TOKEN"); got != "telegram-token" {
		t.Fatalf("expected TELEGRAM_BOT_TOKEN from .env, got %q", got)
	}
	if got := os.Getenv("OPENCTO_REPORT_TEST_QUOTED"); got != "quoted value" {
		t.Fatalf("expected quoted env from .env, got %q", got)
	}
}

func TestLoadDotEnvDoesNotOverrideExistingValues(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DISCORD_TOKEN=from-file\n"), 0o644); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	t.Setenv("DISCORD_TOKEN", "existing")

	if err := loadDotEnv(dir); err != nil {
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
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	if err := os.Unsetenv("TELEGRAM_BOT_TOKEN"); err != nil {
		t.Fatalf("unset TELEGRAM_BOT_TOKEN: %v", err)
	}

	cfg := config.Config{
		General: config.GeneralConfig{
			WorkspaceRoot: t.TempDir(),
		},
		Channels: config.ChannelsConfig{
			Discord: config.DiscordConfig{
				Enabled: true,
			},
			Telegram: config.TelegramConfig{
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
