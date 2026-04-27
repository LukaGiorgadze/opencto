package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRequiresExplicitConfigValues(t *testing.T) {
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

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected missing required config values error")
	}

	for _, field := range []string{
		"project.id",
		"project.name",
		"llm.provider",
		"llm.base_url",
		"llm.model_reasoning",
		"llm.model_fast",
		"llm.transcription_model",
		"llm.embedding_model",
		"temporal.host_port",
		"temporal.namespace",
		"temporal.task_queue",
		"vault.provider",
		"vault.service",
		"observability.log_level",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("expected error to mention %q, got %v", field, err)
		}
	}
}

func TestLoadParsesLLMSecretFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
data := []byte(`
[project]
id = "default"
name = "OpenCTO"
workspace_root = "."

[llm]
provider = "openai"
api_key = "test-key"
base_url = "http://127.0.0.1:4000"
model_reasoning = "gpt-5.4"
model_fast = "gpt-5.4-mini"
transcription_model = "gpt-4o-mini-transcribe"
embedding_model = "text-embedding-3-large"
embedding_dimensions = 1024

[temporal]
host_port = "127.0.0.1:7233"
namespace = "default"
task_queue = "opencto"
continue_as_new_after_events = 1000

[vault]
provider = "keyring"
service = "opencto"

[observability]
log_level = "INFO"
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
	if cfg.LLM.BaseURL != "http://127.0.0.1:4000" {
		t.Fatalf("unexpected llm base url: %s", cfg.LLM.BaseURL)
	}
	if cfg.LLM.TranscriptionModel != "gpt-4o-mini-transcribe" {
		t.Fatalf("unexpected transcription model: %s", cfg.LLM.TranscriptionModel)
	}
	if cfg.LLM.EmbeddingModel != "text-embedding-3-large" {
		t.Fatalf("unexpected embedding model: %s", cfg.LLM.EmbeddingModel)
	}
	if cfg.LLM.EmbeddingDimensions != 1024 {
		t.Fatalf("unexpected embedding dimensions: %d", cfg.LLM.EmbeddingDimensions)
	}
}

func TestLoadRejectsNonPositiveNumericConfigValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte(`
[project]
id = "default"
name = "OpenCTO"
workspace_root = "."

[llm]
provider = "openai"
base_url = "http://127.0.0.1:4000"
model_reasoning = "gpt-5.4"
model_fast = "gpt-5.4-mini"
transcription_model = "gpt-4o-mini-transcribe"
embedding_model = "text-embedding-3-large"
embedding_dimensions = 0

[temporal]
host_port = "127.0.0.1:7233"
namespace = "default"
task_queue = "opencto"
continue_as_new_after_events = 0

[vault]
provider = "keyring"
service = "opencto"

[observability]
log_level = "INFO"
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected numeric validation error")
	}
	if !strings.Contains(err.Error(), "llm.embedding_dimensions must be greater than 0") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "temporal.continue_as_new_after_events must be greater than 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}
