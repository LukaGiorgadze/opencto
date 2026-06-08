package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	operatorservice "go.temporal.io/api/operatorservice/v1"
	temporalclient "go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/storage"
)

const (
	doctorOK   = "ok"
	doctorWarn = "warn"
	doctorFail = "fail"
)

var requiredTemporalSearchAttributes = []string{
	"opencto_project_id",
}

func runDoctorCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if commandHelpRequested(args) {
		writeCommandHelp(stdout, "doctor")
		return nil
	}
	if err := parseNoArgCommand("doctor", args); err != nil {
		return commandUsageError(stderr, "doctor", err.Error())
	}
	env, err := loadCommandEnvironment(stdout)
	if err != nil {
		return err
	}
	return runDoctor(ctx, env.ConfigPath, env.Config, defaultProject, stdout)
}

type doctorResult struct {
	status string
	name   string
	detail string
}

func runDoctor(ctx context.Context, configPath string, cfg config.Config, project domain.Project, out io.Writer) error {
	results := []doctorResult{
		{status: doctorOK, name: "config", detail: fmt.Sprintf("loaded %s", configPath)},
		{status: doctorOK, name: "project", detail: fmt.Sprintf("%s (%s)", project.ID, project.Name)},
		checkCommand(ctx, "go", []string{"version"}, "go"),
		checkCommand(ctx, "task", []string{"--version"}, "task"),
		checkCommand(ctx, "air", []string{"-v"}, "air"),
		checkCommand(ctx, "docker", []string{"compose", "version"}, "docker compose"),
		checkDockerDaemonStatus(ctx),
		checkWritableDir(cfg.General.WorkspaceRoot, "workspace root"),
		checkWritableDir(cfg.Runtime.StateDir, "state dir"),
		checkSQLiteSchema(ctx, cfg),
		checkDiscordEnv(cfg),
		checkTelegramEnv(cfg),
	}
	results = append(results, checkTemporal(ctx, cfg)...)

	writeDoctorResults(out, results)

	failures := 0
	for _, result := range results {
		if result.status == doctorFail {
			failures++
		}
	}
	if failures > 0 {
		if out != nil {
			fmt.Fprintf(out, "\n%d check(s) failed\n", failures)
		}
		return fmt.Errorf("%d check(s) failed", failures)
	}
	if out != nil {
		fmt.Fprintln(out, "\nall checks passed")
	}
	return nil
}

func checkCommand(ctx context.Context, name string, args []string, label string) doctorResult {
	if _, err := exec.LookPath(name); err != nil {
		return doctorResult{status: doctorFail, name: label, detail: fmt.Sprintf("%s is not on PATH", name)}
	}

	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			return doctorResult{status: doctorFail, name: label, detail: "version check timed out"}
		}
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return doctorResult{status: doctorFail, name: label, detail: detail}
	}

	return doctorResult{status: doctorOK, name: label, detail: commandSummary(string(output))}
}

func checkDockerDaemonStatus(ctx context.Context) doctorResult {
	if _, err := exec.LookPath("docker"); err != nil {
		return doctorResult{status: doctorFail, name: "docker daemon", detail: "docker is not on PATH"}
	}
	if err := checkDockerDaemon(ctx); err != nil {
		return doctorResult{status: doctorFail, name: "docker daemon", detail: err.Error()}
	}
	return doctorResult{status: doctorOK, name: "docker daemon", detail: "running"}
}

func checkWritableDir(path, label string) doctorResult {
	path = strings.TrimSpace(path)
	if path == "" {
		return doctorResult{status: doctorFail, name: label, detail: "path is empty"}
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return doctorResult{status: doctorFail, name: label, detail: fmt.Sprintf("create directory: %v", err)}
	}

	file, err := os.CreateTemp(path, ".opencto-doctor-*")
	if err != nil {
		return doctorResult{status: doctorFail, name: label, detail: fmt.Sprintf("write test file: %v", err)}
	}
	name := file.Name()
	closeErr := file.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return doctorResult{status: doctorFail, name: label, detail: fmt.Sprintf("close test file: %v", closeErr)}
	}
	if removeErr != nil {
		return doctorResult{status: doctorFail, name: label, detail: fmt.Sprintf("remove test file: %v", removeErr)}
	}

	return doctorResult{status: doctorOK, name: label, detail: path}
}

func checkSQLiteSchema(ctx context.Context, cfg config.Config) doctorResult {
	dbPath := storage.DefaultDBPath(cfg.General.WorkspaceRoot)
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return doctorResult{status: doctorWarn, name: "sqlite schema", detail: fmt.Sprintf("not initialized yet; run opencto start (%s)", dbPath)}
		}
		return doctorResult{status: doctorFail, name: "sqlite schema", detail: fmt.Sprintf("stat %s: %v", dbPath, err)}
	}

	store, _, err := openRuntimeStore(ctx, cfg)
	if err != nil {
		return doctorResult{status: doctorFail, name: "sqlite schema", detail: err.Error()}
	}
	defer store.Close()

	if err := store.VerifySchema(ctx); err != nil {
		return doctorResult{status: doctorWarn, name: "sqlite schema", detail: fmt.Sprintf("%v; run opencto start", err)}
	}
	return doctorResult{status: doctorOK, name: "sqlite schema", detail: dbPath}
}

func checkDiscordEnv(cfg config.Config) doctorResult {
	if !cfg.Channels.Discord.Enabled {
		return doctorResult{status: doctorOK, name: "discord env", detail: "disabled in config"}
	}

	missing := []string{}
	if strings.TrimSpace(os.Getenv("DISCORD_TOKEN")) == "" {
		missing = append(missing, "DISCORD_TOKEN")
	}
	if len(missing) > 0 {
		return doctorResult{status: doctorFail, name: "discord env", detail: "missing " + strings.Join(missing, ", ")}
	}
	if strings.TrimSpace(os.Getenv("DISCORD_APPLICATION_ID")) == "" {
		return doctorResult{status: doctorWarn, name: "discord env", detail: "DISCORD_TOKEN set; DISCORD_APPLICATION_ID is not set"}
	}
	return doctorResult{status: doctorOK, name: "discord env", detail: "required Discord variables are set"}
}

func checkTelegramEnv(cfg config.Config) doctorResult {
	if !cfg.Channels.Telegram.Enabled {
		return doctorResult{status: doctorOK, name: "telegram env", detail: "disabled in config"}
	}

	missing := []string{}
	if strings.TrimSpace(os.Getenv("TELEGRAM_BOT_TOKEN")) == "" {
		missing = append(missing, "TELEGRAM_BOT_TOKEN")
	}
	webhookURL := strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_URL"))
	if webhookURL == "" {
		missing = append(missing, "TELEGRAM_WEBHOOK_URL")
	}
	if strings.TrimSpace(os.Getenv("TELEGRAM_WEBHOOK_SECRET")) == "" {
		missing = append(missing, "TELEGRAM_WEBHOOK_SECRET")
	}
	if len(missing) > 0 {
		return doctorResult{status: doctorFail, name: "telegram env", detail: "missing " + strings.Join(missing, ", ")}
	}
	parsed, err := url.Parse(webhookURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return doctorResult{status: doctorFail, name: "telegram env", detail: "TELEGRAM_WEBHOOK_URL must be an absolute URL"}
	}
	if parsed.Scheme != "https" {
		return doctorResult{status: doctorFail, name: "telegram env", detail: "TELEGRAM_WEBHOOK_URL must use https"}
	}
	return doctorResult{status: doctorOK, name: "telegram env", detail: "required Telegram variables are set"}
}

func checkTemporal(ctx context.Context, cfg config.Config) []doctorResult {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	temporal, err := temporalclient.Dial(temporalclient.Options{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		detail := fmt.Sprintf("connect %s: %v", cfg.Temporal.HostPort, err)
		return []doctorResult{
			{status: doctorFail, name: "temporal", detail: detail},
			{status: doctorWarn, name: "temporal namespace", detail: "skipped because Temporal is unavailable"},
			{status: doctorWarn, name: "temporal search attributes", detail: "skipped because Temporal is unavailable"},
		}
	}
	defer temporal.Close()

	results := []doctorResult{}
	if _, err := temporal.CheckHealth(checkCtx, &temporalclient.CheckHealthRequest{}); err != nil {
		results = append(results, doctorResult{status: doctorFail, name: "temporal", detail: fmt.Sprintf("health check %s: %v", cfg.Temporal.HostPort, err)})
	} else {
		results = append(results, doctorResult{status: doctorOK, name: "temporal", detail: cfg.Temporal.HostPort})
	}

	results = append(results, checkTemporalNamespace(checkCtx, cfg))
	results = append(results, checkTemporalSearchAttributes(checkCtx, temporal, cfg.Temporal.Namespace))
	return results
}

func checkTemporalNamespace(ctx context.Context, cfg config.Config) doctorResult {
	namespace, err := temporalclient.NewNamespaceClient(temporalclient.Options{
		HostPort: cfg.Temporal.HostPort,
	})
	if err != nil {
		return doctorResult{status: doctorFail, name: "temporal namespace", detail: err.Error()}
	}
	defer namespace.Close()

	if _, err := namespace.Describe(ctx, cfg.Temporal.Namespace); err != nil {
		return doctorResult{status: doctorFail, name: "temporal namespace", detail: fmt.Sprintf("%s: %v", cfg.Temporal.Namespace, err)}
	}
	return doctorResult{status: doctorOK, name: "temporal namespace", detail: cfg.Temporal.Namespace}
}

func checkTemporalSearchAttributes(ctx context.Context, temporal temporalclient.Client, namespace string) doctorResult {
	attributes, err := temporalSearchAttributeKeys(ctx, temporal, namespace)
	if err != nil {
		return doctorResult{status: doctorFail, name: "temporal search attributes", detail: err.Error()}
	}

	missing := missingTemporalSearchAttributes(attributes)
	if len(missing) > 0 {
		return doctorResult{status: doctorFail, name: "temporal search attributes", detail: fmt.Sprintf("missing %s in namespace %s", strings.Join(missing, ", "), namespace)}
	}
	return doctorResult{status: doctorOK, name: "temporal search attributes", detail: strings.Join(requiredTemporalSearchAttributes, ", ")}
}

func temporalSearchAttributeKeys(ctx context.Context, temporal temporalclient.Client, namespace string) (map[string]struct{}, error) {
	attributes, err := temporal.OperatorService().ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{
		Namespace: namespace,
	})
	if err != nil {
		return nil, err
	}

	keys := map[string]struct{}{}
	for attribute := range attributes.GetCustomAttributes() {
		keys[attribute] = struct{}{}
	}
	for attribute := range attributes.GetSystemAttributes() {
		keys[attribute] = struct{}{}
	}
	return keys, nil
}

func missingTemporalSearchAttributes(attributes map[string]struct{}) []string {
	missing := []string{}
	for _, attribute := range requiredTemporalSearchAttributes {
		if _, ok := attributes[attribute]; !ok {
			missing = append(missing, attribute)
		}
	}
	return missing
}

func writeDoctorResults(out io.Writer, results []doctorResult) {
	if out == nil {
		return
	}

	fmt.Fprintln(out, "OpenCTO doctor")
	for _, result := range results {
		fmt.Fprintf(out, "%-4s %-28s %s\n", doctorStatusIcon(result.status), result.name, result.detail)
	}
}

func doctorStatusIcon(status string) string {
	switch status {
	case doctorOK:
		return "✅"
	case doctorWarn:
		return "⚠️"
	case doctorFail:
		return "❌"
	default:
		return strings.ToUpper(status)
	}
}

func commandSummary(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "available"
	}
	lines := strings.Split(value, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" {
			return line
		}
	}
	return "available"
}
