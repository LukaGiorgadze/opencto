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
	ctx := context.Background()
	if commandNeedsSignalContext(os.Args[1:]) {
		var stop context.CancelFunc
		ctx, stop = signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
		defer stop()
	}

	if err := runOpenCTO(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func commandNeedsSignalContext(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-h", "--help", "help", "configure", "config":
		return false
	default:
		return true
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
	case "-h", "--help":
		writeUsage(stdout)
		return nil
	case "help":
		if len(commandArgs) == 0 {
			writeUsage(stdout)
			return nil
		}
		if len(commandArgs) == 1 && writeCommandHelp(stdout, commandArgs[0]) {
			return nil
		}
		return fmt.Errorf("unknown help topic %q", commandArgs[0])
	case "start":
		return runStartCommand(ctx, commandArgs, stdout, stderr)
	case "configure":
		return runConfigureCommand(commandArgs, os.Stdin, stdout, stderr)
	case "config":
		return runConfigCommand(ctx, commandArgs, stdout, stderr)
	case "serve":
		return runServeCommand(ctx, commandArgs, stdout, stderr)
	case "worker":
		return runWorkerCommand(ctx, commandArgs, stdout, stderr)
	case "doctor":
		return runDoctorCommand(ctx, commandArgs, stdout, stderr)
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
	for _, command := range []string{"start", "config", "doctor", "inject", "report"} {
		help := commandHelpByName[command]
		fmt.Fprintf(out, "  %-10s %s\n", command, help.Summary)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Run `opencto help <command>` for command-specific instructions.")
}
