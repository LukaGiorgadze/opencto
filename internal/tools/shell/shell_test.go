package shell

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func TestSafeExecutorRejectsWorkingDirectoryEscape(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	_, err := executor.Run(context.Background(), Request{
		Command:       "pwd",
		WorkingDir:    filepath.Dir(workspace),
		WorkspaceRoot: workspace,
	})
	if err != ErrWorkingDirectoryEscape {
		t.Fatalf("expected ErrWorkingDirectoryEscape, got %v", err)
	}
}

func TestSafeExecutorRunsCommandInsideWorkspace(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:       executable,
		Args:          []string{"-test.run=TestHelperProcess", "--", "hello"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		Timeout:       time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if result.Stdout != "hello" {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestSafeExecutorTimeoutKillsCommand(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:       executable,
		Args:          []string{"-test.run=TestHelperProcess", "--", "block"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		Timeout:       50 * time.Millisecond,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
	if result.ExitCode != 124 {
		t.Fatalf("expected timeout exit code 124, got %d", result.ExitCode)
	}
}

func TestProcessManagerStartStatusLogsStop(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stateDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	manager := NewProcessManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	process, err := manager.Start(context.Background(), StartProcessRequest{
		ProcessID:     "proc-1",
		ProjectID:     "project-1",
		Command:       executable,
		Args:          []string{"-test.run=TestHelperProcess", "--", "block"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		StateDir:      stateDir,
		Timeout:       time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("start process: %v", err)
	}
	if process.PID <= 0 || process.Status != "running" {
		t.Fatalf("unexpected process: %#v", process)
	}
	defer func() {
		_, _ = manager.Stop(context.Background(), stateDir, process.ID)
	}()

	checked, err := manager.Check(context.Background(), stateDir, process.ID)
	if err != nil {
		t.Fatalf("check process: %v", err)
	}
	if checked.Status != "running" {
		t.Fatalf("expected running process, got %#v", checked)
	}

	var logs ProcessLogResult
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		logs, err = manager.Logs(context.Background(), stateDir, process.ID, 1024)
		if err != nil {
			t.Fatalf("read logs: %v", err)
		}
		if strings.Contains(logs.StdoutTail, "ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(logs.StdoutTail, "ready") {
		t.Fatalf("expected stdout log tail to contain process output, got %#v", logs)
	}

	stopped, err := manager.Stop(context.Background(), stateDir, process.ID)
	if err != nil {
		t.Fatalf("stop process: %v", err)
	}
	if stopped.Status != "stopped" {
		t.Fatalf("expected stopped process, got %#v", stopped)
	}
}

func TestProcessManagerReportsImmediateExit(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stateDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	manager := NewProcessManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	process, err := manager.Start(context.Background(), StartProcessRequest{
		ProcessID:     "proc-fail",
		ProjectID:     "project-1",
		Command:       executable,
		Args:          []string{"-test.run=TestHelperProcess", "--", "fail"},
		WorkingDir:    workspace,
		WorkspaceRoot: workspace,
		StateDir:      stateDir,
		Timeout:       time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "background process exited during startup") {
		t.Fatalf("expected startup exit error, got process=%#v err=%v", process, err)
	}
	if process.Status != domain.ProcessStatusFailed {
		t.Fatalf("expected failed process status, got %#v", process)
	}
}

func TestProcessManagerStopUsesStoredProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are POSIX-specific")
	}
	t.Parallel()

	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(stateDir, "processes"), 0o755); err != nil {
		t.Fatalf("create process registry: %v", err)
	}
	cmd := exec.Command("sh", "-c", "sleep 30 &")
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	pid := cmd.Process.Pid
	pgid := processGroupID(pid)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait launcher: %v", err)
	}
	if !processGroupRunning(pgid) {
		t.Skip("shell did not leave a child in the launcher process group")
	}
	defer func() {
		_ = terminateManagedProcess(pid, pgid, time.Second)
	}()

	process := domain.ManagedProcess{
		ID:               "proc-group",
		ProjectID:        "project-1",
		Command:          "sh",
		Args:             []string{"-c", "sleep 30 &"},
		WorkingDirectory: t.TempDir(),
		PID:              pid,
		PGID:             pgid,
		Status:           domain.ProcessStatusRunning,
		StartedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := writeManagedProcess(stateDir, process); err != nil {
		t.Fatalf("write managed process: %v", err)
	}

	manager := NewProcessManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	stopped, err := manager.Stop(context.Background(), stateDir, process.ID)
	if err != nil {
		t.Fatalf("stop process group: %v", err)
	}
	if stopped.Status != domain.ProcessStatusStopped {
		t.Fatalf("expected stopped status, got %#v", stopped)
	}
	if processGroupRunning(pgid) {
		t.Fatalf("expected stored process group %d to be stopped", pgid)
	}
}

func TestSecureWorkingDirDefaultsToOpenCTOInUserHome(t *testing.T) {
	t.Parallel()

	workingDir, err := secureWorkingDir("", "", false)
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	want := filepath.Join(home, "opencto")
	if workingDir != want {
		t.Fatalf("expected %q, got %q", want, workingDir)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 1 {
		switch args[1] {
		case "block":
			_, _ = os.Stdout.WriteString("ready\n")
			for {
				time.Sleep(time.Second)
			}
		case "fail":
			os.Exit(42)
		default:
			_, _ = os.Stdout.WriteString(args[1])
		}
	}
	os.Exit(0)
}
