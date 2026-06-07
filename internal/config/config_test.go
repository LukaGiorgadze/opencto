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
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {}
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected missing required config values error")
	}

	for _, field := range []string{
		"llm.provider",
		"llm.base_url",
		"llm.model_reasoning",
		"llm.model_fast",
		"llm.model_summary",
		"llm.model_transcription",
		"temporal.host_port",
		"temporal.namespace",
		"temporal.task_queue",
		"observability.log_level",
	} {
		if !strings.Contains(err.Error(), field) {
			t.Fatalf("expected error to mention %q, got %v", field, err)
		}
	}
}

func TestWithWorkspaceRootDerivesRuntimeStateDir(t *testing.T) {
	t.Parallel()

	cfg, err := WithWorkspaceRoot(Config{}, "$HOME/.opencto")
	if err != nil {
		t.Fatalf("apply workspace root: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	want := filepath.Join(home, ".opencto", ".state")
	if cfg.Runtime.StateDir != want {
		t.Fatalf("expected runtime state dir %q, got %q", want, cfg.Runtime.StateDir)
	}
}

func TestWithWorkspaceRootExpandsHome(t *testing.T) {
	t.Parallel()

	cfg, err := WithWorkspaceRoot(Config{}, "$HOME/.opencto")
	if err != nil {
		t.Fatalf("apply workspace root: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	want := filepath.Join(home, ".opencto")
	if cfg.General.WorkspaceRoot != want {
		t.Fatalf("expected workspace root %q, got %q", want, cfg.General.WorkspaceRoot)
	}
}

func TestLoadResolvesRelativeStateDirFromConfigDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "runtime": {
    "state_dir": "state"
  },
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if want := filepath.Join(dir, "state"); cfg.Runtime.StateDir != want {
		t.Fatalf("expected state dir %q, got %q", want, cfg.Runtime.StateDir)
	}
}

func TestLoadParsesLLMSecretFields(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "api_key": "test-key",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe",
    "bifrost": {
      "enabled": true,
      "base_url": "http://127.0.0.1:8081/openai"
    }
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
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
	if cfg.LLM.BaseURL != "http://127.0.0.1:8081/openai" {
		t.Fatalf("unexpected llm base url: %s", cfg.LLM.BaseURL)
	}
	if cfg.LLM.ModelTranscription != "gpt-4o-mini-transcribe" {
		t.Fatalf("unexpected transcription model: %s", cfg.LLM.ModelTranscription)
	}
	if cfg.LLM.ModelSummary != "gpt-5.4-mini" {
		t.Fatalf("expected summary model to default to fast model, got %s", cfg.LLM.ModelSummary)
	}
	if !cfg.LLM.Bifrost.Enabled {
		t.Fatal("expected bifrost to be enabled")
	}
	if cfg.LLM.Bifrost.BaseURL != "http://127.0.0.1:8081/openai" {
		t.Fatalf("unexpected bifrost base url: %s", cfg.LLM.Bifrost.BaseURL)
	}
}

func TestLoadRoutesBifrostDefaultBaseURLToLocalGateway(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "llm": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe",
    "bifrost": {
      "enabled": true
    }
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.LLM.BaseURL != "http://127.0.0.1:8081/openai" {
		t.Fatalf("expected Bifrost base URL, got %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Bifrost.BaseURL != "http://127.0.0.1:8081/openai" {
		t.Fatalf("expected Bifrost config base URL default, got %q", cfg.LLM.Bifrost.BaseURL)
	}
}

func TestLoadAllowsCustomEnabledBifrostBaseURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "llm": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe",
    "bifrost": {
      "enabled": true,
      "base_url": "https://bifrost.example.com/openai"
    }
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.LLM.BaseURL != "https://bifrost.example.com/openai" {
		t.Fatalf("expected custom Bifrost base URL, got %q", cfg.LLM.BaseURL)
	}
	if cfg.LLM.Bifrost.BaseURL != "https://bifrost.example.com/openai" {
		t.Fatalf("expected custom Bifrost config base URL, got %q", cfg.LLM.Bifrost.BaseURL)
	}
}

func TestLoadRejectsEnabledBifrostWithInvalidBaseURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "llm": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe",
    "bifrost": {
      "enabled": true,
      "base_url": "/openai"
    }
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected invalid bifrost base url error")
	}
	if !strings.Contains(err.Error(), "llm.bifrost.base_url") {
		t.Fatalf("expected invalid bifrost base url error, got %v", err)
	}
}

func TestLoadRejectsDisabledBifrostWithLocalBifrostBaseURL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081/openai",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe",
    "bifrost": {
      "enabled": false
    }
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected disabled bifrost base url error")
	}
	if !strings.Contains(err.Error(), "llm.bifrost.enabled is false") {
		t.Fatalf("expected disabled bifrost base url error, got %v", err)
	}
}

func TestLoadDefaultsStorageAndMemory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Storage.Provider != "sqlite" {
		t.Fatalf("expected sqlite storage provider, got %q", cfg.Storage.Provider)
	}
	if !cfg.Memory.Enabled || cfg.Memory.AutoContextLimit != 5 {
		t.Fatalf("unexpected memory defaults: %#v", cfg.Memory)
	}
	if !cfg.Memory.Embedding.Enabled || cfg.Memory.Embedding.Provider != "openai" || cfg.Memory.Embedding.Model != "text-embedding-3-small" || cfg.Memory.Embedding.Dimensions != 1536 {
		t.Fatalf("unexpected memory embedding defaults: %#v", cfg.Memory.Embedding)
	}
	if !cfg.Conversation.Enabled || cfg.Conversation.HistoryLimit != 20 || cfg.Conversation.MaxContextChars != 20000 ||
		!cfg.Conversation.SummaryEnabled || cfg.Conversation.SummaryTriggerChars != 24000 ||
		cfg.Conversation.SummaryMaxChars != 6000 || cfg.Conversation.SummaryRecentMessages != 10 {
		t.Fatalf("unexpected conversation defaults: %#v", cfg.Conversation)
	}
}

func TestLoadParsesMemoryEmbeddingConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
	"memory": {
	    "enabled": true,
	    "auto_context_limit": 8,
	    "embedding": {
      "enabled": false,
      "provider": "openai",
      "model": "text-embedding-3-small",
      "dimensions": 1536
    }
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Memory.AutoContextLimit != 8 || cfg.Memory.Embedding.Enabled {
		t.Fatalf("unexpected memory config: %#v", cfg.Memory)
	}
	if cfg.Memory.Embedding.Provider != "openai" || cfg.Memory.Embedding.Model != "text-embedding-3-small" || cfg.Memory.Embedding.Dimensions != 1536 {
		t.Fatalf("unexpected memory embedding config: %#v", cfg.Memory.Embedding)
	}
}

func TestLoadParsesConversationConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "conversation": {
    "enabled": false,
    "history_limit": 12,
    "max_context_chars": 4000,
    "summary_enabled": false,
    "summary_trigger_chars": 5000,
    "summary_max_chars": 2000,
    "summary_recent_messages": 7
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Conversation.Enabled || cfg.Conversation.HistoryLimit != 12 || cfg.Conversation.MaxContextChars != 4000 ||
		cfg.Conversation.SummaryEnabled || cfg.Conversation.SummaryTriggerChars != 5000 ||
		cfg.Conversation.SummaryMaxChars != 2000 || cfg.Conversation.SummaryRecentMessages != 7 {
		t.Fatalf("unexpected conversation config: %#v", cfg.Conversation)
	}
}

func TestLoadParsesDiscordOutboundAttachmentLimits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
	{
	  "project": {
	    "id": "default",
	    "name": "OpenCTO"
	  },
	  "llm": {
	    "provider": "openai",
	    "base_url": "http://127.0.0.1:8081",
	    "model_reasoning": "gpt-5.4",
	    "model_fast": "gpt-5.4-mini",
	    "model_transcription": "gpt-4o-mini-transcribe"
	  },
	  "temporal": {
	    "host_port": "127.0.0.1:7233",
	    "namespace": "default",
	    "task_queue": "opencto",
	    "continue_as_new_after_events": 1000
	  },
	  "channels": {
	    "discord": {
	      "enabled": true,
	      "outbound_messages": {
	        "max_chars": 1500
	      },
	      "outbound_attachments": {
	        "max_files": 4,
	        "max_file_bytes": 1024,
	        "max_total_bytes": 4096
	      }
	    }
	  },
	  "observability": {
	    "log_level": "INFO"
	  }
	}
	`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	messageLimits := cfg.Channels.Discord.OutboundMessages
	if messageLimits.MaxChars != 1500 {
		t.Fatalf("unexpected message limits: %#v", messageLimits)
	}
	limits := cfg.Channels.Discord.OutboundAttachments
	if limits.MaxFiles != 4 || limits.MaxFileBytes != 1024 || limits.MaxTotalBytes != 4096 {
		t.Fatalf("unexpected limits: %#v", limits)
	}
}

func TestLoadDefaultsDiscordOutboundMessageLimits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Channels.Discord.OutboundMessages.MaxChars != 2000 {
		t.Fatalf("unexpected message limits: %#v", cfg.Channels.Discord.OutboundMessages)
	}
}

func TestLoadParsesTelegramConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "channels": {
    "telegram": {
      "enabled": true,
      "webhook": {
        "url": "https://example.com/opencto",
        "listen_addr": "127.0.0.1:9090",
        "path": "opencto",
        "max_connections": 20,
        "drop_pending_updates": true
      },
      "outbound_messages": {
        "max_chars": 3000
      },
      "outbound_attachments": {
        "max_files": 4,
        "max_file_bytes": 1024,
        "max_total_bytes": 4096
      }
    }
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.Channels.Telegram.Enabled {
		t.Fatalf("expected telegram enabled: %#v", cfg.Channels.Telegram)
	}
	if cfg.Channels.Telegram.Webhook.URL != "https://example.com/opencto" ||
		cfg.Channels.Telegram.Webhook.ListenAddr != "127.0.0.1:9090" ||
		cfg.Channels.Telegram.Webhook.Path != "/opencto" ||
		cfg.Channels.Telegram.Webhook.MaxConnections != 20 ||
		!cfg.Channels.Telegram.Webhook.DropPendingUpdates {
		t.Fatalf("unexpected webhook config: %#v", cfg.Channels.Telegram.Webhook)
	}
	if cfg.Channels.Telegram.OutboundMessages.MaxChars != 3000 {
		t.Fatalf("unexpected message limits: %#v", cfg.Channels.Telegram.OutboundMessages)
	}
	limits := cfg.Channels.Telegram.OutboundAttachments
	if limits.MaxFiles != 4 || limits.MaxFileBytes != 1024 || limits.MaxTotalBytes != 4096 {
		t.Fatalf("unexpected attachment limits: %#v", limits)
	}
}

func TestLoadIgnoresInvalidTelegramWebhookWhenDisabled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "https://api.openai.com/v1",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "channels": {
    "telegram": {
      "enabled": false,
      "webhook": {
        "url": "http://example.com/opencto"
      }
    }
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	if _, err := Load(path); err != nil {
		t.Fatalf("load config: %v", err)
	}
}

func TestLoadRejectsInvalidDiscordOutboundMessageLimits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 1000
  },
  "channels": {
    "discord": {
      "outbound_messages": {
        "max_chars": 2001
      }
    }
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected message limit validation error")
	}
	if !strings.Contains(err.Error(), "channels.discord.outbound_messages.max_chars must be between 1 and 2000") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRejectsNonPositiveTemporalConfigValues(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	data := []byte(`
{
  "project": {
    "id": "default",
    "name": "OpenCTO"
  },
  "llm": {
    "provider": "openai",
    "base_url": "http://127.0.0.1:8081",
    "model_reasoning": "gpt-5.4",
    "model_fast": "gpt-5.4-mini",
    "model_transcription": "gpt-4o-mini-transcribe"
  },
  "temporal": {
    "host_port": "127.0.0.1:7233",
    "namespace": "default",
    "task_queue": "opencto",
    "continue_as_new_after_events": 0
  },
  "observability": {
    "log_level": "INFO"
  }
}
`)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected numeric validation error")
	}
	if !strings.Contains(err.Error(), "temporal.continue_as_new_after_events must be greater than 0") {
		t.Fatalf("unexpected error: %v", err)
	}
}
