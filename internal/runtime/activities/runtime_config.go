package activities

import (
	"strings"
	"time"

	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/workspace"
)

func (a *Activities) execGrace(timeout time.Duration) time.Duration {
	if a.ExecGrace > 0 {
		return a.ExecGrace
	}
	if timeout > 0 && timeout < 2*defaultExecGrace {
		return timeout / 2
	}
	return defaultExecGrace
}

func (a *Activities) execTailBytes() int64 {
	if a.ExecTailBytes > 0 {
		return a.ExecTailBytes
	}
	return defaultExecTailBytes
}

func (a *Activities) runtimeStateDir() string {
	stateDir, err := workspace.ResolveStateDir(a.StateDir, a.WorkspaceRoot)
	if err != nil {
		return ""
	}
	return stateDir
}

func (a *Activities) skillsRoots() []string {
	if strings.TrimSpace(a.SkillsRoot) != "" {
		return []string{a.SkillsRoot}
	}
	return skillcatalog.RuntimeRoots(a.WorkspaceRoot)
}
