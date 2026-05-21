package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := runOpenCTO(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runOpenCTO(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		writeUsage(stderr)
		return fmt.Errorf("missing command")
	}

	command := args[0]
	commandArgs := args[1:]
	switch command {
	case "help", "-h", "--help":
		writeUsage(stdout)
		return nil
	case "serve":
		return runServeCommand(ctx, commandArgs, stdout, stderr)
	case "worker":
		return runWorkerCommand(ctx, commandArgs, stdout, stderr)
	case "bootstrap":
		return runBootstrapCommand(ctx, commandArgs, stdout, stderr)
	case "doctor", "check-env":
		return runDoctorCommand(ctx, command, commandArgs, stdout, stderr)
	case "validate":
		return runValidateCommand(ctx, commandArgs, stdout, stderr)
	case "inject":
		return runInjectCommand(ctx, commandArgs, stdout, stderr)
	case "report":
		return runReportCommand(ctx, commandArgs, stdout, stderr)
	default:
		writeUsage(stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}

func writeUsage(out io.Writer) {
	if out == nil {
		return
	}
	fmt.Fprintln(out, "Usage: opencto <command> [options]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  serve       run the worker and channel adapters")
	fmt.Fprintln(out, "  worker      run only the Temporal worker")
	fmt.Fprintln(out, "  bootstrap   initialize workspace state and install the CLI")
	fmt.Fprintln(out, "  doctor      check local configuration and runtime dependencies")
	fmt.Fprintln(out, "  validate    validate configuration")
	fmt.Fprintln(out, "  inject      inject a local event")
	fmt.Fprintln(out, "  report      send a one-shot channel report")
}
