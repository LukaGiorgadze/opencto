package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Project       ProjectConfig       `toml:"project"`
	Memory        MemoryConfig        `toml:"memory"`
	LLM           LLMConfig           `toml:"llm"`
	Temporal      TemporalConfig      `toml:"temporal"`
	Channels      ChannelsConfig      `toml:"channels"`
	Vault         VaultConfig         `toml:"vault"`
	Observability ObservabilityConfig `toml:"observability"`
}

type fileConfig struct {
	Project       ProjectConfig       `toml:"project"`
	Memory        memoryFileConfig    `toml:"memory"`
	LLM           llmFileConfig       `toml:"llm"`
	Temporal      TemporalConfig      `toml:"temporal"`
	Channels      ChannelsConfig      `toml:"channels"`
	Vault         VaultConfig         `toml:"vault"`
	Observability ObservabilityConfig `toml:"observability"`
}

type memoryFileConfig struct {
	Path              string `toml:"path"`
	SQLiteVecPath     string `toml:"sqlite_vec_path"`
	SQLiteVecRequired bool   `toml:"sqlite_vec_required"`
	BusyTimeout       string `toml:"busy_timeout"`
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

type MemoryConfig struct {
	Path              string        `toml:"path"`
	SQLiteVecPath     string        `toml:"sqlite_vec_path"`
	SQLiteVecRequired bool          `toml:"sqlite_vec_required"`
	BusyTimeout       time.Duration `toml:"busy_timeout"`
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
		Memory: MemoryConfig{
			Path:              raw.Memory.Path,
			SQLiteVecPath:     raw.Memory.SQLiteVecPath,
			SQLiteVecRequired: raw.Memory.SQLiteVecRequired,
		},
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
	if raw.Memory.BusyTimeout != "" {
		duration, err := time.ParseDuration(raw.Memory.BusyTimeout)
		if err != nil {
			return Config{}, fmt.Errorf("parse memory.busy_timeout: %w", err)
		}
		cfg.Memory.BusyTimeout = duration
	}

	if err := cfg.applyDefaults(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func (c *Config) applyDefaults() error {
	if c.Project.ID == "" {
		c.Project.ID = "default"
	}
	if c.Project.Name == "" {
		c.Project.Name = "OpenCTO"
	}
	if c.Project.WorkspaceRoot == "" {
		c.Project.WorkspaceRoot = "."
	}
	if c.Memory.Path == "" {
		c.Memory.Path = filepath.Join("data", "memory.db")
	}
	if c.Memory.BusyTimeout == 0 {
		c.Memory.BusyTimeout = 5 * time.Second
	}
	if c.LLM.Provider == "" {
		c.LLM.Provider = "openai"
	}
	if c.LLM.BaseURL == "" {
		c.LLM.BaseURL = "http://127.0.0.1:4000"
	}
	if c.LLM.ModelReasoning == "" {
		c.LLM.ModelReasoning = "gpt-5.4"
	}
	if c.LLM.ModelFast == "" {
		c.LLM.ModelFast = "gpt-5.4-mini"
	}
	if c.LLM.TranscriptionModel == "" {
		c.LLM.TranscriptionModel = "gpt-4o-mini-transcribe"
	}
	if c.LLM.EmbeddingModel == "" {
		c.LLM.EmbeddingModel = "text-embedding-3-small"
	}
	if c.LLM.EmbeddingDimensions == 0 {
		c.LLM.EmbeddingDimensions = 1536
	}
	if c.Temporal.TaskQueue == "" {
		c.Temporal.TaskQueue = "opencto"
	}
	if c.Temporal.Namespace == "" {
		c.Temporal.Namespace = "default"
	}
	if c.Temporal.HostPort == "" {
		c.Temporal.HostPort = "127.0.0.1:7233"
	}
	if c.Temporal.ContinueAsNewAfterEvents == 0 {
		c.Temporal.ContinueAsNewAfterEvents = 1000
	}
	if c.Observability.LogLevel == "" {
		c.Observability.LogLevel = "INFO"
	}
	if c.Vault.Provider == "" {
		c.Vault.Provider = "keychain"
	}
	if c.Vault.Service == "" {
		c.Vault.Service = "opencto"
	}

	if c.Project.WorkspaceRoot == "" {
		return errors.New("project.workspace_root is required")
	}

	return nil
}
