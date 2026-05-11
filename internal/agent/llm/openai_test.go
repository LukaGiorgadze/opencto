package llm

import (
	"strings"
	"testing"

	"github.com/opencto/opencto/internal/config"
)

func TestResolveOpenAIAPIKeyFromConfig(t *testing.T) {
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		APIKey: "direct-key",
	})
	if err != nil {
		t.Fatalf("resolve api key: %v", err)
	}
	if key != "direct-key" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != APIKeySourceConfig {
		t.Fatalf("unexpected source: %s", source)
	}
}

func TestResolveOpenAIAPIKeyFromEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "env-key")
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{})
	if err != nil {
		t.Fatalf("resolve api key: %v", err)
	}
	if key != "env-key" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != APIKeySourceEnvironment {
		t.Fatalf("unexpected source: %s", source)
	}
}

func TestResolveOpenAIAPIKeyFromBifrostEnvironment(t *testing.T) {
	t.Setenv("BIFROST_API_KEY", "bifrost-key")
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		Bifrost: config.BifrostConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("resolve api key: %v", err)
	}
	if key != "bifrost-key" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != APIKeySourceEnvironment {
		t.Fatalf("unexpected source: %s", source)
	}
}

func TestResolveOpenAIAPIKeyMissingEnvironment(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")

	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{})
	if err == nil {
		t.Fatal("expected missing env error")
	}
	if key != "" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != "" {
		t.Fatalf("unexpected source: %s", source)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Fatalf("expected missing OPENAI_API_KEY error, got %v", err)
	}
}

func TestResolveOpenAIAPIKeyMissingBifrostEnvironment(t *testing.T) {
	t.Setenv("BIFROST_API_KEY", "")

	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		Bifrost: config.BifrostConfig{Enabled: true},
	})
	if err == nil {
		t.Fatal("expected missing env error")
	}
	if key != "" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != "" {
		t.Fatalf("unexpected source: %s", source)
	}
	if !strings.Contains(err.Error(), "BIFROST_API_KEY") {
		t.Fatalf("expected missing BIFROST_API_KEY error, got %v", err)
	}
}
