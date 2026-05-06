package exec

import (
	"context"
	"errors"
	"fmt"
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

func TestSafeExecutorRunsCommandFromProcessWorkingDirectory(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command: executable,
		Args:    []string{"-test.run=TestHelperProcess", "--", "pwd"},
		Timeout: time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	current, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve current working directory: %v", err)
	}
	want, err := filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatalf("resolve expected working directory: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	if err != nil {
		t.Fatalf("resolve actual working directory: %v", err)
	}
	if got != want {
		t.Fatalf("expected command to run in %q, got %q", want, got)
	}
	if result.WorkingDirectory != current {
		t.Fatalf("expected result working directory %q, got %q", current, result.WorkingDirectory)
	}
}

func TestSafeExecutorRunsCommandFromConfiguredWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:    executable,
		Args:       []string{"-test.run=TestHelperProcess", "--", "pwd"},
		WorkingDir: workingDir,
		Timeout:    time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	want, err := filepath.EvalSymlinks(workingDir)
	if err != nil {
		t.Fatalf("resolve expected working directory: %v", err)
	}
	got, err := filepath.EvalSymlinks(strings.TrimSpace(result.Stdout))
	if err != nil {
		t.Fatalf("resolve actual working directory: %v", err)
	}
	if got != want {
		t.Fatalf("expected command to run in %q, got %q", want, got)
	}
	if result.WorkingDirectory != workingDir {
		t.Fatalf("expected result working directory %q, got %q", workingDir, result.WorkingDirectory)
	}
}

func TestSafeExecutorRunsCommand(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command: executable,
		Args:    []string{"-test.run=TestHelperProcess", "--", "hello"},
		Timeout: time.Second,
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

func TestSafeExecutorStreamsOutputToLogsAndReturnsTail(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command:   executable,
		Args:      []string{"-test.run=TestHelperProcess", "--", "spam"},
		StateDir:  stateDir,
		Timeout:   time.Second,
		TailBytes: 64,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if result.StdoutLogPath == "" || result.StderrLogPath == "" {
		t.Fatalf("expected log paths, got %#v", result)
	}
	if !result.StdoutTruncated {
		t.Fatalf("expected stdout to be truncated, got %#v", result)
	}
	if strings.Contains(result.Stdout, "line-000") || !strings.Contains(result.Stdout, "line-099") {
		t.Fatalf("expected stdout result to contain only tail, got %q", result.Stdout)
	}
	full, err := os.ReadFile(result.StdoutLogPath)
	if err != nil {
		t.Fatalf("read stdout log: %v", err)
	}
	if !strings.Contains(string(full), "line-000") || !strings.Contains(string(full), "line-099") {
		t.Fatalf("expected full stdout log, got %q", string(full))
	}
}

func TestSafeExecutorPromotesLongCommandToManagedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX exec fixture")
	}
	t.Parallel()

	stateDir := t.TempDir()
	workingDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		ProjectID:   "project-1",
		WorkItemID:  "work-item-1",
		ToolCallID:  "toolu_long",
		ProcessID:   "proc-long",
		Command:     executable,
		Args:        []string{"-test.run=TestHelperProcess", "--", "block"},
		WorkingDir:  workingDir,
		StateDir:    stateDir,
		Timeout:     time.Second,
		GracePeriod: 20 * time.Millisecond,
		TailBytes:   1024,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	})
	if err != nil {
		t.Fatalf("run command: %v", err)
	}
	if result.ManagedProcess == nil || result.ManagedProcess.ID != "proc-long" {
		t.Fatalf("expected promoted process, got %#v", result)
	}
	if result.ManagedProcess.Status != domain.ProcessStatusRunning {
		t.Fatalf("expected running process, got %#v", result.ManagedProcess)
	}

	manager := NewProcessManager(nil)
	defer func() {
		_, _ = manager.Stop(context.Background(), stateDir, "proc-long")
	}()
	checked, err := manager.Check(context.Background(), stateDir, "proc-long")
	if err != nil {
		t.Fatalf("check process: %v", err)
	}
	if checked.Status != domain.ProcessStatusRunning || checked.WorkingDirectory != workingDir {
		t.Fatalf("unexpected process: %#v", checked)
	}
}

func TestSafeExecutorRunsBatchActions(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		ProjectID: "project-1",
		Intent:    "inspect system",
		Timeout:   time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
		Actions: []Action{
			{
				Command: executable,
				Args:    []string{"-test.run=TestHelperProcess", "--", "hello"},
			},
			{
				Command: executable,
				Args:    []string{"-test.run=TestHelperProcess", "--", "world"},
			},
		},
	})
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	if !strings.Contains(result.Stdout, "command 1:") || !strings.Contains(result.Stdout, "hello") || !strings.Contains(result.Stdout, "world") {
		t.Fatalf("unexpected stdout: %q", result.Stdout)
	}
}

func TestSafeExecutorRunsBatchActionsFromConfiguredWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		ProjectID:  "project-1",
		Intent:     "inspect workspace",
		WorkingDir: workingDir,
		Timeout:    time.Second,
		Environment: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
		Actions: []Action{
			{
				Command: executable,
				Args:    []string{"-test.run=TestHelperProcess", "--", "pwd"},
			},
		},
	})
	if err != nil {
		t.Fatalf("run batch: %v", err)
	}
	if !strings.Contains(result.Stdout, workingDir) {
		t.Fatalf("expected stdout to contain working directory %q, got %q", workingDir, result.Stdout)
	}
	if result.WorkingDirectory != workingDir {
		t.Fatalf("expected result working directory %q, got %q", workingDir, result.WorkingDirectory)
	}
}

func TestSafeExecutorTimeoutKillsCommand(t *testing.T) {
	t.Parallel()

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	executor := NewSafeExecutor(slog.New(slog.NewTextHandler(io.Discard, nil)))
	result, err := executor.Run(context.Background(), Request{
		Command: executable,
		Args:    []string{"-test.run=TestHelperProcess", "--", "block"},
		Timeout: 50 * time.Millisecond,
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

	stateDir := t.TempDir()
	workingDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	manager := NewProcessManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	process, err := manager.Start(context.Background(), StartProcessRequest{
		ProcessID:  "proc-1",
		ProjectID:  "project-1",
		Command:    executable,
		Args:       []string{"-test.run=TestHelperProcess", "--", "block"},
		WorkingDir: workingDir,
		StateDir:   stateDir,
		Timeout:    time.Second,
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
	if process.WorkingDirectory != workingDir {
		t.Fatalf("expected process working directory %q, got %q", workingDir, process.WorkingDirectory)
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

	stateDir := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}
	manager := NewProcessManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
	process, err := manager.Start(context.Background(), StartProcessRequest{
		ProcessID: "proc-fail",
		ProjectID: "project-1",
		Command:   executable,
		Args:      []string{"-test.run=TestHelperProcess", "--", "fail"},
		StateDir:  stateDir,
		Timeout:   time.Second,
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
		t.Skip("exec did not leave a child in the launcher process group")
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

func TestResolveWorkingDirDefaultsToProcessWorkingDirectory(t *testing.T) {
	t.Parallel()

	workingDir, err := ResolveWorkingDir("")
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	want, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve current working directory: %v", err)
	}
	if workingDir != want {
		t.Fatalf("expected %q, got %q", want, workingDir)
	}
}

func TestProcessStateDirRequiresConfiguredStateDir(t *testing.T) {
	t.Parallel()

	_, err := processStateDir("")
	if err == nil {
		t.Fatal("expected missing state dir error")
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
		case "pwd":
			workingDir, err := os.Getwd()
			if err != nil {
				_, _ = os.Stderr.WriteString(err.Error())
				os.Exit(1)
			}
			_, _ = os.Stdout.WriteString(workingDir)
		case "spam":
			for i := 0; i < 100; i++ {
				_, _ = os.Stdout.WriteString(fmt.Sprintf("line-%03d\n", i))
			}
		default:
			_, _ = os.Stdout.WriteString(args[1])
		}
	}
	os.Exit(0)
}
