package main

import (
	"context"
	"fmt"
	"io"
)

func runWorkerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := requireDevCommand("worker"); err != nil {
		return err
	}
	if err := parseNoArgCommand("worker", args); err != nil {
		return err
	}
	env, err := loadCommandEnvironment(stdout)
	if err != nil {
		return err
	}
	if err := runBootstrap(ctx, env.Config, defaultProject, env.Logger, env.SkillsRoot == ""); err != nil {
		return err
	}
	return runWorker(ctx, env)
}

func runWorker(ctx context.Context, env commandEnvironment) error {
	components, err := newRuntimeComponents(ctx, env)
	if err != nil {
		return err
	}
	defer components.Close()

	if err := components.Worker.Run(); err != nil {
		return fmt.Errorf("run worker: %w", err)
	}
	return nil
}
