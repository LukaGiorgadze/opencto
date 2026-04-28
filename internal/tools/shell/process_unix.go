//go:build !windows

package shell

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func processGroupID(pid int) int {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return 0
	}
	return pgid
}

func processRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func processGroupRunning(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, syscall.Signal(0))
	return err == nil || errors.Is(err, syscall.EPERM)
}

func managedProcessRunning(pid, pgid int) bool {
	return processRunning(pid) || processGroupRunning(pgid)
}

func terminateProcessGroup(pid int, grace time.Duration) error {
	return terminateManagedProcess(pid, processGroupID(pid), grace)
}

func terminateManagedProcess(pid, pgid int, grace time.Duration) error {
	if pid <= 0 {
		return nil
	}
	if pgid > 0 {
		_ = syscall.Kill(-pgid, syscall.SIGTERM)
	} else if process, err := os.FindProcess(pid); err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}

	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !managedProcessRunning(pid, pgid) {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	if pgid > 0 {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	if process, err := os.FindProcess(pid); err == nil {
		return process.Kill()
	}
	return nil
}
