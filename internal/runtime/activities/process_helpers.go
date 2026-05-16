package activities

import (
	"context"
	"strconv"
	"strings"

	"github.com/opencto/opencto/internal/domain"
	exectool "github.com/opencto/opencto/internal/tools/exec"
)

func processStartObservation(process domain.ManagedProcess) string {
	if strings.TrimSpace(process.ID) == "" {
		return "Background process did not start."
	}
	var builder strings.Builder
	builder.WriteString("Started background process.")
	builder.WriteString("\nprocess_id: ")
	builder.WriteString(process.ID)
	if process.PID > 0 {
		builder.WriteString("\npid: ")
		builder.WriteString(strconv.Itoa(process.PID))
	}
	if process.PGID > 0 {
		builder.WriteString("\npgid: ")
		builder.WriteString(strconv.Itoa(process.PGID))
	}
	if process.StdoutLogPath != "" {
		builder.WriteString("\nstdout_log: ")
		builder.WriteString(process.StdoutLogPath)
	}
	if process.StderrLogPath != "" {
		builder.WriteString("\nstderr_log: ")
		builder.WriteString(process.StderrLogPath)
	}
	return builder.String()
}

func backgroundStartFailureObservation(ctx context.Context, manager *exectool.ProcessManager, stateDir string, process domain.ManagedProcess, runErr error) string {
	if manager == nil || strings.TrimSpace(process.ID) == "" {
		return fullObservation("", "", runErr)
	}
	logs, err := manager.Logs(ctx, stateDir, process.ID, 0)
	if err != nil {
		return fullObservation("", "", runErr)
	}
	return fullObservation(logs.StdoutTail, logs.StderrTail, runErr)
}
