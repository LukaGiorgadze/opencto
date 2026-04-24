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

type PlanRepository interface {
	Upsert(context.Context, Plan) error
	Get(context.Context, string, string) (Plan, error)
}

type ExecutionAttemptRepository interface {
	Upsert(context.Context, ExecutionAttempt) error
	ListByWorkItem(context.Context, string, string) ([]ExecutionAttempt, error)
}

type ApprovalRepository interface {
	Upsert(context.Context, ApprovalRequest) error
	GetPendingByWorkItem(context.Context, string, string) ([]ApprovalRequest, error)
	GetByID(context.Context, string, string) (ApprovalRequest, error)
}

type ContradictionRepository interface {
	Upsert(context.Context, PendingContradiction) error
	ListOpen(context.Context, string) ([]PendingContradiction, error)
}

type MemoryRepository interface {
	UpsertFact(context.Context, MemoryFact) error
	SearchByCategory(context.Context, string, MemoryCategory, string, int) ([]MemoryFact, error)
}

type ToolInvocationRepository interface {
	Upsert(context.Context, ToolInvocation) error
	ListByExecutionAttempt(context.Context, string, string) ([]ToolInvocation, error)
}

type ArtifactRepository interface {
	Append(context.Context, Artifact) error
	ListByProject(context.Context, string, ArtifactKind) ([]Artifact, error)
}

type ADRRepository interface {
	Append(context.Context, ADR) error
	ListByProject(context.Context, string) ([]ADR, error)
}

type IntegrationRepository interface {
	Upsert(context.Context, Integration) error
	ListByProject(context.Context, string) ([]Integration, error)
}

type CredentialRepository interface {
	Upsert(context.Context, CredentialRef) error
	ListByProject(context.Context, string) ([]CredentialRef, error)
}
