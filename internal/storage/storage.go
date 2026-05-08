package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

const (
	ProviderSQLite = "sqlite"

	defaultAutoContextLimit            = 5
	defaultConversationHistoryLimit    = 10
	defaultConversationMaxContextChars = 8000
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
	AppendEvent(context.Context, domain.Event) (EventAppendResult, error)
	ListPendingWorkItems(context.Context, string) ([]domain.WorkItem, error)
	UpsertWorkItems(context.Context, []domain.WorkItem) error
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
	UpsertConversationMessage(context.Context, domain.ConversationMessage) error
	ListConversationMessages(context.Context, ConversationQuery) ([]domain.ConversationMessage, error)
	RememberMemory(context.Context, domain.Memory) (domain.Memory, error)
	SearchMemories(context.Context, domain.MemorySearchRequest) ([]domain.Memory, error)
	ListMemories(context.Context, domain.MemoryListRequest) ([]domain.Memory, error)
	UpdateMemory(context.Context, domain.MemoryUpdateRequest) (domain.MemoryUpdateResult, error)
	UpsertMemoryEmbedding(context.Context, domain.MemoryEmbedding) error
	DeleteMemoryEmbeddings(context.Context, []string) error
	ForgetMemory(context.Context, string, string) (bool, error)
	ForgetMemories(context.Context, domain.MemoryForgetRequest) (domain.MemoryForgetResult, error)
}

type ConversationScope string

const (
	ConversationScopeProject ConversationScope = "project"
	ConversationScopeChannel ConversationScope = "channel"
	ConversationScopeThread  ConversationScope = "thread"
)

type ConversationQuery struct {
	ProjectID      string
	ChannelType    domain.ChannelType
	ChannelID      string
	ThreadID       string
	Scope          ConversationScope
	Roles          []domain.ConversationRole
	Limit          int
	ExcludeEventID string
	ExcludeControl bool
}

func DefaultDBPath(workspaceRoot string) string {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if workspaceRoot == "" {
		return filepath.Join("db", "opencto.db")
	}
	return filepath.Join(workspaceRoot, "db", "opencto.db")
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
