package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/agent"
	agentllm "github.com/opencto/opencto/internal/agent/llm"
	"github.com/opencto/opencto/internal/channels/discord"
	"github.com/opencto/opencto/internal/channels/local"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/observability"
	"github.com/opencto/opencto/internal/runtime"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/tools/shell"
)

func main() {
	var (
		configPath = flag.String("config", "config.toml", "path to config file")
		mode       = flag.String("mode", "validate", "validate|worker|inject|serve")
		body       = flag.String("body", "", "event body for inject mode")
		actor      = flag.String("actor", "local-user", "actor name for inject mode")
	)
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.JSONLogs, os.Stdout)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if *mode == "validate" {
		logger.Info("configuration validated", slog.String("project_id", cfg.Project.ID))
		return
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		logger.Error("connect temporal", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer temporalClient.Close()

	dispatcher := runtime.NewDispatcher(temporalClient, cfg.Temporal.TaskQueue, cfg.Temporal.ContinueAsNewAfterEvents)
	var reporter activities.Reporter = local.NewReporter(logger)
	openAI := buildOpenAIServices(cfg, logger)

	if *mode == "worker" || *mode == "serve" {
		availableSkills := discoverAvailableSkills("skills")
		var discordAdapter *discord.Adapter
		if cfg.Channels.Discord.Enabled {
			token := strings.TrimSpace(os.Getenv("DISCORD_TOKEN"))
			appID := strings.TrimSpace(os.Getenv("DISCORD_APPLICATION_ID"))
			if token == "" {
				logger.Error("discord is enabled but the bot token is missing", slog.String("env", "DISCORD_TOKEN"))
				os.Exit(1)
			}
			if appID == "" {
				logger.Warn("discord application id is not set; continuing because the runtime does not require it yet", slog.String("env", "DISCORD_APPLICATION_ID"))
			}
			discordAdapter, err = discord.New(cfg.Project.ID, token, appID, dispatcher, logger)
			if err != nil {
				logger.Error("create discord adapter", slog.String("error", err.Error()))
				os.Exit(1)
			}
			defer discordAdapter.Close()
			reporter = discordAdapter
		}

		activitySet := &activities.Activities{
			Engine:   openAI.Engine,
			Shell:    shell.NewSafeExecutor(logger),
			Reporter: reporter,
			Project: domain.Project{
				ID:   cfg.Project.ID,
				Name: cfg.Project.Name,
			},
			WorkspaceRoot:   cfg.Project.WorkspaceRoot,
			AvailableSkills: availableSkills,
			MemoryEmbedder:  openAI.Embedder,
			EmbeddingModel:  cfg.LLM.EmbeddingModel,
			Logger:          logger,
		}

		worker := runtime.NewWorker(temporalClient, cfg.Temporal.TaskQueue, activitySet)
		if *mode == "worker" {
			if err := worker.Run(); err != nil {
				logger.Error("run worker", slog.String("error", err.Error()))
				os.Exit(1)
			}
			return
		}

		go func() {
			if err := worker.Run(); err != nil {
				logger.Error("run worker", slog.String("error", err.Error()))
				stop()
			}
		}()

		if discordAdapter != nil {
			if err := discordAdapter.Start(ctx); err != nil {
				logger.Error("start discord adapter", slog.String("error", err.Error()))
				os.Exit(1)
			}
			logger.Info("discord adapter started", slog.String("project_id", cfg.Project.ID))
		}

		<-ctx.Done()
		return
	}

	if *mode == "inject" {
		injector := local.NewInjector(cfg.Project.ID, dispatcher, logger)
		if _, err := injector.Inject(ctx, *actor, *body); err != nil {
			logger.Error("inject local event", slog.String("error", err.Error()))
			os.Exit(1)
		}
		logger.Info("event injected", slog.String("project_id", cfg.Project.ID))
		return
	}

	logger.Error("unknown mode", slog.String("mode", *mode))
	os.Exit(1)
}

func discoverAvailableSkills(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	skills := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		skills = append(skills, strings.TrimSuffix(name, ".md"))
	}
	return skills
}

func buildDecisionEngine(cfg config.Config, logger *slog.Logger) agent.Engine {
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

	engine, err := agentllm.NewOpenAIEngine(apiKey, cfg.LLM.BaseURL, cfg.LLM.ModelReasoning, cfg.LLM.ModelFast, cfg.LLM.TranscriptionModel)
	if err != nil {
		logger.Warn("failed to initialize openai decision engine", slog.String("error", err.Error()))
		return unavailable(err.Error())
	}
	logger.Info("openai decision engine configured",
		slog.String("base_url", cfg.LLM.BaseURL),
		slog.String("model_reasoning", cfg.LLM.ModelReasoning),
		slog.String("model_fast", cfg.LLM.ModelFast),
		slog.String("transcription_model", cfg.LLM.TranscriptionModel),
		slog.String("api_key_source", string(source)),
	)
	return engine
}

type openAIServices struct {
	Engine   agent.Engine
	Embedder activities.SemanticEmbedder
}

func buildOpenAIServices(cfg config.Config, logger *slog.Logger) openAIServices {
	engine := buildDecisionEngine(cfg, logger)
	if cfg.LLM.Provider != "" && cfg.LLM.Provider != "openai" {
		return openAIServices{Engine: engine}
	}

	apiKey, source, err := agentllm.ResolveOpenAIAPIKey(cfg.LLM)
	if err != nil {
		return openAIServices{Engine: engine}
	}

	embedder, err := agentllm.NewOpenAIEmbedder(apiKey, cfg.LLM.BaseURL, cfg.LLM.EmbeddingModel, cfg.LLM.EmbeddingDimensions)
	if err != nil {
		logger.Warn("failed to initialize openai memory embedder; continuing without semantic memory", slog.String("error", err.Error()))
		return openAIServices{Engine: engine}
	}

	logger.Info("openai memory embedder configured",
		slog.String("base_url", cfg.LLM.BaseURL),
		slog.String("embedding_model", cfg.LLM.EmbeddingModel),
		slog.Int("embedding_dimensions", cfg.LLM.EmbeddingDimensions),
		slog.String("api_key_source", string(source)),
	)
	return openAIServices{
		Engine:   engine,
		Embedder: embedder,
	}
}
