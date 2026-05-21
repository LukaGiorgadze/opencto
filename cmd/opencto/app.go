package main

import (
	"log/slog"

	"github.com/opencto/opencto/internal/agent"
	agentllm "github.com/opencto/opencto/internal/agent/llm"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
)

var defaultProject = domain.Project{
	ID:   "default",
	Name: "OpenCTO",
}

func buildNextActionEngine(cfg config.Config, logger *slog.Logger) agent.Engine {
	unavailable := func(reason string) agent.Engine {
		return agent.NewUnavailableEngine(reason)
	}
	if cfg.LLM.Provider != "" && cfg.LLM.Provider != "openai" {
		logger.Warn("unsupported llm provider configured", slog.String("provider", cfg.LLM.Provider))
		return unavailable("unsupported llm provider: " + cfg.LLM.Provider)
	}
	apiKey, source, err := agentllm.ResolveOpenAIAPIKey(cfg.LLM)
	if err != nil {
		logger.Warn("openai api key is not configured", slog.String("error", err.Error()))
		return unavailable(err.Error())
	}
	if source == agentllm.APIKeySourceConfig {
		logger.Warn("openai api key is loaded directly from config; prefer environment variables for local and production safety")
	}

	engine, err := agentllm.NewOpenAIEngine(apiKey, cfg.LLM.BaseURL, cfg.LLM.ModelReasoning, cfg.LLM.ModelFast, cfg.LLM.ModelTranscription, cfg.LLM.Bifrost.Enabled)
	if err != nil {
		logger.Warn("failed to initialize openai next action engine", slog.String("error", err.Error()))
		return unavailable(err.Error())
	}
	logger.Info(
		"openai next action engine configured",
		slog.String("base_url", cfg.LLM.BaseURL),
		slog.String("model_reasoning", cfg.LLM.ModelReasoning),
		slog.String("model_fast", cfg.LLM.ModelFast),
		slog.String("model_transcription", cfg.LLM.ModelTranscription),
		slog.String("api_key_source", string(source)),
		slog.Bool("bifrost_enabled", cfg.LLM.Bifrost.Enabled),
	)
	return engine
}

func buildMemoryEmbedder(cfg config.Config, logger *slog.Logger) embedding.Embedder {
	if !cfg.Memory.Enabled || !cfg.Memory.Embedding.Enabled {
		return nil
	}
	if cfg.Memory.Embedding.Provider != embedding.ProviderOpenAI {
		logger.Warn("unsupported memory embedding provider configured", slog.String("provider", cfg.Memory.Embedding.Provider))
		return nil
	}
	apiKey, source, err := agentllm.ResolveOpenAIAPIKey(cfg.LLM)
	if err != nil {
		logger.Warn("openai embedding api key is not configured", slog.String("error", err.Error()))
		return nil
	}
	embedder, err := embedding.NewOpenAIEmbedder(embedding.OpenAIConfig{
		APIKey:     apiKey,
		BaseURL:    cfg.LLM.BaseURL,
		Model:      cfg.Memory.Embedding.Model,
		Dimensions: cfg.Memory.Embedding.Dimensions,
	})
	if err != nil {
		logger.Warn("failed to initialize memory embedder", slog.String("error", err.Error()))
		return nil
	}
	logger.Info(
		"memory embedder configured",
		slog.String("provider", cfg.Memory.Embedding.Provider),
		slog.String("model", cfg.Memory.Embedding.Model),
		slog.Int("dimensions", cfg.Memory.Embedding.Dimensions),
		slog.String("api_key_source", string(source)),
		slog.Bool("bifrost_enabled", cfg.LLM.Bifrost.Enabled),
	)
	return embedder
}

func buildConversationCompressor(cfg config.Config, logger *slog.Logger) agent.ConversationCompressor {
	if !cfg.Conversation.Enabled || !cfg.Conversation.SummaryEnabled {
		return nil
	}
	if cfg.LLM.Provider != "" && cfg.LLM.Provider != "openai" {
		logger.Warn("unsupported conversation compression llm provider configured", slog.String("provider", cfg.LLM.Provider))
		return nil
	}
	apiKey, source, err := agentllm.ResolveOpenAIAPIKey(cfg.LLM)
	if err != nil {
		logger.Warn("openai conversation compression api key is not configured", slog.String("error", err.Error()))
		return nil
	}
	compressor, err := agentllm.NewOpenAIConversationCompressor(apiKey, cfg.LLM.BaseURL, cfg.LLM.ModelSummary, cfg.LLM.Bifrost.Enabled)
	if err != nil {
		logger.Warn("failed to initialize conversation compressor", slog.String("error", err.Error()))
		return nil
	}
	logger.Info(
		"conversation compressor configured",
		slog.String("base_url", cfg.LLM.BaseURL),
		slog.String("model", cfg.LLM.ModelSummary),
		slog.String("api_key_source", string(source)),
		slog.Bool("bifrost_enabled", cfg.LLM.Bifrost.Enabled),
	)
	return compressor
}

func buildAgentObservationCompressor(cfg config.Config, logger *slog.Logger) agent.AgentObservationCompressor {
	if !cfg.Conversation.Enabled || !cfg.Conversation.SummaryEnabled {
		return nil
	}
	if cfg.LLM.Provider != "" && cfg.LLM.Provider != "openai" {
		logger.Warn("unsupported agent observation compression llm provider configured", slog.String("provider", cfg.LLM.Provider))
		return nil
	}
	apiKey, source, err := agentllm.ResolveOpenAIAPIKey(cfg.LLM)
	if err != nil {
		logger.Warn("openai agent observation compression api key is not configured", slog.String("error", err.Error()))
		return nil
	}
	compressor, err := agentllm.NewOpenAIAgentObservationCompressor(apiKey, cfg.LLM.BaseURL, cfg.LLM.ModelSummary, cfg.LLM.Bifrost.Enabled)
	if err != nil {
		logger.Warn("failed to initialize agent observation compressor", slog.String("error", err.Error()))
		return nil
	}
	logger.Info(
		"agent observation compressor configured",
		slog.String("base_url", cfg.LLM.BaseURL),
		slog.String("model", cfg.LLM.ModelSummary),
		slog.String("api_key_source", string(source)),
		slog.Bool("bifrost_enabled", cfg.LLM.Bifrost.Enabled),
	)
	return compressor
}
