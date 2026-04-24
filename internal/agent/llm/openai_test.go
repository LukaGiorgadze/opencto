package llm

import (
	"testing"

	"github.com/opencto/opencto/internal/config"
)

func TestResolveOpenAIAPIKeyFromConfig(t *testing.T) {
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		APIKey:    "direct-key",
		APIKeyEnv: "OPENAI_API_KEY",
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
	t.Setenv("OPENAI_TEST_KEY", "env-key")
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		APIKeyEnv: "OPENAI_TEST_KEY",
	})
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

func TestResolveOpenAIAPIKeyFromLegacyAPIKeyEnvField(t *testing.T) {
	key, source, err := ResolveOpenAIAPIKey(config.LLMConfig{
		APIKeyEnv: "sk-test-key",
	})
	if err != nil {
		t.Fatalf("resolve api key: %v", err)
	}
	if key != "sk-test-key" {
		t.Fatalf("unexpected key: %s", key)
	}
	if source != APIKeySourceLegacyConfig {
		t.Fatalf("unexpected source: %s", source)
	}
}
