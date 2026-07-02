package activities

import (
	"context"
	"log/slog"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
	"github.com/opencto/opencto/internal/storage"
	agentemailtool "github.com/opencto/opencto/internal/tools/agentemail"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	"github.com/opencto/opencto/internal/tools/postprocess"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

type Reporter interface {
	Report(context.Context, domain.Event, domain.ReportMessage) ([]domain.ReportReceipt, error)
}

type EventEnqueuer interface {
	EnqueueEvent(context.Context, domain.Event) error
}

type TypingReporter interface {
	NotifyTyping(context.Context, domain.Event) error
}

type Activities struct {
	Store                       storage.RuntimeStore
	Engine                      agent.Engine
	AgentEmail                  agentemailtool.Executor
	Exec                        exectool.Executor
	Edit                        edittool.Executor
	Glob                        globtool.Executor
	Grep                        greptool.Executor
	Read                        readtool.Executor
	Schedule                    scheduletool.Executor
	Skill                       skilltool.Executor
	Write                       writetool.Executor
	ToolResultProcessors        []postprocess.Processor
	Reporter                    Reporter
	EventEnqueuer               EventEnqueuer
	MemoryEmbedder              embedding.Embedder
	ConversationCompressor      agent.ConversationCompressor
	AgentObservationCompressor  agent.AgentObservationCompressor
	Project                     domain.Project
	WorkspaceRoot               string
	SkillsRoot                  string
	StateDir                    string
	MemoryEnabled               bool
	MemoryLimit                 int
	ConversationEnabled         bool
	ConversationLimit           int
	ConversationMaxContextChars int
	ConversationSummaryEnabled  bool
	ConversationSummaryTrigger  int
	ConversationSummaryMaxChars int
	ConversationSummaryRecent   int
	ExecTailBytes               int64
	ExecGrace                   time.Duration
	HeartbeatGap                time.Duration
	Logger                      *slog.Logger
}
