package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestUsageListsOnlyPublicCommands(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runOpenCTO(context.Background(), []string{"--help"}, &stdout, &stderr); err != nil {
		t.Fatalf("help: %v", err)
	}

	usage := stdout.String()
	for _, command := range []string{"start", "doctor", "inject", "report"} {
		if !strings.Contains(usage, "  "+command+" ") {
			t.Fatalf("expected usage to list %q:\n%s", command, usage)
		}
	}
	for _, command := range []string{"serve", "server", "worker", "bootstrap", "validate", "check-env"} {
		if strings.Contains(usage, command) {
			t.Fatalf("expected usage not to list %q:\n%s", command, usage)
		}
	}
}

func TestPublicCommandsShowCommandHelp(t *testing.T) {
	for _, command := range []string{"start", "doctor", "inject", "report"} {
		command := command
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if err := runOpenCTO(context.Background(), []string{command, "--help"}, &stdout, &stderr); err != nil {
				t.Fatalf("%s --help: %v", command, err)
			}
			output := stdout.String()
			if !strings.Contains(output, "Usage: opencto "+command) {
				t.Fatalf("expected command usage for %q:\n%s", command, output)
			}
			if !strings.Contains(output, "Examples:") {
				t.Fatalf("expected examples for %q:\n%s", command, output)
			}
		})
	}
}

func TestHelpCommandShowsCommandHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := runOpenCTO(context.Background(), []string{"help", "inject"}, &stdout, &stderr); err != nil {
		t.Fatalf("help inject: %v", err)
	}
	output := stdout.String()
	if !strings.Contains(output, `Usage: opencto inject -body "message"`) {
		t.Fatalf("expected inject usage:\n%s", output)
	}
}

func TestInjectWithoutArgsShowsUsageBeforeLoadingEnvironment(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runOpenCTO(context.Background(), []string{"inject"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing body error")
	}
	if !strings.Contains(err.Error(), "inject: missing required -body") {
		t.Fatalf("unexpected error: %v", err)
	}
	output := stderr.String()
	if !strings.Contains(output, `Usage: opencto inject -body "message"`) {
		t.Fatalf("expected inject usage:\n%s", output)
	}
	if strings.Contains(output, "OPENCTO_WORKSPACE is required") {
		t.Fatalf("expected usage before environment loading:\n%s", output)
	}
}

func TestReportWithoutArgsShowsUsageBeforeLoadingEnvironment(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runOpenCTO(context.Background(), []string{"report"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing report arguments error")
	}
	if !strings.Contains(err.Error(), "report: missing report arguments") {
		t.Fatalf("unexpected error: %v", err)
	}
	output := stderr.String()
	if !strings.Contains(output, "Usage: opencto report") {
		t.Fatalf("expected report usage:\n%s", output)
	}
	if strings.Contains(output, "OPENCTO_WORKSPACE is required") {
		t.Fatalf("expected usage before environment loading:\n%s", output)
	}
}

func TestRemovedCLICommandsAreUnknown(t *testing.T) {
	for _, command := range []string{"server", "bootstrap", "validate", "check-env"} {
		command := command
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runOpenCTO(context.Background(), []string{command}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected unknown command error")
			}
			if !strings.Contains(err.Error(), `unknown command "`+command+`"`) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDevOnlyCLICommandsRequireRepoMode(t *testing.T) {
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldwd)
	})

	for _, command := range []string{"serve", "worker"} {
		command := command
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			err := runOpenCTO(context.Background(), []string{command}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected dev-only command error")
			}
			if !strings.Contains(err.Error(), "only available in OpenCTO dev mode") {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
