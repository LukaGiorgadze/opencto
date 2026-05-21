package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"go.temporal.io/sdk/client"

	"github.com/opencto/opencto/internal/channels/local"
	"github.com/opencto/opencto/internal/runtime"
)

func runInjectCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newCommandFlagSet("inject")
	configPath := flags.String("config", "", "path to config file")
	actor := flags.String("actor", "local-user", "actor name for injected event")
	body := flags.String("body", "", "event body")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("inject: unexpected argument %q", flags.Arg(0))
	}

	env, err := loadCommandEnvironment(*configPath, stdout)
	if err != nil {
		return err
	}

	temporalClient, err := client.Dial(client.Options{
		HostPort:  env.Config.Temporal.HostPort,
		Namespace: env.Config.Temporal.Namespace,
	})
	if err != nil {
		return fmt.Errorf("connect temporal: %w", err)
	}
	defer temporalClient.Close()

	dispatcher := runtime.NewDispatcher(temporalClient, env.Config.Temporal.TaskQueue, env.Config.Temporal.ContinueAsNewAfterEvents)
	injector := local.NewInjector(defaultProject.ID, dispatcher, env.Logger)
	if _, err := injector.Inject(ctx, *actor, *body); err != nil {
		return fmt.Errorf("inject local event: %w", err)
	}
	env.Logger.Info("event injected", slog.String("project_id", defaultProject.ID))
	return nil
}
