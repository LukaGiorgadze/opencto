package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

func runStartCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if commandHelpRequested(args) {
		writeCommandHelp(stdout, "start")
		return nil
	}
	if err := parseNoArgCommand("start", args); err != nil {
		return commandUsageError(stderr, "start", err.Error())
	}
	env, err := loadCommandEnvironment(stdout)
	if err != nil {
		return err
	}
	if len(env.UserEditableCreated) > 0 {
		return fmt.Errorf("created starter files:\n%s\n\nFill in %s, then run opencto start again", strings.Join(env.UserEditableCreated, "\n"), envPathForConfig(env))
	}
	return runStart(ctx, env, stderr)
}

func envPathForConfig(env commandEnvironment) string {
	if env.Config.General.WorkspaceRoot != "" {
		return filepath.Join(env.Config.General.WorkspaceRoot, ".env")
	}
	return ".env"
}

func runStart(ctx context.Context, env commandEnvironment, progress io.Writer) error {
	if err := runBootstrap(ctx, env.Config, defaultProject, env.Logger, env.SkillsRoot == ""); err != nil {
		return err
	}
	if err := ensureRuntimeServices(ctx, env.Config, env.DotEnvPath, env.Logger, progress); err != nil {
		return err
	}
	return runServe(ctx, env)
}
