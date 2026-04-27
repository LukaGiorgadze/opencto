package config

import (
	"errors"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Project       ProjectConfig       `toml:"project"`
	LLM           LLMConfig           `toml:"llm"`
	Temporal      TemporalConfig      `toml:"temporal"`
	Channels      ChannelsConfig      `toml:"channels"`
	Vault         VaultConfig         `toml:"vault"`
	Observability ObservabilityConfig `toml:"observability"`
}

type fileConfig struct {
	Project       ProjectConfig       `toml:"project"`
	LLM           llmFileConfig       `toml:"llm"`
	Temporal      TemporalConfig      `toml:"temporal"`
	Channels      ChannelsConfig      `toml:"channels"`
	Vault         VaultConfig         `toml:"vault"`
	Observability ObservabilityConfig `toml:"observability"`
}

type llmFileConfig struct {
	Provider            string `toml:"provider"`
	BaseURL             string `toml:"base_url"`
	ModelReasoning      string `toml:"model_reasoning"`
	ModelFast           string `toml:"model_fast"`
	TranscriptionModel  string `toml:"transcription_model"`
	EmbeddingModel      string `toml:"embedding_model"`
	EmbeddingDimensions int    `toml:"embedding_dimensions"`
	APIKey              string `toml:"api_key"`
}

type ProjectConfig struct {
	ID            string `toml:"id"`
	Name          string `toml:"name"`
	WorkspaceRoot string `toml:"workspace_root"`
}

type LLMConfig struct {
	Provider            string `toml:"provider"`
	BaseURL             string `toml:"base_url"`
	ModelReasoning      string `toml:"model_reasoning"`
	ModelFast           string `toml:"model_fast"`
	TranscriptionModel  string `toml:"transcription_model"`
	EmbeddingModel      string `toml:"embedding_model"`
	EmbeddingDimensions int    `toml:"embedding_dimensions"`
	APIKey              string `toml:"api_key"`
}

type TemporalConfig struct {
	HostPort                 string `toml:"host_port"`
	Namespace                string `toml:"namespace"`
	TaskQueue                string `toml:"task_queue"`
	ContinueAsNewAfterEvents int    `toml:"continue_as_new_after_events"`
}

type ChannelsConfig struct {
	Discord DiscordConfig `toml:"discord"`
}

type DiscordConfig struct {
	Enabled bool `toml:"enabled"`
}

type VaultConfig struct {
	Provider string `toml:"provider"`
	Service  string `toml:"service"`
}

type ObservabilityConfig struct {
	LogLevel string `toml:"log_level"`
	JSONLogs bool   `toml:"json_logs"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var raw fileConfig
	if err := toml.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Project: raw.Project,
		LLM: LLMConfig{
			Provider:            raw.LLM.Provider,
			BaseURL:             raw.LLM.BaseURL,
			ModelReasoning:      raw.LLM.ModelReasoning,
			ModelFast:           raw.LLM.ModelFast,
			TranscriptionModel:  raw.LLM.TranscriptionModel,
			EmbeddingModel:      raw.LLM.EmbeddingModel,
			EmbeddingDimensions: raw.LLM.EmbeddingDimensions,
			APIKey:              raw.LLM.APIKey,
		},
		Temporal:      raw.Temporal,
		Channels:      raw.Channels,
		Vault:         raw.Vault,
		Observability: raw.Observability,
	}

	if err := cfg.validate(); err != nil {
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
	requireString(c.LLM.EmbeddingModel, "llm.embedding_model")
	requireString(c.Temporal.HostPort, "temporal.host_port")
	requireString(c.Temporal.Namespace, "temporal.namespace")
	requireString(c.Temporal.TaskQueue, "temporal.task_queue")
	requireString(c.Vault.Provider, "vault.provider")
	requireString(c.Vault.Service, "vault.service")
	requireString(c.Observability.LogLevel, "observability.log_level")

	var errs []error
	if len(missing) > 0 {
		errs = append(errs, errors.New("missing required config values: "+strings.Join(missing, ", ")))
	}
	if c.LLM.EmbeddingDimensions <= 0 {
		errs = append(errs, errors.New("llm.embedding_dimensions must be greater than 0"))
	}
	if c.Temporal.ContinueAsNewAfterEvents <= 0 {
		errs = append(errs, errors.New("temporal.continue_as_new_after_events must be greater than 0"))
	}

	return errors.Join(errs...)
}
