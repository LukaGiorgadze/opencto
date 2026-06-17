package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

const (
	ProviderSQLite = "sqlite"

	defaultAutoContextLimit            = 5
	defaultConversationHistoryLimit    = 20
	defaultConversationMaxContextChars = 20000
	defaultConversationSummaryTrigger  = 24000
	defaultConversationSummaryMaxChars = 6000
	defaultConversationSummaryRecent   = 10
)

var ErrMemoryPolicyRejected = errors.New("memory rejected by policy")

type EventAppendResult struct {
	Inserted bool
	Updated  bool
	Changed  bool
}

type RuntimeStore interface {
	Close() error
	Migrate(context.Context) error
	VerifySchema(context.Context) error
	EnsureProject(context.Context, domain.Project) error
	GetOnboardingState(context.Context, string) (domain.OnboardingState, bool, error)
	UpsertOnboardingState(context.Context, domain.OnboardingState) error
	AppendEvent(context.Context, domain.Event) (EventAppendResult, error)
	ListPendingWorkItems(context.Context, string) ([]domain.WorkItem, error)
	UpsertWorkItems(context.Context, []domain.WorkItem) error
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
	UpsertConversationThread(context.Context, domain.ConversationThread) error
	GetConversationThread(context.Context, ConversationThreadQuery) (domain.ConversationThread, bool, error)
	GetConversationRootMessage(context.Context, ConversationRootMessageQuery) (domain.ConversationMessage, bool, error)
	UpsertConversationMessage(context.Context, domain.ConversationMessage) error
	ListConversationMessages(context.Context, ConversationQuery) ([]domain.ConversationMessage, error)
	UpsertConversationSummary(context.Context, domain.ConversationSummary) error
	ListConversationSummaries(context.Context, ConversationSummaryQuery) ([]domain.ConversationSummary, error)
	ResetContext(context.Context, domain.ContextResetRequest) (domain.ContextResetResult, error)
	RememberMemory(context.Context, domain.Memory) (domain.Memory, error)
	SearchMemories(context.Context, domain.MemorySearchRequest) ([]domain.Memory, error)
	ListMemories(context.Context, domain.MemoryListRequest) ([]domain.Memory, error)
	UpdateMemory(context.Context, domain.MemoryUpdateRequest) (domain.MemoryUpdateResult, error)
	UpsertMemoryEmbedding(context.Context, domain.MemoryEmbedding) error
	DeleteMemoryEmbeddings(context.Context, []string) error
	ForgetMemory(context.Context, string, string) (bool, error)
	ForgetMemories(context.Context, domain.MemoryForgetRequest) (domain.MemoryForgetResult, error)
	UpsertScheduledWorkflow(context.Context, domain.ScheduledWorkflow) error
	GetScheduledWorkflow(context.Context, string, string) (domain.ScheduledWorkflow, bool, error)
	ListScheduledWorkflows(context.Context, ScheduledWorkflowQuery) ([]domain.ScheduledWorkflow, error)
	DeleteScheduledWorkflow(context.Context, string, string) error
	GetScheduledWorkflowRun(context.Context, string, string) (domain.ScheduledWorkflowRun, bool, error)
	UpsertScheduledWorkflowRun(context.Context, domain.ScheduledWorkflowRun) error
	UpsertScheduledWorkflowStepRun(context.Context, domain.ScheduledWorkflowStepRun) error
}

type ConversationScope string

const (
	ConversationScopeProject ConversationScope = "project"
	ConversationScopeChannel ConversationScope = "channel"
	ConversationScopeThread  ConversationScope = "thread"
)

type ConversationQuery struct {
	ProjectID       string
	ChannelType     domain.ChannelType
	ChannelID       string
	ThreadID        string
	Scope           ConversationScope
	Roles           []domain.ConversationRole
	Limit           int
	AfterCreatedAt  time.Time
	AfterID         string
	BeforeCreatedAt time.Time
	BeforeID        string
	OldestFirst     bool
	ExcludeEventID  string
	ExcludeControl  bool
}

type ConversationThreadQuery struct {
	ProjectID   string
	ChannelType domain.ChannelType
	ChannelID   string
	ThreadID    string
}

type ConversationRootMessageQuery struct {
	ProjectID   string
	ChannelType domain.ChannelType
	ChannelID   string
	MessageID   string
}

type ConversationSummaryQuery struct {
	ProjectID       string
	ChannelType     domain.ChannelType
	ChannelID       string
	ThreadID        string
	Scope           domain.ConversationSummaryScope
	Limit           int
	BeforeCreatedAt time.Time
	BeforeID        string
}

type ScheduledWorkflowQuery struct {
	ProjectID      string
	IncludeDeleted bool
	Limit          int
}

func DefaultDBPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return filepath.Join(".db", "opencto.db")
	}
	return filepath.Join(workspaceRoot, ".db", "opencto.db")
}

func DefaultAutoContextLimit(limit int) int {
	if limit <= 0 {
		return defaultAutoContextLimit
	}
	return limit
}

func DefaultConversationHistoryLimit(limit int) int {
	if limit <= 0 {
		return defaultConversationHistoryLimit
	}
	return limit
}

func DefaultConversationMaxContextChars(limit int) int {
	if limit <= 0 {
		return defaultConversationMaxContextChars
	}
	return limit
}

func DefaultConversationSummaryTriggerChars(limit int) int {
	if limit <= 0 {
		return defaultConversationSummaryTrigger
	}
	return limit
}

func DefaultConversationSummaryMaxChars(limit int) int {
	if limit <= 0 {
		return defaultConversationSummaryMaxChars
	}
	return limit
}

func DefaultConversationSummaryRecentMessages(limit int) int {
	if limit <= 0 {
		return defaultConversationSummaryRecent
	}
	return limit
}
