package sqlite

import (
	"context"

	"github.com/opencto/opencto/internal/domain"
)

type ProjectRepository struct{ store *Store }
type EventRepository struct{ store *Store }
type WorkItemRepository struct{ store *Store }
type ExecutionAttemptRepository struct{ store *Store }
type MemoryRepository struct{ store *Store }
type ToolInvocationRepository struct{ store *Store }
type ADRRepository struct{ store *Store }
type CredentialRepository struct{ store *Store }

type Repositories struct {
	Projects    domain.ProjectRepository
	Events      domain.EventRepository
	WorkItems   domain.WorkItemRepository
	Executions  domain.ExecutionAttemptRepository
	Memory      domain.MemoryRepository
	ToolCalls   domain.ToolInvocationRepository
	ADRs        domain.ADRRepository
	Credentials domain.CredentialRepository
}

func NewRepositories(store *Store) Repositories {
	return Repositories{
		Projects:    ProjectRepository{store: store},
		Events:      EventRepository{store: store},
		WorkItems:   WorkItemRepository{store: store},
		Executions:  ExecutionAttemptRepository{store: store},
		Memory:      MemoryRepository{store: store},
		ToolCalls:   ToolInvocationRepository{store: store},
		ADRs:        ADRRepository{store: store},
		Credentials: CredentialRepository{store: store},
	}
}

func (r ProjectRepository) Upsert(ctx context.Context, project domain.Project) error {
	return r.store.Upsert(ctx, project)
}

func (r ProjectRepository) Get(ctx context.Context, projectID string) (domain.Project, error) {
	return r.store.Get(ctx, projectID)
}

func (r EventRepository) Append(ctx context.Context, event domain.Event) error {
	return r.store.Append(ctx, event)
}

func (r EventRepository) ListByProject(ctx context.Context, projectID string, limit int) ([]domain.Event, error) {
	return r.store.ListByProject(ctx, projectID, limit)
}

func (r WorkItemRepository) Upsert(ctx context.Context, item domain.WorkItem) error {
	return r.store.UpsertWorkItem(ctx, item)
}

func (r WorkItemRepository) Get(ctx context.Context, projectID, workItemID string) (domain.WorkItem, error) {
	return r.store.GetWorkItem(ctx, projectID, workItemID)
}

func (r WorkItemRepository) ListPending(ctx context.Context, projectID string) ([]domain.WorkItem, error) {
	return r.store.ListPending(ctx, projectID)
}

func (r ExecutionAttemptRepository) Upsert(ctx context.Context, attempt domain.ExecutionAttempt) error {
	return r.store.UpsertExecutionAttempt(ctx, attempt)
}

func (r ExecutionAttemptRepository) ListByWorkItem(ctx context.Context, projectID, workItemID string) ([]domain.ExecutionAttempt, error) {
	return r.store.ListByWorkItem(ctx, projectID, workItemID)
}

func (r MemoryRepository) UpsertFact(ctx context.Context, fact domain.MemoryFact) error {
	return r.store.UpsertFact(ctx, fact)
}

func (r MemoryRepository) SearchByCategory(ctx context.Context, projectID string, category domain.MemoryCategory, query string, limit int) ([]domain.MemoryFact, error) {
	return r.store.SearchByCategory(ctx, projectID, category, query, limit)
}

func (r ToolInvocationRepository) Upsert(ctx context.Context, invocation domain.ToolInvocation) error {
	return r.store.UpsertToolInvocation(ctx, invocation)
}

func (r ToolInvocationRepository) ListByExecutionAttempt(ctx context.Context, projectID, executionAttemptID string) ([]domain.ToolInvocation, error) {
	return r.store.ListByExecutionAttempt(ctx, projectID, executionAttemptID)
}

func (r ADRRepository) Append(ctx context.Context, adr domain.ADR) error {
	return r.store.AppendADR(ctx, adr)
}

func (r CredentialRepository) Upsert(ctx context.Context, ref domain.CredentialRef) error {
	return r.store.UpsertCredential(ctx, ref)
}

func (r CredentialRepository) ListByProject(ctx context.Context, projectID string) ([]domain.CredentialRef, error) {
	return r.store.ListCredentialRefsByProject(ctx, projectID)
}
