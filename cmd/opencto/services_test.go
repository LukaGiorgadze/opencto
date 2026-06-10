package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/config"
)

func TestFriendlyDockerComposeErrorExplainsDockerDaemonFailure(t *testing.T) {
	err := friendlyDockerComposeError(errors.New("failed to connect to the docker API at unix:///Users/luka/.docker/run/docker.sock"))
	if err == nil {
		t.Fatal("expected friendly error")
	}
	message := err.Error()
	for _, want := range []string{"Docker is not running or not reachable", "Start Docker Desktop or OrbStack"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected friendly Docker guidance %q in:\n%s", want, message)
		}
	}
}

func TestEnsureRuntimeServiceFilesCreatesComposeAssets(t *testing.T) {
	workspace := t.TempDir()

	serviceDir, err := ensureRuntimeServiceFiles(workspace, testServiceConfig())
	if err != nil {
		t.Fatalf("ensure service files: %v", err)
	}

	composePath := filepath.Join(serviceDir, "compose.yaml")
	compose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composeText := string(compose)
	for _, want := range []string{
		"postgresql:",
		"temporal:",
		"temporal-init:",
		`profiles: ["bifrost"]`,
		`"127.0.0.1:8081:8080"`,
		"../.env",
	} {
		if !strings.Contains(composeText, want) {
			t.Fatalf("expected compose to contain %q:\n%s", want, composeText)
		}
	}
	if strings.Contains(composeText, "BIFROST_API_KEY: ${BIFROST_API_KEY") {
		t.Fatalf("expected compose not to override BIFROST_API_KEY from env_file:\n%s", composeText)
	}
	if strings.Contains(composeText, `"5432:5432"`) {
		t.Fatalf("expected generated Postgres service not to bind host port 5432:\n%s", composeText)
	}

	for _, path := range []string{
		filepath.Join(serviceDir, "dynamicconfig", "development-sql.yml"),
		filepath.Join(serviceDir, "scripts", "setup-postgres.sh"),
		filepath.Join(serviceDir, "scripts", "create-namespace.sh"),
		filepath.Join(serviceDir, "scripts", "temporal-init-entrypoint.sh"),
		filepath.Join(serviceDir, "bifrost.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected generated service file %s: %v", path, err)
		}
	}

	setupPostgres, err := os.ReadFile(filepath.Join(serviceDir, "scripts", "setup-postgres.sh"))
	if err != nil {
		t.Fatalf("read setup postgres script: %v", err)
	}
	setupPostgresText := string(setupPostgres)
	for _, want := range []string{
		"is_existing_schema_error()",
		"run_sql_tool_idempotent opencto --quiet create",
		"run_sql_tool_idempotent opencto --quiet setup-schema -v 0.0",
		"run_sql_tool_idempotent temporal_visibility --quiet create",
		"run_sql_tool_idempotent temporal_visibility --quiet setup-schema -v 0.0",
	} {
		if !strings.Contains(setupPostgresText, want) {
			t.Fatalf("expected setup postgres script to contain %q:\n%s", want, setupPostgresText)
		}
	}
}

func TestEnsureRuntimeServiceFilesUsesConfiguredLocalBifrostPort(t *testing.T) {
	workspace := t.TempDir()
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true
	cfg.LLM.Bifrost.BaseURL = "http://localhost:9090/openai"

	serviceDir, err := ensureRuntimeServiceFiles(workspace, cfg)
	if err != nil {
		t.Fatalf("ensure service files: %v", err)
	}

	compose, err := os.ReadFile(filepath.Join(serviceDir, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	if composeText := string(compose); !strings.Contains(composeText, `"127.0.0.1:9090:8080"`) {
		t.Fatalf("expected configured Bifrost host port in compose:\n%s", composeText)
	}
}

func TestEnsureRuntimeServiceFilesIgnoresDisabledBifrostBaseURL(t *testing.T) {
	workspace := t.TempDir()
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = false
	cfg.LLM.Bifrost.BaseURL = "%"

	if _, err := ensureRuntimeServiceFiles(workspace, cfg); err != nil {
		t.Fatalf("ensure service files: %v", err)
	}
}

func TestManagedBifrostServiceEnabledSkipsRemoteGateway(t *testing.T) {
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true
	cfg.LLM.Bifrost.BaseURL = "https://bifrost.example.com/openai"

	enabled, err := managedBifrostServiceEnabled(cfg)
	if err != nil {
		t.Fatalf("managed bifrost enabled: %v", err)
	}
	if enabled {
		t.Fatalf("expected remote Bifrost gateway not to start bundled service")
	}
}

func TestManagedBifrostServiceEnabledSkipsLocalHTTPSGateway(t *testing.T) {
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true
	cfg.LLM.Bifrost.BaseURL = "https://127.0.0.1:8081/openai"

	enabled, err := managedBifrostServiceEnabled(cfg)
	if err != nil {
		t.Fatalf("managed bifrost enabled: %v", err)
	}
	if enabled {
		t.Fatalf("expected HTTPS Bifrost gateway not to start bundled HTTP service")
	}
}

func TestManagedBifrostServiceEnabledUsesDefaultLocalGateway(t *testing.T) {
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true

	enabled, err := managedBifrostServiceEnabled(cfg)
	if err != nil {
		t.Fatalf("managed bifrost enabled: %v", err)
	}
	if !enabled {
		t.Fatalf("expected default Bifrost gateway to start bundled service")
	}
}

func TestManagedBifrostHostPortUsesConfiguredLocalGateway(t *testing.T) {
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true
	cfg.LLM.Bifrost.BaseURL = "http://localhost:9090/openai"

	hostPort, managed, err := managedBifrostHostPort(cfg)
	if err != nil {
		t.Fatalf("managed bifrost host port: %v", err)
	}
	if !managed {
		t.Fatalf("expected configured local Bifrost gateway to be managed")
	}
	if hostPort != "127.0.0.1:9090" {
		t.Fatalf("unexpected Bifrost host port: %q", hostPort)
	}
}

func TestCheckManagedBifrostFailsWhenPortIsClosed(t *testing.T) {
	cfg := testServiceConfig()
	cfg.LLM.Bifrost.Enabled = true
	cfg.LLM.Bifrost.BaseURL = "http://127.0.0.1:1/openai"

	result := checkManagedBifrost(context.Background(), cfg)
	if result.status != doctorFail {
		t.Fatalf("expected Bifrost check to fail, got %#v", result)
	}
	if !strings.Contains(result.detail, "127.0.0.1:1") {
		t.Fatalf("expected Bifrost detail to include host port, got %#v", result)
	}
}

func TestEnsureRuntimeServiceFilesUsesTemporalConfig(t *testing.T) {
	workspace := t.TempDir()
	cfg := testServiceConfig()
	cfg.Temporal.HostPort = "127.0.0.1:7723"
	cfg.Temporal.Namespace = "custom"

	serviceDir, err := ensureRuntimeServiceFiles(workspace, cfg)
	if err != nil {
		t.Fatalf("ensure service files: %v", err)
	}

	compose, err := os.ReadFile(filepath.Join(serviceDir, "compose.yaml"))
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	composeText := string(compose)
	for _, want := range []string{
		`"127.0.0.1:7723:7233"`,
		`DEFAULT_NAMESPACE: "custom"`,
	} {
		if !strings.Contains(composeText, want) {
			t.Fatalf("expected compose to contain %q:\n%s", want, composeText)
		}
	}
}

func TestEnsureRuntimeServiceFilesRefreshesExistingCompose(t *testing.T) {
	workspace := t.TempDir()
	cfg := testServiceConfig()

	serviceDir, err := ensureRuntimeServiceFiles(workspace, cfg)
	if err != nil {
		t.Fatalf("ensure service files: %v", err)
	}
	composePath := filepath.Join(serviceDir, "compose.yaml")

	cfg.Temporal.HostPort = "127.0.0.1:7723"
	cfg.Temporal.Namespace = "custom"
	if _, err := ensureRuntimeServiceFiles(workspace, cfg); err != nil {
		t.Fatalf("refresh service files: %v", err)
	}

	compose, err := os.ReadFile(composePath)
	if err != nil {
		t.Fatalf("read refreshed compose: %v", err)
	}
	composeText := string(compose)
	for _, want := range []string{
		`"127.0.0.1:7723:7233"`,
		`DEFAULT_NAMESPACE: "custom"`,
	} {
		if !strings.Contains(composeText, want) {
			t.Fatalf("expected refreshed compose to contain %q:\n%s", want, composeText)
		}
	}
}

func testServiceConfig() config.Config {
	return config.Config{
		Temporal: config.TemporalConfig{
			HostPort:  "127.0.0.1:7233",
			Namespace: "default",
		},
	}
}
