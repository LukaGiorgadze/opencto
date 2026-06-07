package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

func runServeCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := requireDevCommand("serve"); err != nil {
		return err
	}
	if err := parseNoArgCommand("serve", args); err != nil {
		return err
	}
	env, err := loadCommandEnvironment(stdout)
	if err != nil {
		return err
	}
	if err := runBootstrap(ctx, env.Config, defaultProject, env.Logger, env.SkillsRoot == ""); err != nil {
		return err
	}
	return runServe(ctx, env)
}

func runServe(ctx context.Context, env commandEnvironment) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	components, err := newRuntimeComponents(runCtx, env)
	if err != nil {
		return err
	}
	defer components.Close()

	workerErr := make(chan error, 1)
	go func() {
		workerErr <- components.Worker.Run()
		cancel()
	}()

	for _, starter := range components.Reporters.Starters {
		if err := starter.Start(runCtx); err != nil {
			return fmt.Errorf("start channel adapter: %w", err)
		}
		env.Logger.Info("channel adapter started", slog.String("project_id", defaultProject.ID))
	}

	select {
	case err := <-workerErr:
		if err != nil {
			return fmt.Errorf("run worker: %w", err)
		}
		return nil
	case <-runCtx.Done():
		return nil
	}
}
