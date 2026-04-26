package domain

import "context"

type ProjectRepository interface {
	Upsert(context.Context, Project) error
	Get(context.Context, string) (Project, error)
}

type EventRepository interface {
	Append(context.Context, Event) error
	ListByProject(context.Context, string, int) ([]Event, error)
}

type WorkItemRepository interface {
	Upsert(context.Context, WorkItem) error
	Get(context.Context, string, string) (WorkItem, error)
	ListPending(context.Context, string) ([]WorkItem, error)
}

type ExecutionAttemptRepository interface {
	Upsert(context.Context, ExecutionAttempt) error
	ListByWorkItem(context.Context, string, string) ([]ExecutionAttempt, error)
}

type MemoryRepository interface {
	UpsertFact(context.Context, MemoryFact) error
	SearchByCategory(context.Context, string, MemoryCategory, string, int) ([]MemoryFact, error)
}

type ToolInvocationRepository interface {
	Upsert(context.Context, ToolInvocation) error
	ListByExecutionAttempt(context.Context, string, string) ([]ToolInvocation, error)
}

type ADRRepository interface {
	Append(context.Context, ADR) error
}

type CredentialRepository interface {
	Upsert(context.Context, CredentialRef) error
	ListByProject(context.Context, string) ([]CredentialRef, error)
}
