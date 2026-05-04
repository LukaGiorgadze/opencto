package browser

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRunBuildsAgentBrowserCommandWithDefaultSession(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var captured commandInvocation
	var contextHadDeadline bool
	executor := NewSafeExecutor(nil)
	executor.runner = func(ctx context.Context, invocation commandInvocation) (commandOutput, error) {
		captured = invocation
		_, contextHadDeadline = ctx.Deadline()
		return commandOutput{stdout: "### Snapshot\n[Snapshot](.agent-browser/page.yml)\n"}, nil
	}

	result, err := executor.Run(context.Background(), Request{
		ProjectID:     "Project One!",
		WorkItemID:    "WI/2",
		Command:       "open",
		Args:          []string{"--headed", "https://example.com"},
		WorkspaceRoot: dir,
		TimeoutMs:     2500,
	})
	if err != nil {
		t.Fatalf("run browser command: %v", err)
	}

	wantArgs := []string{"--session", "opencto-project-one-wi-2", "--headed", "open", "https://example.com"}
	if captured.executable != "agent-browser" {
		t.Fatalf("unexpected executable: %q", captured.executable)
	}
	if !reflect.DeepEqual(captured.args, wantArgs) {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, captured.args)
	}
	if captured.dir != dir {
		t.Fatalf("expected working dir %q, got %q", dir, captured.dir)
	}
	if !contextHadDeadline {
		t.Fatalf("expected timeout to create a context deadline")
	}
	if !envContains(captured.env, "OPENCTO_WORKSPACE="+dir) {
		t.Fatalf("expected OPENCTO_WORKSPACE in command environment")
	}
	if result.Session != "opencto-project-one-wi-2" {
		t.Fatalf("unexpected session: %q", result.Session)
	}
	wantArtifact := filepath.Join(dir, ".agent-browser", "page.yml")
	if len(result.ArtifactPaths) != 1 || result.ArtifactPaths[0] != wantArtifact {
		t.Fatalf("unexpected artifact paths: %#v", result.ArtifactPaths)
	}
}

func TestRunUsesDefaultAgentBrowserExecutable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	var captured commandInvocation
	executor := NewSafeExecutor(nil)
	executor.runner = func(_ context.Context, invocation commandInvocation) (commandOutput, error) {
		captured = invocation
		return commandOutput{stdout: "listed\n"}, nil
	}

	result, err := executor.Run(context.Background(), Request{
		ProjectID:     "project-1",
		WorkItemID:    "work-item-1",
		Command:       "session",
		Args:          []string{"list"},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("run browser command: %v", err)
	}

	wantArgs := []string{"--session", "opencto-project-1-work-item-1", "session", "list"}
	if captured.executable != "agent-browser" {
		t.Fatalf("unexpected executable: %q", captured.executable)
	}
	if !reflect.DeepEqual(captured.args, wantArgs) {
		t.Fatalf("unexpected args:\nwant %#v\ngot  %#v", wantArgs, captured.args)
	}
	if result.Executable != "agent-browser" {
		t.Fatalf("unexpected result executable: %q", result.Executable)
	}
}

func TestNormalizeRejectsSessionFlagsOutsideSessionField(t *testing.T) {
	t.Parallel()

	for _, req := range []Request{
		{Command: "snapshot", Args: []string{"--session=other"}},
		{Command: "--session=other"},
	} {
		if _, err := normalizeRequest(req); !errors.Is(err, ErrSessionFlagNotAllowed) {
			t.Fatalf("expected session flag error for %#v, got %v", req, err)
		}
	}
}

func TestNormalizeAllowsAgentBrowserSnapshotSelectorFlag(t *testing.T) {
	t.Parallel()

	req, err := normalizeRequest(Request{
		Command: "snapshot",
		Args:    []string{"-s", "#main"},
	})
	if err != nil {
		t.Fatalf("normalize request: %v", err)
	}
	if !reflect.DeepEqual(req.Args, []string{"-s", "#main"}) {
		t.Fatalf("unexpected args: %#v", req.Args)
	}
}

func TestRunMapsDeadlineExceededToTimeoutExitCode(t *testing.T) {
	t.Parallel()

	executor := NewSafeExecutor(nil)
	executor.runner = func(context.Context, commandInvocation) (commandOutput, error) {
		return commandOutput{stderr: "timed out\n"}, context.DeadlineExceeded
	}

	result, err := executor.Run(context.Background(), Request{
		ProjectID:     "project-1",
		WorkItemID:    "work-item-1",
		Command:       "snapshot",
		WorkspaceRoot: t.TempDir(),
		TimeoutMs:     1,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if result.ExitCode != 124 {
		t.Fatalf("expected timeout exit code 124, got %d", result.ExitCode)
	}
}

func TestAgentBrowserE2E(t *testing.T) {
	if strings.TrimSpace(os.Getenv("OPENCTO_BROWSER_TOOL_E2E")) != "1" {
		t.Skip("set OPENCTO_BROWSER_TOOL_E2E=1 to run agent-browser integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := NewSafeExecutor(nil).Run(ctx, Request{
		ProjectID:     "project-1",
		WorkItemID:    "work-item-1",
		Command:       "session",
		Args:          []string{"list"},
		WorkspaceRoot: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("run agent-browser session list: %v\nstdout: %s\nstderr: %s", err, result.Stdout, result.Stderr)
	}
}

func envContains(env []string, entry string) bool {
	for _, item := range env {
		if item == entry {
			return true
		}
	}
	return false
}
