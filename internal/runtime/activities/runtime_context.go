package activities

import (
	"os"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/config"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
)

func buildRuntimeContext(workspaceRoot, openCTORoot string) agent.RuntimeContext {
	execPath := strings.TrimSpace(os.Getenv("SHELL"))
	now := time.Now()
	location, timeZone, timeZoneErr := scheduletool.ResolveHostTimeZone()
	localNow := now
	timeZoneError := ""
	if timeZoneErr != nil {
		timeZoneError = timeZoneErr.Error()
	} else if location != nil {
		localNow = now.In(location)
	}
	return agent.RuntimeContext{
		OS:                goruntime.GOOS,
		Arch:              goruntime.GOARCH,
		Exec:              execPath,
		Path:              os.Getenv("PATH"),
		WorkspaceRoot:     workspaceRoot,
		OpenCTORoot:       openCTORoot,
		CurrentLocalTime:  localNow.Format(time.RFC3339),
		CurrentUTCTime:    now.UTC().Format(time.RFC3339),
		HostTimeZone:      timeZone,
		HostTimeZoneError: timeZoneError,
	}
}

func workspaceEnvironment(workspaceRoot, openCTORoot string) map[string]string {
	env := map[string]string{}
	if workspaceRoot = strings.TrimSpace(workspaceRoot); workspaceRoot != "" {
		env[config.EnvOpenCTOWorkspace] = workspaceRoot
	}
	if openCTORoot = strings.TrimSpace(openCTORoot); openCTORoot != "" {
		env["OPENCTO_ROOT"] = openCTORoot
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
