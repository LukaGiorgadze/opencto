package llm

import (
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/embeddings"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/config"
)

type APIKeySource string

const (
	APIKeySourceConfig       APIKeySource = "config"
	APIKeySourceEnvironment  APIKeySource = "environment"
	APIKeySourceLegacyConfig APIKeySource = "legacy_api_key_env"
)

func ResolveOpenAIAPIKey(cfg config.LLMConfig) (string, APIKeySource, error) {
	if value := strings.TrimSpace(cfg.APIKey); value != "" {
		return value, APIKeySourceConfig, nil
	}

	name := strings.TrimSpace(cfg.APIKeyEnv)
	if name == "" {
		return "", "", fmt.Errorf("llm.api_key_env is empty")
	}

	if strings.HasPrefix(name, "sk-") {
		return name, APIKeySourceLegacyConfig, nil
	}

	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", name)
	}
	return value, APIKeySourceEnvironment, nil
}

func NewOpenAIEmbedder(apiKey, baseURL, embeddingModel string, dimensions int) (*embeddings.EmbedderImpl, error) {
	options := []openai.Option{
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithEmbeddingModel(embeddingModel),
	}
	if dimensions > 0 {
		options = append(options, openai.WithEmbeddingDimensions(dimensions))
	}

	model, err := openai.New(options...)
	if err != nil {
		return nil, err
	}

	return embeddings.NewEmbedder(model)
}
