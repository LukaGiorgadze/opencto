package storage

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

const (
	ProviderSQLite = "sqlite"

	defaultAutoContextLimit = 5
)

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
	RememberMemory(context.Context, domain.Memory) (domain.Memory, error)
	SearchMemories(context.Context, domain.MemorySearchRequest) ([]domain.Memory, error)
	ForgetMemory(context.Context, string, string) (bool, error)
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
