package llm

import (
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
	t.Setenv("LITELLM_PROXY_KEY", "env-key")
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

func TestResolveOpenAIAPIKeyMissingEnvironment(t *testing.T) {
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
}
