package sqlite

import (
	"context"

	"github.com/opencto/opencto/internal/domain"
)

type ProjectRepository struct{ store *Store }
type EventRepository struct{ store *Store }
type WorkItemRepository struct{ store *Store }
type PlanRepository struct{ store *Store }
type ExecutionAttemptRepository struct{ store *Store }
type ApprovalRepository struct{ store *Store }
type ContradictionRepository struct{ store *Store }
type MemoryRepository struct{ store *Store }
type ToolInvocationRepository struct{ store *Store }
type ArtifactRepository struct{ store *Store }
type ADRRepository struct{ store *Store }
type IntegrationRepository struct{ store *Store }
type CredentialRepository struct{ store *Store }

type Repositories struct {
	Projects       domain.ProjectRepository
	Events         domain.EventRepository
	WorkItems      domain.WorkItemRepository
	Plans          domain.PlanRepository
	Executions     domain.ExecutionAttemptRepository
	Approvals      domain.ApprovalRepository
	Contradictions domain.ContradictionRepository
	Memory         domain.MemoryRepository
	ToolCalls      domain.ToolInvocationRepository
	Artifacts      domain.ArtifactRepository
	ADRs           domain.ADRRepository
	Integrations   domain.IntegrationRepository
	Credentials    domain.CredentialRepository
}

func NewRepositories(store *Store) Repositories {
	return Repositories{
		Projects:       ProjectRepository{store: store},
		Events:         EventRepository{store: store},
		WorkItems:      WorkItemRepository{store: store},
		Plans:          PlanRepository{store: store},
		Executions:     ExecutionAttemptRepository{store: store},
		Approvals:      ApprovalRepository{store: store},
		Contradictions: ContradictionRepository{store: store},
		Memory:         MemoryRepository{store: store},
		ToolCalls:      ToolInvocationRepository{store: store},
		Artifacts:      ArtifactRepository{store: store},
		ADRs:           ADRRepository{store: store},
		Integrations:   IntegrationRepository{store: store},
		Credentials:    CredentialRepository{store: store},
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

func (r PlanRepository) Upsert(ctx context.Context, plan domain.Plan) error {
	return r.store.UpsertPlan(ctx, plan)
}

func (r PlanRepository) Get(ctx context.Context, projectID, planID string) (domain.Plan, error) {
	return r.store.GetPlan(ctx, projectID, planID)
}

func (r ExecutionAttemptRepository) Upsert(ctx context.Context, attempt domain.ExecutionAttempt) error {
	return r.store.UpsertExecutionAttempt(ctx, attempt)
}

func (r ExecutionAttemptRepository) ListByWorkItem(ctx context.Context, projectID, workItemID string) ([]domain.ExecutionAttempt, error) {
	return r.store.ListByWorkItem(ctx, projectID, workItemID)
}

func (r ApprovalRepository) Upsert(ctx context.Context, approval domain.ApprovalRequest) error {
	return r.store.UpsertApproval(ctx, approval)
}

func (r ApprovalRepository) GetPendingByWorkItem(ctx context.Context, projectID, workItemID string) ([]domain.ApprovalRequest, error) {
	return r.store.GetPendingByWorkItem(ctx, projectID, workItemID)
}

func (r ApprovalRepository) GetByID(ctx context.Context, projectID, approvalID string) (domain.ApprovalRequest, error) {
	return r.store.GetByID(ctx, projectID, approvalID)
}

func (r ContradictionRepository) Upsert(ctx context.Context, contradiction domain.PendingContradiction) error {
	return r.store.UpsertContradiction(ctx, contradiction)
}

func (r ContradictionRepository) ListOpen(ctx context.Context, projectID string) ([]domain.PendingContradiction, error) {
	return r.store.ListOpen(ctx, projectID)
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

func (r ArtifactRepository) Append(ctx context.Context, artifact domain.Artifact) error {
	return r.store.AppendArtifact(ctx, artifact)
}

func (r ArtifactRepository) ListByProject(ctx context.Context, projectID string, kind domain.ArtifactKind) ([]domain.Artifact, error) {
	return r.store.ListArtifactsByProject(ctx, projectID, kind)
}

func (r ADRRepository) Append(ctx context.Context, adr domain.ADR) error {
	return r.store.AppendADR(ctx, adr)
}

func (r ADRRepository) ListByProject(ctx context.Context, projectID string) ([]domain.ADR, error) {
	return r.store.ListADRsByProject(ctx, projectID)
}

func (r IntegrationRepository) Upsert(ctx context.Context, integration domain.Integration) error {
	return r.store.UpsertIntegration(ctx, integration)
}

func (r IntegrationRepository) ListByProject(ctx context.Context, projectID string) ([]domain.Integration, error) {
	return r.store.ListIntegrationsByProject(ctx, projectID)
}

func (r CredentialRepository) Upsert(ctx context.Context, ref domain.CredentialRef) error {
	return r.store.UpsertCredential(ctx, ref)
}

func (r CredentialRepository) ListByProject(ctx context.Context, projectID string) ([]domain.CredentialRef, error) {
	return r.store.ListCredentialRefsByProject(ctx, projectID)
}
