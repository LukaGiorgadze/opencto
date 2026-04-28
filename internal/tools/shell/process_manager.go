package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/workspace"
)

type StartProcessRequest struct {
	ProcessID     string
	ProjectID     string
	WorkItemID    string
	ToolCallID    string
	Intent        string
	ProcessScope  domain.ProcessScope
	Command       string
	Args          []string
	WorkingDir    string
	WorkspaceRoot string
	StateDir      string
	Timeout       time.Duration
	Environment   map[string]string
}

type ProcessLogResult struct {
	Process    domain.ManagedProcess
	StdoutTail string
	StderrTail string
}

type ProcessManager struct {
	logger *slog.Logger
}

type processExit struct {
	process domain.ManagedProcess
	err     error
}

const backgroundStartupObservation = 500 * time.Millisecond

func NewProcessManager(logger *slog.Logger) *ProcessManager {
	if logger == nil {
		logger = slog.Default()
	}
	return &ProcessManager{logger: logger}
}

func backgroundStartupWindow(timeout time.Duration) time.Duration {
	if timeout > 0 && timeout < backgroundStartupObservation {
		return timeout
	}
	return backgroundStartupObservation
}

func startProcessScope(scope domain.ProcessScope) domain.ProcessScope {
	if scope == domain.ProcessScopeProject {
		return domain.ProcessScopeProject
	}
	return domain.ProcessScopeTask
}

func (m *ProcessManager) Start(ctx context.Context, req StartProcessRequest) (domain.ManagedProcess, error) {
	if strings.TrimSpace(req.ProcessID) == "" {
		return domain.ManagedProcess{}, fmt.Errorf("process_id is required")
	}
	if strings.TrimSpace(req.Command) == "" {
		return domain.ManagedProcess{}, ErrEmptyCommand
	}
	workingDir, err := secureWorkingDir(req.WorkspaceRoot, req.WorkingDir, false)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	stateDir, err := processStateDir(req.StateDir)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "processes"), 0o755); err != nil {
		return domain.ManagedProcess{}, err
	}
	logDir := filepath.Join(stateDir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return domain.ManagedProcess{}, err
	}

	stdoutPath := filepath.Join(logDir, req.ProcessID+".stdout.log")
	stderrPath := filepath.Join(logDir, req.ProcessID+".stderr.log")
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	defer stdoutFile.Close()
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	defer stderrFile.Close()

	runCtx := ctx
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	cmd := exec.Command(req.Command, req.Args...)
	cmd.Dir = workingDir
	cmd.Env = mergeEnv(req.Environment)
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	setProcessGroup(cmd)

	if err := cmd.Start(); err != nil {
		return domain.ManagedProcess{}, err
	}
	startedAt := time.Now().UTC()
	process := domain.ManagedProcess{
		ID:               req.ProcessID,
		ProjectID:        req.ProjectID,
		WorkItemID:       req.WorkItemID,
		ToolCallID:       req.ToolCallID,
		Command:          req.Command,
		Args:             append([]string(nil), req.Args...),
		WorkingDirectory: workingDir,
		PID:              cmd.Process.Pid,
		PGID:             processGroupID(cmd.Process.Pid),
		Status:           domain.ProcessStatusRunning,
		StdoutLogPath:    stdoutPath,
		StderrLogPath:    stderrPath,
		StartedAt:        startedAt,
		UpdatedAt:        startedAt,
		Metadata: domain.Metadata{
			"intent":        req.Intent,
			"process_scope": string(startProcessScope(req.ProcessScope)),
		},
	}

	if err := writeManagedProcess(stateDir, process); err != nil {
		_ = terminateProcessGroup(cmd.Process.Pid, time.Second)
		return domain.ManagedProcess{}, err
	}
	exitCh := make(chan processExit, 1)
	go func() {
		waitErr := cmd.Wait()
		if waitErr != nil {
			m.logger.Info("background process exited with error",
				slog.String("project_id", req.ProjectID),
				slog.String("process_id", process.ID),
				slog.String("error", waitErr.Error()),
			)
		}
		updated := process
		current, readErr := readManagedProcess(stateDir, process.ID)
		if readErr == nil {
			updated = current
		}
		if updated.Status != domain.ProcessStatusRunning {
			exitCh <- processExit{process: updated, err: waitErr}
			return
		}
		if waitErr != nil {
			updated.Status = domain.ProcessStatusFailed
		} else {
			updated.Status = domain.ProcessStatusExited
		}
		updated.UpdatedAt = time.Now().UTC()
		_ = writeManagedProcess(stateDir, updated)
		exitCh <- processExit{process: updated, err: waitErr}
	}()

	startupWindow := backgroundStartupWindow(req.Timeout)
	select {
	case exited := <-exitCh:
		if exited.err != nil {
			return exited.process, fmt.Errorf("background process exited during startup: %w", exited.err)
		}
		return exited.process, fmt.Errorf("background process exited during startup")
	case <-runCtx.Done():
		_ = terminateManagedProcess(process.PID, process.PGID, time.Second)
		process.Status = domain.ProcessStatusFailed
		process.UpdatedAt = time.Now().UTC()
		_ = writeManagedProcess(stateDir, process)
		return process, runCtx.Err()
	case <-time.After(startupWindow):
	}

	m.logger.Info("background process started",
		slog.String("project_id", req.ProjectID),
		slog.String("process_id", process.ID),
		slog.String("command", req.Command),
		slog.Any("args", req.Args),
		slog.Int("pid", process.PID),
	)
	return process, nil
}

func (m *ProcessManager) Check(ctx context.Context, stateDir, processID string) (domain.ManagedProcess, error) {
	if err := ctx.Err(); err != nil {
		return domain.ManagedProcess{}, err
	}
	root, err := processStateDir(stateDir)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	process, err := readManagedProcess(root, processID)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	if process.Status == domain.ProcessStatusRunning && !managedProcessRunning(process.PID, process.PGID) {
		process.Status = domain.ProcessStatusExited
		process.UpdatedAt = time.Now().UTC()
		_ = writeManagedProcess(root, process)
	}
	return process, nil
}

func (m *ProcessManager) Stop(ctx context.Context, stateDir, processID string) (domain.ManagedProcess, error) {
	if err := ctx.Err(); err != nil {
		return domain.ManagedProcess{}, err
	}
	root, err := processStateDir(stateDir)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	process, err := readManagedProcess(root, processID)
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	if process.Status == domain.ProcessStatusRunning {
		if err := terminateManagedProcess(process.PID, process.PGID, 2*time.Second); err != nil {
			return process, err
		}
	}
	process.Status = domain.ProcessStatusStopped
	process.UpdatedAt = time.Now().UTC()
	if err := writeManagedProcess(root, process); err != nil {
		return process, err
	}
	return process, nil
}

func (m *ProcessManager) Logs(ctx context.Context, stateDir, processID string, limitBytes int64) (ProcessLogResult, error) {
	if err := ctx.Err(); err != nil {
		return ProcessLogResult{}, err
	}
	root, err := processStateDir(stateDir)
	if err != nil {
		return ProcessLogResult{}, err
	}
	process, err := readManagedProcess(root, processID)
	if err != nil {
		return ProcessLogResult{}, err
	}
	if limitBytes <= 0 {
		limitBytes = 8192
	}
	stdoutTail, _ := readTail(process.StdoutLogPath, limitBytes)
	stderrTail, _ := readTail(process.StderrLogPath, limitBytes)
	return ProcessLogResult{
		Process:    process,
		StdoutTail: stdoutTail,
		StderrTail: stderrTail,
	}, nil
}

func processStateDir(stateDir string) (string, error) {
	return workspace.ResolveStateDir(stateDir, "default")
}

func processPath(stateDir, processID string) string {
	return filepath.Join(stateDir, "processes", processID+".json")
}

func readManagedProcess(stateDir, processID string) (domain.ManagedProcess, error) {
	processID = strings.TrimSpace(processID)
	if processID == "" {
		return domain.ManagedProcess{}, fmt.Errorf("process_id is required")
	}
	data, err := os.ReadFile(processPath(stateDir, processID))
	if err != nil {
		return domain.ManagedProcess{}, err
	}
	var process domain.ManagedProcess
	if err := json.Unmarshal(data, &process); err != nil {
		return domain.ManagedProcess{}, err
	}
	return process, nil
}

func writeManagedProcess(stateDir string, process domain.ManagedProcess) error {
	data, err := json.MarshalIndent(process, "", "  ")
	if err != nil {
		return err
	}
	tmp := processPath(stateDir, process.ID) + "." + strconv.FormatInt(time.Now().UnixNano(), 10) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, processPath(stateDir, process.ID))
}

func readTail(path string, limitBytes int64) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	offset := int64(0)
	if info.Size() > limitBytes {
		offset = info.Size() - limitBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
