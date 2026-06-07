//go:build windows

package exec

import (
	"os"
	"os/exec"
	"time"
)

func setProcessGroup(_ *exec.Cmd) {}

func processGroupID(_ int) int {
	return 0
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	_, err := os.FindProcess(pid)
	return err == nil
}

func managedProcessRunning(pid, _ int) bool {
	return processRunning(pid)
}

func terminateProcessGroup(pid int, _ time.Duration) error {
	return terminateManagedProcess(pid, 0, 0)
}

func terminateManagedProcess(pid, _ int, _ time.Duration) error {
	if pid <= 0 {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	return process.Kill()
}
