package main

import (
	"context"
	"fmt"
	"io"
)

func runWorkerCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	configPath, err := parseConfigOnlyCommand("worker", args)
	if err != nil {
		return err
	}
	env, err := loadCommandEnvironment(configPath, stdout)
	if err != nil {
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
