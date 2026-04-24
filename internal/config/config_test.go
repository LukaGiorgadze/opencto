package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadAppliesDefaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[project]
workspace_root = "."
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Project.ID != "default" {
		t.Fatalf("unexpected project id: %s", cfg.Project.ID)
	}
	if cfg.Memory.BusyTimeout != 5*time.Second {
		t.Fatalf("unexpected busy timeout: %s", cfg.Memory.BusyTimeout)
	}
	if cfg.LLM.EmbeddingModel != "text-embedding-3-small" {
		t.Fatalf("unexpected embedding model: %s", cfg.LLM.EmbeddingModel)
	}
	if cfg.LLM.EmbeddingDimensions != 1536 {
		t.Fatalf("unexpected embedding dimensions: %d", cfg.LLM.EmbeddingDimensions)
	}
	if cfg.Temporal.TaskQueue != "opencto" {
		t.Fatalf("unexpected task queue: %s", cfg.Temporal.TaskQueue)
	}
}

func TestLoadParsesBusyTimeoutDuration(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[project]
workspace_root = "."

[memory]
busy_timeout = "7s"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Memory.BusyTimeout != 7*time.Second {
		t.Fatalf("unexpected busy timeout: %s", cfg.Memory.BusyTimeout)
	}
}

func TestLoadParsesLLMSecretFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[project]
workspace_root = "."

[llm]
api_key = "test-key"
embedding_model = "text-embedding-3-large"
embedding_dimensions = 1024
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.LLM.APIKey != "test-key" {
		t.Fatalf("unexpected api key: %s", cfg.LLM.APIKey)
	}
	if cfg.LLM.EmbeddingModel != "text-embedding-3-large" {
		t.Fatalf("unexpected embedding model: %s", cfg.LLM.EmbeddingModel)
	}
	if cfg.LLM.EmbeddingDimensions != 1024 {
		t.Fatalf("unexpected embedding dimensions: %d", cfg.LLM.EmbeddingDimensions)
	}
}
