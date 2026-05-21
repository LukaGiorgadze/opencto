package main

import (
	"context"
	"io"
	"log/slog"
)

func runValidateCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	configPath, err := parseConfigOnlyCommand("validate", args)
	if err != nil {
		return err
	}
	env, err := loadCommandEnvironment(configPath, stdout)
	if err != nil {
		return err
	}
	env.Logger.Info("configuration validated", slog.String("project_id", defaultProject.ID))
	return nil
}
