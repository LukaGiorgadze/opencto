package main

import (
	"context"
	"errors"
	"fmt"

	"go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/runtime"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/storage/sqlite"
	"github.com/opencto/opencto/internal/tools/exec"
	workflowscheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

type runtimeComponents struct {
	TemporalClient client.Client
	Store          *sqlite.Store
	Reporters      channelReporterSet
	Worker         *runtime.Worker
}

func newRuntimeComponents(ctx context.Context, env commandEnvironment) (*runtimeComponents, error) {
	cfg := env.Config
	logger := env.Logger

	temporalClient, err := client.Dial(client.Options{
		HostPort:  cfg.Temporal.HostPort,
		Namespace: cfg.Temporal.Namespace,
	})
	if err != nil {
		return nil, fmt.Errorf("connect temporal: %w", err)
	}

	store, dbPath, err := openRuntimeStore(ctx, cfg)
	if err != nil {
		temporalClient.Close()
		return nil, fmt.Errorf("open sqlite store %s: %w", dbPath, err)
	}

	dispatcher := runtime.NewDispatcher(temporalClient, cfg.Temporal.TaskQueue, cfg.Temporal.ContinueAsNewAfterEvents)
	reporters, err := newConfiguredChannelReporter(cfg, dispatcher, logger)
	if err != nil {
		store.Close()
		temporalClient.Close()
		return nil, fmt.Errorf("create channel reporters: %w", err)
	}

	activitySet := &activities.Activities{
		Store:                       store,
		Engine:                      buildNextActionEngine(cfg, logger),
		Exec:                        exec.NewSafeExecutor(logger),
		Schedule:                    workflowscheduletool.NewTemporalExecutor(temporalClient.ScheduleClient(), store, cfg.Temporal.TaskQueue, cfg.General.WorkspaceRoot, logger),
		Reporter:                    reporters.Reporter,
		EventEnqueuer:               dispatcher,
		MemoryEmbedder:              buildMemoryEmbedder(cfg, logger),
		ConversationCompressor:      buildConversationCompressor(cfg, logger),
		AgentObservationCompressor:  buildAgentObservationCompressor(cfg, logger),
		Project:                     defaultProject,
		WorkspaceRoot:               cfg.General.WorkspaceRoot,
		SkillsRoot:                  env.SkillsRoot,
		StateDir:                    cfg.Runtime.StateDir,
		MemoryEnabled:               cfg.Memory.Enabled,
		MemoryLimit:                 cfg.Memory.AutoContextLimit,
		ConversationEnabled:         cfg.Conversation.Enabled,
		ConversationLimit:           cfg.Conversation.HistoryLimit,
		ConversationMaxContextChars: cfg.Conversation.MaxContextChars,
		ConversationSummaryEnabled:  cfg.Conversation.SummaryEnabled,
		ConversationSummaryTrigger:  cfg.Conversation.SummaryTriggerChars,
		ConversationSummaryMaxChars: cfg.Conversation.SummaryMaxChars,
		ConversationSummaryRecent:   cfg.Conversation.SummaryRecentMessages,
		Logger:                      logger,
	}

	return &runtimeComponents{
		TemporalClient: temporalClient,
		Store:          store,
		Reporters:      reporters,
		Worker:         runtime.NewWorker(temporalClient, cfg.Temporal.TaskQueue, activitySet),
	}, nil
}

func (c *runtimeComponents) Close() error {
	if c == nil {
		return nil
	}
	var errs []error
	if err := c.Reporters.Close(); err != nil {
		errs = append(errs, err)
	}
	if c.Store != nil {
		c.Store.Close()
	}
	if c.TemporalClient != nil {
		c.TemporalClient.Close()
	}
	return errors.Join(errs...)
}
