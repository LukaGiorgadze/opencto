package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/config"
)

const serviceWaitTimeout = 2 * time.Minute
const defaultManagedBifrostOpenAIBaseURL = "http://127.0.0.1:8081/openai"

func ensureRuntimeServices(ctx context.Context, cfg config.Config, dotEnvPath string, logger *slog.Logger, progress io.Writer) error {
	serviceDir, err := ensureRuntimeServiceFilesWithDotEnv(cfg.General.WorkspaceRoot, cfg, dotEnvPath)
	if err != nil {
		return err
	}
	writeServiceProgress(progress, "Generated Docker Compose files: %s", filepath.Join(serviceDir, "compose.yaml"))
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("Docker is required to run OpenCTO services. Install Docker Desktop or OrbStack, then run opencto start again")
	}
	if err := checkDockerDaemon(ctx); err != nil {
		return err
	}
	writeServiceProgress(progress, "Docker daemon is running")

	startBifrost, err := managedBifrostServiceEnabled(cfg)
	if err != nil {
		return err
	}
	args := []string{"compose", "-f", filepath.Join(serviceDir, "compose.yaml")}
	if startBifrost {
		args = append(args, "--profile", "bifrost")
	}
	args = append(args, "up", "-d", "temporal-init")
	if startBifrost {
		args = append(args, "bifrost")
	}

	if logger != nil {
		logger.Info("starting OpenCTO services",
			slog.String("compose_dir", serviceDir),
			slog.Bool("bifrost_enabled", cfg.LLM.Bifrost.Enabled),
			slog.Bool("managed_bifrost_started", startBifrost),
		)
	}
	writeServiceProgress(progress, "Starting Postgres and Temporal with Docker Compose")
	writeServiceProgress(progress, "Running: docker %s", strings.Join(args, " "))
	if err := runDockerCompose(ctx, serviceDir, args, progress); err != nil {
		return friendlyDockerComposeError(err)
	}
	if err := waitForRuntimeServices(ctx, cfg, startBifrost, progress); err != nil {
		return err
	}
	writeServiceProgress(progress, "OpenCTO services are ready")
	if logger != nil {
		logger.Info("OpenCTO services ready", slog.String("temporal", cfg.Temporal.HostPort))
	}
	return nil
}

func writeServiceProgress(out io.Writer, format string, args ...any) {
	if out == nil {
		return
	}
	fmt.Fprintf(out, "opencto: "+format+"\n", args...)
}

func checkDockerDaemon(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(checkCtx, "docker", "info", "--format", "{{.ServerVersion}}")
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
		return errors.New("Docker is installed, but the daemon did not respond. Start Docker Desktop or OrbStack, then run opencto start again")
	}
	detail := strings.TrimSpace(string(output))
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("Docker is installed, but it is not running or not reachable. Start Docker Desktop or OrbStack, then run opencto start again.\nDocker error: %s", detail)
}

func friendlyDockerComposeError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	if strings.Contains(message, "docker API") ||
		strings.Contains(message, "docker.sock") ||
		strings.Contains(message, "Cannot connect to the Docker daemon") ||
		strings.Contains(message, "Is the docker daemon running") {
		return fmt.Errorf("Docker is not running or not reachable. Start Docker Desktop or OrbStack, then run opencto start again.\nDocker error: %s", message)
	}
	return err
}

func ensureRuntimeServiceFiles(workspaceRoot string, cfg config.Config) (string, error) {
	return ensureRuntimeServiceFilesWithDotEnv(workspaceRoot, cfg, "")
}

func ensureRuntimeServiceFilesWithDotEnv(workspaceRoot string, cfg config.Config, dotEnvPath string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	dotEnvPath, composeDotEnvPath, err := resolveRuntimeServiceDotEnv(workspaceRoot, dotEnvPath)
	if err != nil {
		return "", err
	}
	composeYAML, err := renderServiceComposeYAML(cfg, composeDotEnvPath)
	if err != nil {
		return "", err
	}
	serviceDir := filepath.Join(workspaceRoot, "services")
	for _, dir := range []string{
		serviceDir,
		filepath.Join(serviceDir, "scripts"),
		filepath.Join(serviceDir, "dynamicconfig"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("create service directory %s: %w", dir, err)
		}
	}
	if _, err := writeFileIfMissing(dotEnvPath, defaultEnvFile(), 0o600); err != nil {
		return "", err
	}

	files := []struct {
		path string
		data string
		perm os.FileMode
	}{
		{path: filepath.Join(serviceDir, "compose.yaml"), data: composeYAML, perm: 0o644},
		{path: filepath.Join(serviceDir, "bifrost.json"), data: serviceBifrostJSON, perm: 0o644},
		{path: filepath.Join(serviceDir, "dynamicconfig", "development-sql.yml"), data: serviceTemporalDynamicConfigYAML, perm: 0o644},
		{path: filepath.Join(serviceDir, "scripts", "setup-postgres.sh"), data: serviceSetupPostgresSH, perm: 0o755},
		{path: filepath.Join(serviceDir, "scripts", "create-namespace.sh"), data: serviceCreateNamespaceSH, perm: 0o755},
		{path: filepath.Join(serviceDir, "scripts", "temporal-init-entrypoint.sh"), data: serviceTemporalInitSH, perm: 0o755},
	}
	for _, file := range files {
		if err := writeManagedServiceFile(file.path, []byte(file.data), file.perm); err != nil {
			return "", err
		}
	}
	return serviceDir, nil
}

func resolveRuntimeServiceDotEnv(workspaceRoot, dotEnvPath string) (string, string, error) {
	dotEnvPath = strings.TrimSpace(dotEnvPath)
	if dotEnvPath == "" {
		return filepath.Join(workspaceRoot, ".env"), "../.env", nil
	}
	resolved, err := filepath.Abs(dotEnvPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve .env path: %w", err)
	}
	return resolved, resolved, nil
}

func writeManagedServiceFile(path string, data []byte, perm os.FileMode) error {
	existing, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read %s: %w", path, err)
		}
	} else if bytes.Equal(existing, data) {
		if err := os.Chmod(path, perm); err != nil {
			return fmt.Errorf("chmod %s: %w", path, err)
		}
		return nil
	}

	if err := os.WriteFile(path, data, perm); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func renderServiceComposeYAML(cfg config.Config, dotEnvPath string) (string, error) {
	temporalHostPort, err := temporalComposeHostPort(cfg.Temporal.HostPort)
	if err != nil {
		return "", err
	}
	bifrostHostPort := "127.0.0.1:8081:8080"
	if cfg.LLM.Bifrost.Enabled {
		bifrostHostPort, _, err = managedBifrostComposeHostPort(cfg)
		if err != nil {
			return "", err
		}
	}
	namespace := strings.TrimSpace(cfg.Temporal.Namespace)
	if namespace == "" {
		namespace = "default"
	}
	out := strings.ReplaceAll(serviceComposeYAMLTemplate, "{{TEMPORAL_HOST_PORT}}", strconv.Quote(temporalHostPort))
	out = strings.ReplaceAll(out, "{{TEMPORAL_NAMESPACE}}", strconv.Quote(namespace))
	out = strings.ReplaceAll(out, "{{BIFROST_HOST_PORT}}", strconv.Quote(bifrostHostPort))
	out = strings.ReplaceAll(out, "{{DOT_ENV_PATH}}", strconv.Quote(dotEnvPath))
	return out, nil
}

func temporalComposeHostPort(hostPort string) (string, error) {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		hostPort = "127.0.0.1:7233"
	}
	host, port, err := net.SplitHostPort(hostPort)
	if err != nil {
		return "", fmt.Errorf("parse temporal.host_port %q: %w", hostPort, err)
	}
	port = strings.TrimSpace(port)
	if port == "" {
		return "", fmt.Errorf("temporal.host_port %q is missing a port", hostPort)
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	if host == "" {
		return port + ":7233", nil
	}
	return net.JoinHostPort(host, port) + ":7233", nil
}

func managedBifrostServiceEnabled(cfg config.Config) (bool, error) {
	if !cfg.LLM.Bifrost.Enabled {
		return false, nil
	}
	_, managed, err := managedBifrostComposeHostPort(cfg)
	return managed, err
}

func managedBifrostComposeHostPort(cfg config.Config) (string, bool, error) {
	hostPort, managed, err := managedBifrostHostPort(cfg)
	if err != nil {
		return "", false, err
	}
	if !managed {
		return "127.0.0.1:8081:8080", false, nil
	}
	return hostPort + ":8080", true, nil
}

func managedBifrostHostPort(cfg config.Config) (string, bool, error) {
	baseURL := strings.TrimSpace(cfg.LLM.Bifrost.BaseURL)
	if baseURL == "" {
		baseURL = defaultManagedBifrostOpenAIBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", false, fmt.Errorf("parse llm.bifrost.base_url %q: %w", baseURL, err)
	}
	if !strings.EqualFold(parsed.Scheme, "http") {
		return "", false, nil
	}
	host := strings.TrimSpace(parsed.Hostname())
	port := strings.TrimSpace(parsed.Port())
	if strings.Trim(parsed.EscapedPath(), "/") != "openai" || host == "" || port == "" {
		return "", false, nil
	}
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	} else if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return "", false, nil
	}
	return net.JoinHostPort(host, port), true, nil
}

func runDockerCompose(ctx context.Context, serviceDir string, args []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = serviceDir
	var output bytes.Buffer
	if out == nil {
		cmd.Stdout = &output
		cmd.Stderr = &output
	} else {
		writer := io.MultiWriter(out, &output)
		cmd.Stdout = writer
		cmd.Stderr = writer
	}
	err := cmd.Run()
	if err == nil {
		return nil
	}
	detail := strings.TrimSpace(output.String())
	if detail == "" {
		detail = err.Error()
	}
	return fmt.Errorf("docker %s failed: %s", strings.Join(args, " "), detail)
}

func waitForRuntimeServices(ctx context.Context, cfg config.Config, waitForBifrost bool, progress io.Writer) error {
	waitCtx, cancel := context.WithTimeout(ctx, serviceWaitTimeout)
	defer cancel()

	var last []doctorResult
	nextProgress := time.Now()
	writeServiceProgress(progress, "Waiting for Temporal at %s", cfg.Temporal.HostPort)
	for {
		last = checkTemporal(waitCtx, cfg)
		if waitForBifrost {
			last = append(last, checkManagedBifrost(waitCtx, cfg))
		}
		if doctorResultsOK(last) {
			return nil
		}
		if time.Now().After(nextProgress) {
			writeServiceProgress(progress, "Still waiting: %s", summarizeDoctorFailures(last))
			nextProgress = time.Now().Add(10 * time.Second)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("OpenCTO services did not become ready: %s\nCheck logs with: docker compose -f %s logs", summarizeDoctorFailures(last), filepath.Join(cfg.General.WorkspaceRoot, "services", "compose.yaml"))
		case <-time.After(2 * time.Second):
		}
	}
}

func checkManagedBifrost(ctx context.Context, cfg config.Config) doctorResult {
	hostPort, managed, err := managedBifrostHostPort(cfg)
	if err != nil {
		return doctorResult{status: doctorFail, name: "bifrost", detail: err.Error()}
	}
	if !managed {
		return doctorResult{status: doctorOK, name: "bifrost", detail: "unmanaged"}
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return doctorResult{status: doctorFail, name: "bifrost", detail: fmt.Sprintf("connect %s: %v", hostPort, err)}
	}
	_ = conn.Close()
	return doctorResult{status: doctorOK, name: "bifrost", detail: hostPort}
}

func doctorResultsOK(results []doctorResult) bool {
	for _, result := range results {
		if result.status != doctorOK {
			return false
		}
	}
	return true
}

func summarizeDoctorFailures(results []doctorResult) string {
	var parts []string
	for _, result := range results {
		if result.status == doctorOK {
			continue
		}
		parts = append(parts, result.name+": "+result.detail)
	}
	if len(parts) == 0 {
		return "unknown service readiness failure"
	}
	return strings.Join(parts, "; ")
}

const serviceComposeYAMLTemplate = `name: opencto

services:
  postgresql:
    image: postgres:18
    restart: unless-stopped
    environment:
      POSTGRES_DB: ${POSTGRES_DB:-postgres}
      POSTGRES_USER: ${POSTGRES_USER:-temporal}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-temporal}
    networks:
      - opencto-network
    volumes:
      - postgresql_data:/var/lib/postgresql
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-temporal} -d ${POSTGRES_DB:-postgres}"]
      interval: 5s
      timeout: 5s
      retries: 60
      start_period: 10s

  temporal-admin-tools:
    image: temporalio/admin-tools:latest
    restart: on-failure:6
    depends_on:
      postgresql:
        condition: service_healthy
    environment:
      DB: postgres12
      DB_PORT: ${POSTGRES_PORT:-5432}
      POSTGRES_USER: ${POSTGRES_USER:-temporal}
      POSTGRES_PWD: ${POSTGRES_PASSWORD:-temporal}
      POSTGRES_SEEDS: ${POSTGRES_HOST:-postgresql}
      SQL_PLUGIN: postgres12
    networks:
      - opencto-network
    volumes:
      - ./scripts:/scripts:ro
    entrypoint: ["/bin/sh", "/scripts/setup-postgres.sh"]

  temporal:
    image: temporalio/server:latest
    restart: unless-stopped
    depends_on:
      temporal-admin-tools:
        condition: service_completed_successfully
    environment:
      DB: postgres12
      DB_PORT: ${POSTGRES_PORT:-5432}
      POSTGRES_USER: ${POSTGRES_USER:-temporal}
      POSTGRES_PWD: ${POSTGRES_PASSWORD:-temporal}
      POSTGRES_SEEDS: ${POSTGRES_HOST:-postgresql}
      BIND_ON_IP: 0.0.0.0
      DBNAME: opencto
      VISIBILITY_DBNAME: temporal_visibility
      DYNAMIC_CONFIG_FILE_PATH: /etc/temporal/config/dynamicconfig/development-sql.yml
      TEMPORAL_CLI_ADDRESS: temporal:7233
    networks:
      - opencto-network
    ports:
      - {{TEMPORAL_HOST_PORT}}
    volumes:
      - ./dynamicconfig:/etc/temporal/config/dynamicconfig:ro
    healthcheck:
      test: ["CMD", "nc", "-z", "localhost", "7233"]
      interval: 5s
      timeout: 3s
      start_period: 20s
      retries: 60

  temporal-create-namespace:
    image: temporalio/admin-tools:latest
    restart: on-failure:5
    depends_on:
      temporal:
        condition: service_healthy
    environment:
      TEMPORAL_ADDRESS: temporal:7233
      DEFAULT_NAMESPACE: {{TEMPORAL_NAMESPACE}}
      TEMPORAL_NAMESPACE_RETENTION: 24h
    networks:
      - opencto-network
    volumes:
      - ./scripts:/scripts:ro
    entrypoint: ["/bin/sh", "/scripts/create-namespace.sh"]

  temporal-init:
    image: temporalio/admin-tools:latest
    restart: on-failure:5
    depends_on:
      temporal-create-namespace:
        condition: service_completed_successfully
    environment:
      TEMPORAL_ADDRESS: temporal:7233
      DEFAULT_NAMESPACE: {{TEMPORAL_NAMESPACE}}
      TEMPORAL_SEARCH_ATTRIBUTES: opencto_project_id
    networks:
      - opencto-network
    volumes:
      - ./scripts:/scripts:ro
    entrypoint: ["/bin/sh", "/scripts/temporal-init-entrypoint.sh"]

  bifrost:
    profiles: ["bifrost"]
    image: maximhq/bifrost:latest
    restart: unless-stopped
    depends_on:
      postgresql:
        condition: service_healthy
    env_file:
      - {{DOT_ENV_PATH}}
    volumes:
      - ./bifrost.json:/app/data/config.json:ro
    networks:
      - opencto-network
    ports:
      - {{BIFROST_HOST_PORT}}

networks:
  opencto-network:
    driver: bridge
    name: opencto-network

volumes:
  postgresql_data:
`

const serviceTemporalDynamicConfigYAML = `limit.blobSize:
  - value: 1048576
    constraints: {}
limit.maxIDLength:
  - value: 255
    constraints: {}
system.forceSearchAttributesCacheRefreshOnRead:
  - value: true
    constraints: {}
frontend.enableUpdateWorkflowExecution:
  - value: true
    constraints: {}
`

const serviceSetupPostgresSH = `#!/bin/sh
set -eu

: "${POSTGRES_SEEDS:?ERROR: POSTGRES_SEEDS environment variable is required}"
: "${POSTGRES_USER:?ERROR: POSTGRES_USER environment variable is required}"
: "${POSTGRES_PWD:?ERROR: POSTGRES_PWD environment variable is required}"

SQL_PLUGIN="${SQL_PLUGIN:-postgres12}"
SQL_PORT="${DB_PORT:-5432}"

run_sql_tool() {
  database="$1"
  shift

  temporal-sql-tool \
    --plugin "${SQL_PLUGIN}" \
    --ep "${POSTGRES_SEEDS}" \
    -u "${POSTGRES_USER}" \
    --pw "${POSTGRES_PWD}" \
    -p "${SQL_PORT}" \
    --db "${database}" \
    "$@"
}

is_existing_schema_error() {
  printf '%s\n' "$1" | grep -Eiq 'already exists|duplicate key value violates unique constraint'
}

run_sql_tool_idempotent() {
  output="$(run_sql_tool "$@" 2>&1)" && {
    [ -z "$output" ] || printf '%s\n' "$output"
    return 0
  }

  if is_existing_schema_error "$output"; then
    printf '%s\n' "$output"
    return 0
  fi

  printf '%s\n' "$output" >&2
  return 1
}

nc -z -w 10 "${POSTGRES_SEEDS}" "${SQL_PORT}"

run_sql_tool_idempotent opencto --quiet create
run_sql_tool_idempotent opencto --quiet setup-schema -v 0.0
run_sql_tool opencto update-schema -d /etc/temporal/schema/postgresql/v12/temporal/versioned

run_sql_tool_idempotent temporal_visibility --quiet create
run_sql_tool_idempotent temporal_visibility --quiet setup-schema -v 0.0
run_sql_tool temporal_visibility update-schema -d /etc/temporal/schema/postgresql/v12/visibility/versioned
`

const serviceCreateNamespaceSH = `#!/bin/sh
set -eu

namespace="${DEFAULT_NAMESPACE:-default}"
retention="${TEMPORAL_NAMESPACE_RETENTION:-24h}"

if temporal operator namespace describe --namespace "${namespace}" >/dev/null 2>&1; then
  exit 0
fi

temporal operator namespace create \
  --namespace "${namespace}" \
  --retention "${retention}"
`

const serviceTemporalInitSH = `#!/bin/sh
set -eu

attributes="${TEMPORAL_SEARCH_ATTRIBUTES:-opencto_project_id}"
old_ifs="$IFS"
IFS=','
set -- ${attributes}
IFS="$old_ifs"

for attribute in "$@"; do
  while true; do
    output="$(temporal operator search-attribute create --name "${attribute}" --type Keyword 2>&1)" && break
    echo "${output}" | grep -qi "already exists" && break
    sleep 1
  done
done
`

const serviceBifrostJSON = `{
  "$schema": "https://www.getbifrost.ai/schema",
  "version": 2,
  "client": {
    "drop_excess_requests": false,
    "enable_logging": true,
    "enforce_auth_on_inference": true
  },
  "providers": {
    "openai": {
      "keys": [
        {
          "id": "openai-primary",
          "name": "openai-primary",
          "value": "env.OPENAI_API_KEY",
          "models": [
            "gpt-5.5",
            "gpt-5.4",
            "gpt-5.4-mini",
            "gpt-5.4-nano",
            "text-embedding-3-small",
            "gpt-4o-mini-transcribe"
          ],
          "weight": 1.0
        }
      ]
    }
  },
  "governance": {
    "virtual_keys": [
      {
        "id": "opencto-local",
        "name": "opencto-local",
        "value": "env.BIFROST_API_KEY",
        "is_active": true,
        "provider_configs": [
          {
            "provider": "openai",
            "allowed_models": ["*"],
            "key_ids": ["openai-primary"],
            "weight": 1.0
          }
        ]
      }
    ]
  },
  "config_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "env.POSTGRES_HOST",
      "port": "env.POSTGRES_PORT",
      "user": "env.POSTGRES_USER",
      "password": "env.POSTGRES_PASSWORD",
      "db_name": "env.POSTGRES_DB",
      "ssl_mode": "disable"
    }
  },
  "logs_store": {
    "enabled": true,
    "type": "postgres",
    "config": {
      "host": "env.POSTGRES_HOST",
      "port": "env.POSTGRES_PORT",
      "user": "env.POSTGRES_USER",
      "password": "env.POSTGRES_PASSWORD",
      "db_name": "env.POSTGRES_DB",
      "ssl_mode": "disable"
    }
  }
}
`
