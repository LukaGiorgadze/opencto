package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/opencto/opencto/internal/workspace"
)

const (
	EnvOpenCTOWorkspace = "OPENCTO_WORKSPACE"
)

type Config struct {
	Project       ProjectConfig       `json:"project"`
	Runtime       RuntimeConfig       `json:"runtime"`
	LLM           LLMConfig           `json:"llm"`
	Temporal      TemporalConfig      `json:"temporal"`
	Channels      ChannelsConfig      `json:"channels"`
	Observability ObservabilityConfig `json:"observability"`
}

type fileConfig struct {
	Project       ProjectConfig       `json:"project"`
	Runtime       RuntimeConfig       `json:"runtime"`
	LLM           llmFileConfig       `json:"llm"`
	Temporal      TemporalConfig      `json:"temporal"`
	Channels      ChannelsConfig      `json:"channels"`
	Observability ObservabilityConfig `json:"observability"`
}

type llmFileConfig struct {
	Provider           string `json:"provider"`
	BaseURL            string `json:"base_url"`
	ModelReasoning     string `json:"model_reasoning"`
	ModelFast          string `json:"model_fast"`
	TranscriptionModel string `json:"transcription_model"`
	APIKey             string `json:"api_key"`
}

type ProjectConfig struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	WorkspaceRoot string `json:"workspace_root"`
}

type RuntimeConfig struct {
	StateDir string `json:"state_dir"`
}

type LLMConfig struct {
	Provider           string `json:"provider"`
	BaseURL            string `json:"base_url"`
	ModelReasoning     string `json:"model_reasoning"`
	ModelFast          string `json:"model_fast"`
	TranscriptionModel string `json:"transcription_model"`
	APIKey             string `json:"api_key"`
}

type TemporalConfig struct {
	HostPort                 string `json:"host_port"`
	Namespace                string `json:"namespace"`
	TaskQueue                string `json:"task_queue"`
	ContinueAsNewAfterEvents int    `json:"continue_as_new_after_events"`
}

type ChannelsConfig struct {
	Discord DiscordConfig `json:"discord"`
}

type DiscordConfig struct {
	Enabled bool `json:"enabled"`
}

type ObservabilityConfig struct {
	LogLevel string `json:"log_level"`
	JSONLogs bool   `json:"json_logs"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Project: raw.Project,
		Runtime: raw.Runtime,
		LLM: LLMConfig{
			Provider:           raw.LLM.Provider,
			BaseURL:            raw.LLM.BaseURL,
			ModelReasoning:     raw.LLM.ModelReasoning,
			ModelFast:          raw.LLM.ModelFast,
			TranscriptionModel: raw.LLM.TranscriptionModel,
			APIKey:             raw.LLM.APIKey,
		},
		Temporal:      raw.Temporal,
		Channels:      raw.Channels,
		Observability: raw.Observability,
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	cfg.Project.WorkspaceRoot, err = workspace.ResolveRoot(cfg.Project.WorkspaceRoot)
	if err != nil {
		return Config{}, err
	}
	cfg.Runtime.StateDir, err = workspace.ResolveStateDir(cfg.Runtime.StateDir, cfg.Project.WorkspaceRoot)
	if err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string

	requireString := func(value, field string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field)
		}
	}

	requireString(c.Project.ID, "project.id")
	requireString(c.Project.Name, "project.name")
	requireString(c.Project.WorkspaceRoot, "project.workspace_root")
	requireString(c.LLM.Provider, "llm.provider")
	requireString(c.LLM.BaseURL, "llm.base_url")
	requireString(c.LLM.ModelReasoning, "llm.model_reasoning")
	requireString(c.LLM.ModelFast, "llm.model_fast")
	requireString(c.LLM.TranscriptionModel, "llm.transcription_model")
	requireString(c.Temporal.HostPort, "temporal.host_port")
	requireString(c.Temporal.Namespace, "temporal.namespace")
	requireString(c.Temporal.TaskQueue, "temporal.task_queue")
	requireString(c.Observability.LogLevel, "observability.log_level")

	var errs []error
	if len(missing) > 0 {
		errs = append(errs, errors.New("missing required config values: "+strings.Join(missing, ", ")))
	}
	if c.Temporal.ContinueAsNewAfterEvents <= 0 {
		errs = append(errs, errors.New("temporal.continue_as_new_after_events must be greater than 0"))
	}

	return errors.Join(errs...)
}
