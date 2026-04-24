package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/opencto/opencto/internal/domain"
)

type Store struct {
	db              *sql.DB
	sqliteVecLoaded bool
}

func Open(path, sqliteVecPath string, busyTimeout time.Duration) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_foreign_keys=on&_busy_timeout=%d", path, busyTimeout.Milliseconds())
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}

	store := &Store{db: db}
	loaded, err := store.pingAndLoadExtension(sqliteVecPath)
	if err != nil {
		db.Close()
		return nil, err
	}
	store.sqliteVecLoaded = loaded
	if err := store.Migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) SQLiteVecLoaded() bool {
	return s.sqliteVecLoaded
}

func (s *Store) Migrate(ctx context.Context) error {
	for _, statement := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "tool_invocations", "result_code", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (s *Store) pingAndLoadExtension(sqliteVecPath string) (bool, error) {
	if err := s.db.Ping(); err != nil {
		return false, err
	}
	if strings.TrimSpace(sqliteVecPath) == "" {
		return false, nil
	}

	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return false, err
	}
	defer conn.Close()

	if err := conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*sqlite3.SQLiteConn)
		if !ok {
			return fmt.Errorf("unexpected sqlite driver connection type %T", driverConn)
		}
		return c.LoadExtension(sqliteVecPath, "")
	}); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) Upsert(ctx context.Context, project domain.Project) error {
	metadata, _ := json.Marshal(project.Metadata)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO projects (id, name, description, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			description = excluded.description,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, project.ID, project.Name, project.Description, metadata, project.CreatedAt.UTC(), project.UpdatedAt.UTC())
	return err
}

func (s *Store) Get(ctx context.Context, projectID string) (domain.Project, error) {
	var project domain.Project
	var metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, description, metadata_json, created_at, updated_at
		FROM projects
		WHERE id = ?
	`, projectID).Scan(&project.ID, &project.Name, &project.Description, &metadata, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Project{}, domain.ErrNotFound
		}
		return domain.Project{}, err
	}
	_ = json.Unmarshal(metadata, &project.Metadata)
	return project, nil
}

func (s *Store) Append(ctx context.Context, event domain.Event) error {
	return s.insert(ctx, `
		INSERT INTO events (id, project_id, kind, channel_id, channel_type, actor_id, actor_name, body, metadata_json, payload_json, provenance_json, correlation_id, causation_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, event.ID, event.ProjectID, event.Kind, event.ChannelID, event.ChannelType, event.ActorID, event.ActorName, event.Body, mustJSON(event.Metadata), mustJSON(event.Payload), mustJSON(event.Provenance), event.CorrelationID, event.CausationID, event.CreatedAt.UTC())
}

func (s *Store) ListByProject(ctx context.Context, projectID string, limit int) ([]domain.Event, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, kind, channel_id, channel_type, actor_id, actor_name, body, metadata_json, payload_json, provenance_json, correlation_id, causation_id, created_at
		FROM events
		WHERE project_id = ?
		ORDER BY created_at DESC
		LIMIT ?
	`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []domain.Event
	for rows.Next() {
		var item domain.Event
		var metadata, payload, provenance []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Kind, &item.ChannelID, &item.ChannelType, &item.ActorID, &item.ActorName, &item.Body, &metadata, &payload, &provenance, &item.CorrelationID, &item.CausationID, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		_ = json.Unmarshal(payload, &item.Payload)
		_ = json.Unmarshal(provenance, &item.Provenance)
		events = append(events, item)
	}
	return events, rows.Err()
}

func (s *Store) UpsertWorkItem(ctx context.Context, item domain.WorkItem) error {
	return s.insert(ctx, `
		INSERT INTO work_items (id, project_id, plan_id, title, description, status, risk_tier, correlation_id, causation_id, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			plan_id = excluded.plan_id,
			title = excluded.title,
			description = excluded.description,
			status = excluded.status,
			risk_tier = excluded.risk_tier,
			correlation_id = excluded.correlation_id,
			causation_id = excluded.causation_id,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, item.ID, item.ProjectID, item.PlanID, item.Title, item.Description, item.Status, int(item.RiskTier), item.CorrelationID, item.CausationID, mustJSON(item.Metadata), item.CreatedAt.UTC(), item.UpdatedAt.UTC())
}

func (s *Store) UpsertPlan(ctx context.Context, plan domain.Plan) error {
	return s.insert(ctx, `
		INSERT INTO plans (id, project_id, event_id, status, summary, decision, steps_json, work_item_ids_json, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			summary = excluded.summary,
			decision = excluded.decision,
			steps_json = excluded.steps_json,
			work_item_ids_json = excluded.work_item_ids_json,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, plan.ID, plan.ProjectID, plan.EventID, plan.Status, plan.Summary, plan.Decision, mustJSON(plan.Steps), mustJSON(plan.WorkItemIDs), mustJSON(plan.Metadata), plan.CreatedAt.UTC(), plan.UpdatedAt.UTC())
}

func (s *Store) GetPlan(ctx context.Context, projectID, planID string) (domain.Plan, error) {
	var plan domain.Plan
	var steps, workItems, metadata []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, event_id, status, summary, decision, steps_json, work_item_ids_json, metadata_json, created_at, updated_at
		FROM plans WHERE project_id = ? AND id = ?
	`, projectID, planID).Scan(&plan.ID, &plan.ProjectID, &plan.EventID, &plan.Status, &plan.Summary, &plan.Decision, &steps, &workItems, &metadata, &plan.CreatedAt, &plan.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.Plan{}, domain.ErrNotFound
		}
		return domain.Plan{}, err
	}
	_ = json.Unmarshal(steps, &plan.Steps)
	_ = json.Unmarshal(workItems, &plan.WorkItemIDs)
	_ = json.Unmarshal(metadata, &plan.Metadata)
	return plan, nil
}

func (s *Store) GetWorkItem(ctx context.Context, projectID, workItemID string) (domain.WorkItem, error) {
	var item domain.WorkItem
	var metadata []byte
	var riskTier int
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, plan_id, title, description, status, risk_tier, correlation_id, causation_id, metadata_json, created_at, updated_at
		FROM work_items WHERE project_id = ? AND id = ?
	`, projectID, workItemID).Scan(&item.ID, &item.ProjectID, &item.PlanID, &item.Title, &item.Description, &item.Status, &riskTier, &item.CorrelationID, &item.CausationID, &metadata, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.WorkItem{}, domain.ErrNotFound
		}
		return domain.WorkItem{}, err
	}
	item.RiskTier = domain.RiskTier(riskTier)
	_ = json.Unmarshal(metadata, &item.Metadata)
	return item, nil
}

func (s *Store) ListPending(ctx context.Context, projectID string) ([]domain.WorkItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT w.id, w.project_id, w.plan_id, w.title, w.description, w.status, w.risk_tier, w.correlation_id, w.causation_id, w.metadata_json, w.created_at, w.updated_at
		FROM work_items w
		JOIN plans p ON w.plan_id = p.id
		WHERE w.project_id = ? AND w.status IN (?, ?, ?, ?)
		  AND p.status NOT IN (?, ?)
		ORDER BY w.created_at ASC
	`, projectID, domain.WorkItemStatusPending, domain.WorkItemStatusPlanning, domain.WorkItemStatusAwaitingApproval, domain.WorkItemStatusReady,
		domain.PlanStatusExecuted, domain.PlanStatusAbandoned)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []domain.WorkItem
	for rows.Next() {
		var item domain.WorkItem
		var metadata []byte
		var riskTier int
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.PlanID, &item.Title, &item.Description, &item.Status, &riskTier, &item.CorrelationID, &item.CausationID, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.RiskTier = domain.RiskTier(riskTier)
		_ = json.Unmarshal(metadata, &item.Metadata)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) UpsertExecutionAttempt(ctx context.Context, attempt domain.ExecutionAttempt) error {
	return s.insert(ctx, `
		INSERT INTO execution_attempts (id, project_id, work_item_id, status, attempt_number, tool, summary, output_summary, correlation_id, causation_id, metadata_json, started_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			attempt_number = excluded.attempt_number,
			tool = excluded.tool,
			summary = excluded.summary,
			output_summary = excluded.output_summary,
			correlation_id = excluded.correlation_id,
			causation_id = excluded.causation_id,
			metadata_json = excluded.metadata_json,
			started_at = excluded.started_at,
			completed_at = excluded.completed_at
	`, attempt.ID, attempt.ProjectID, attempt.WorkItemID, attempt.Status, attempt.Attempt, attempt.Tool, attempt.Summary, attempt.OutputSummary, attempt.CorrelationID, attempt.CausationID, mustJSON(attempt.Metadata), attempt.StartedAt.UTC(), nullableTime(attempt.CompletedAt))
}

func (s *Store) ListByWorkItem(ctx context.Context, projectID, workItemID string) ([]domain.ExecutionAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, work_item_id, status, attempt_number, tool, summary, output_summary, correlation_id, causation_id, metadata_json, started_at, completed_at
		FROM execution_attempts WHERE project_id = ? AND work_item_id = ?
		ORDER BY attempt_number ASC
	`, projectID, workItemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []domain.ExecutionAttempt
	for rows.Next() {
		var item domain.ExecutionAttempt
		var metadata []byte
		var completedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.WorkItemID, &item.Status, &item.Attempt, &item.Tool, &item.Summary, &item.OutputSummary, &item.CorrelationID, &item.CausationID, &metadata, &item.StartedAt, &completedAt); err != nil {
			return nil, err
		}
		if completedAt.Valid {
			item.CompletedAt = &completedAt.Time
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		attempts = append(attempts, item)
	}
	return attempts, rows.Err()
}

func (s *Store) UpsertApproval(ctx context.Context, approval domain.ApprovalRequest) error {
	return s.insert(ctx, `
		INSERT INTO approval_requests (id, project_id, work_item_id, status, risk_tier, requested_action, reason, requested_by, decided_by, metadata_json, created_at, updated_at, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			risk_tier = excluded.risk_tier,
			requested_action = excluded.requested_action,
			reason = excluded.reason,
			requested_by = excluded.requested_by,
			decided_by = excluded.decided_by,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at,
			decided_at = excluded.decided_at
	`, approval.ID, approval.ProjectID, approval.WorkItemID, approval.Status, int(approval.RiskTier), approval.RequestedAction, approval.Reason, approval.RequestedBy, approval.DecidedBy, mustJSON(approval.Metadata), approval.CreatedAt.UTC(), approval.UpdatedAt.UTC(), nullableTime(approval.DecidedAt))
}

func (s *Store) GetPendingByWorkItem(ctx context.Context, projectID, workItemID string) ([]domain.ApprovalRequest, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, work_item_id, status, risk_tier, requested_action, reason, requested_by, decided_by, metadata_json, created_at, updated_at, decided_at
		FROM approval_requests WHERE project_id = ? AND work_item_id = ? AND status = ?
	`, projectID, workItemID, domain.ApprovalStatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var approvals []domain.ApprovalRequest
	for rows.Next() {
		var item domain.ApprovalRequest
		var metadata []byte
		var tier int
		var decidedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.WorkItemID, &item.Status, &tier, &item.RequestedAction, &item.Reason, &item.RequestedBy, &item.DecidedBy, &metadata, &item.CreatedAt, &item.UpdatedAt, &decidedAt); err != nil {
			return nil, err
		}
		item.RiskTier = domain.RiskTier(tier)
		if decidedAt.Valid {
			item.DecidedAt = &decidedAt.Time
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		approvals = append(approvals, item)
	}
	return approvals, rows.Err()
}

func (s *Store) GetByID(ctx context.Context, projectID, approvalID string) (domain.ApprovalRequest, error) {
	var item domain.ApprovalRequest
	var metadata []byte
	var tier int
	var decidedAt sql.NullTime
	err := s.db.QueryRowContext(ctx, `
		SELECT id, project_id, work_item_id, status, risk_tier, requested_action, reason, requested_by, decided_by, metadata_json, created_at, updated_at, decided_at
		FROM approval_requests WHERE project_id = ? AND id = ?
	`, projectID, approvalID).Scan(&item.ID, &item.ProjectID, &item.WorkItemID, &item.Status, &tier, &item.RequestedAction, &item.Reason, &item.RequestedBy, &item.DecidedBy, &metadata, &item.CreatedAt, &item.UpdatedAt, &decidedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return domain.ApprovalRequest{}, domain.ErrNotFound
		}
		return domain.ApprovalRequest{}, err
	}
	item.RiskTier = domain.RiskTier(tier)
	if decidedAt.Valid {
		item.DecidedAt = &decidedAt.Time
	}
	_ = json.Unmarshal(metadata, &item.Metadata)
	return item, nil
}

func (s *Store) UpsertContradiction(ctx context.Context, contradiction domain.PendingContradiction) error {
	return s.insert(ctx, `
		INSERT INTO pending_contradictions (id, project_id, status, topic, existing_fact, incoming_fact, resolution, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			status = excluded.status,
			topic = excluded.topic,
			existing_fact = excluded.existing_fact,
			incoming_fact = excluded.incoming_fact,
			resolution = excluded.resolution,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, contradiction.ID, contradiction.ProjectID, contradiction.Status, contradiction.Topic, contradiction.ExistingFact, contradiction.IncomingFact, contradiction.Resolution, mustJSON(contradiction.Metadata), contradiction.CreatedAt.UTC(), contradiction.UpdatedAt.UTC())
}

func (s *Store) ListOpen(ctx context.Context, projectID string) ([]domain.PendingContradiction, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, status, topic, existing_fact, incoming_fact, resolution, metadata_json, created_at, updated_at
		FROM pending_contradictions WHERE project_id = ? AND status = ?
	`, projectID, domain.ContradictionStatusOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contradictions []domain.PendingContradiction
	for rows.Next() {
		var item domain.PendingContradiction
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Status, &item.Topic, &item.ExistingFact, &item.IncomingFact, &item.Resolution, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		contradictions = append(contradictions, item)
	}
	return contradictions, rows.Err()
}

func (s *Store) UpsertFact(ctx context.Context, fact domain.MemoryFact) error {
	return s.insert(ctx, `
		INSERT INTO memory_facts (id, project_id, category, key_name, value_text, status, embedding_id, provenance_json, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			category = excluded.category,
			key_name = excluded.key_name,
			value_text = excluded.value_text,
			status = excluded.status,
			embedding_id = excluded.embedding_id,
			provenance_json = excluded.provenance_json,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, fact.ID, fact.ProjectID, fact.Category, fact.Key, fact.Value, fact.Status, fact.EmbeddingID, mustJSON(fact.Provenance), mustJSON(fact.Metadata), fact.CreatedAt.UTC(), fact.UpdatedAt.UTC())
}

func (s *Store) SearchByCategory(ctx context.Context, projectID string, category domain.MemoryCategory, query string, limit int) ([]domain.MemoryFact, error) {
	pattern := "%" + strings.ToLower(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, category, key_name, value_text, status, embedding_id, provenance_json, metadata_json, created_at, updated_at
		FROM memory_facts
		WHERE project_id = ? AND category = ? AND (LOWER(key_name) LIKE ? OR LOWER(value_text) LIKE ?)
		ORDER BY updated_at DESC
		LIMIT ?
	`, projectID, category, pattern, pattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var facts []domain.MemoryFact
	for rows.Next() {
		var item domain.MemoryFact
		var provenance, metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Category, &item.Key, &item.Value, &item.Status, &item.EmbeddingID, &provenance, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(provenance, &item.Provenance)
		_ = json.Unmarshal(metadata, &item.Metadata)
		facts = append(facts, item)
	}
	return facts, rows.Err()
}

func (s *Store) UpsertToolInvocation(ctx context.Context, invocation domain.ToolInvocation) error {
	return s.insert(ctx, `
		INSERT INTO tool_invocations (id, project_id, execution_attempt_id, requested_intent, chosen_tool, fallback_candidates_json, risk_tier, working_directory, timeout_seconds, input_summary, output_summary, result_code, error_details, compensation_notes, metadata_json, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			requested_intent = excluded.requested_intent,
			chosen_tool = excluded.chosen_tool,
			fallback_candidates_json = excluded.fallback_candidates_json,
			risk_tier = excluded.risk_tier,
			working_directory = excluded.working_directory,
			timeout_seconds = excluded.timeout_seconds,
			input_summary = excluded.input_summary,
			output_summary = excluded.output_summary,
			result_code = excluded.result_code,
			error_details = excluded.error_details,
			compensation_notes = excluded.compensation_notes,
			metadata_json = excluded.metadata_json,
			completed_at = excluded.completed_at
	`, invocation.ID, invocation.ProjectID, invocation.ExecutionAttemptID, invocation.RequestedIntent, invocation.ChosenTool, mustJSON(invocation.FallbackCandidates), int(invocation.RiskTier), invocation.WorkingDirectory, invocation.TimeoutSeconds, invocation.InputSummary, invocation.OutputSummary, invocation.ResultCode, invocation.ErrorDetails, invocation.CompensationNotes, mustJSON(invocation.Metadata), invocation.CreatedAt.UTC(), nullableTime(invocation.CompletedAt))
}

func (s *Store) ListByExecutionAttempt(ctx context.Context, projectID, executionAttemptID string) ([]domain.ToolInvocation, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, execution_attempt_id, requested_intent, chosen_tool, fallback_candidates_json, risk_tier, working_directory, timeout_seconds, input_summary, output_summary, result_code, error_details, compensation_notes, metadata_json, created_at, completed_at
		FROM tool_invocations WHERE project_id = ? AND execution_attempt_id = ?
	`, projectID, executionAttemptID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var invocations []domain.ToolInvocation
	for rows.Next() {
		var item domain.ToolInvocation
		var fallbackCandidates, metadata []byte
		var tier int
		var completedAt sql.NullTime
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ExecutionAttemptID, &item.RequestedIntent, &item.ChosenTool, &fallbackCandidates, &tier, &item.WorkingDirectory, &item.TimeoutSeconds, &item.InputSummary, &item.OutputSummary, &item.ResultCode, &item.ErrorDetails, &item.CompensationNotes, &metadata, &item.CreatedAt, &completedAt); err != nil {
			return nil, err
		}
		item.RiskTier = domain.RiskTier(tier)
		if completedAt.Valid {
			item.CompletedAt = &completedAt.Time
		}
		_ = json.Unmarshal(fallbackCandidates, &item.FallbackCandidates)
		_ = json.Unmarshal(metadata, &item.Metadata)
		invocations = append(invocations, item)
	}
	return invocations, rows.Err()
}

func (s *Store) AppendArtifact(ctx context.Context, artifact domain.Artifact) error {
	return s.insert(ctx, `
		INSERT INTO artifacts (id, project_id, kind, uri, digest, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, artifact.ID, artifact.ProjectID, artifact.Kind, artifact.URI, artifact.Digest, mustJSON(artifact.Metadata), artifact.CreatedAt.UTC())
}

func (s *Store) ListArtifactsByProject(ctx context.Context, projectID string, kind domain.ArtifactKind) ([]domain.Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, kind, uri, digest, metadata_json, created_at
		FROM artifacts WHERE project_id = ? AND kind = ?
		ORDER BY created_at DESC
	`, projectID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var artifacts []domain.Artifact
	for rows.Next() {
		var item domain.Artifact
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Kind, &item.URI, &item.Digest, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		artifacts = append(artifacts, item)
	}
	return artifacts, rows.Err()
}

func (s *Store) AppendADR(ctx context.Context, adr domain.ADR) error {
	return s.insert(ctx, `
		INSERT INTO adrs (id, project_id, title, summary, path, commit_sha, metadata_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, adr.ID, adr.ProjectID, adr.Title, adr.Summary, adr.Path, adr.CommitSHA, mustJSON(adr.Metadata), adr.CreatedAt.UTC())
}

func (s *Store) ListADRsByProject(ctx context.Context, projectID string) ([]domain.ADR, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, title, summary, path, commit_sha, metadata_json, created_at
		FROM adrs WHERE project_id = ?
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var adrs []domain.ADR
	for rows.Next() {
		var item domain.ADR
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Title, &item.Summary, &item.Path, &item.CommitSHA, &metadata, &item.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		adrs = append(adrs, item)
	}
	return adrs, rows.Err()
}

func (s *Store) UpsertIntegration(ctx context.Context, integration domain.Integration) error {
	return s.insert(ctx, `
		INSERT INTO integrations (id, project_id, kind, status, external_ref, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			kind = excluded.kind,
			status = excluded.status,
			external_ref = excluded.external_ref,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, integration.ID, integration.ProjectID, integration.Kind, integration.Status, integration.ExternalRef, mustJSON(integration.Metadata), integration.CreatedAt.UTC(), integration.UpdatedAt.UTC())
}

func (s *Store) ListIntegrationsByProject(ctx context.Context, projectID string) ([]domain.Integration, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, kind, status, external_ref, metadata_json, created_at, updated_at
		FROM integrations WHERE project_id = ?
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var integrations []domain.Integration
	for rows.Next() {
		var item domain.Integration
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Kind, &item.Status, &item.ExternalRef, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		integrations = append(integrations, item)
	}
	return integrations, rows.Err()
}

func (s *Store) UpsertCredential(ctx context.Context, ref domain.CredentialRef) error {
	return s.insert(ctx, `
		INSERT INTO credential_refs (id, project_id, provider, handle, scope, metadata_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			provider = excluded.provider,
			handle = excluded.handle,
			scope = excluded.scope,
			metadata_json = excluded.metadata_json,
			updated_at = excluded.updated_at
	`, ref.ID, ref.ProjectID, ref.Provider, ref.Handle, ref.Scope, mustJSON(ref.Metadata), ref.CreatedAt.UTC(), ref.UpdatedAt.UTC())
}

func (s *Store) ListCredentialRefsByProject(ctx context.Context, projectID string) ([]domain.CredentialRef, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, project_id, provider, handle, scope, metadata_json, created_at, updated_at
		FROM credential_refs WHERE project_id = ?
		ORDER BY created_at DESC
	`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []domain.CredentialRef
	for rows.Next() {
		var item domain.CredentialRef
		var metadata []byte
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.Provider, &item.Handle, &item.Scope, &metadata, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(metadata, &item.Metadata)
		refs = append(refs, item)
	}
	return refs, rows.Err()
}

func (s *Store) insert(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *Store) ensureColumn(ctx context.Context, tableName, columnName, definition string) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA table_info("+tableName+")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal any
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return err
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", tableName, columnName, definition))
	return err
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		return []byte("{}")
	}
	return data
}

func nullableTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS events (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		channel_id TEXT NOT NULL DEFAULT '',
		channel_type TEXT NOT NULL DEFAULT '',
		actor_id TEXT NOT NULL DEFAULT '',
		actor_name TEXT NOT NULL DEFAULT '',
		body TEXT NOT NULL,
		metadata_json BLOB NOT NULL DEFAULT '{}',
		payload_json BLOB NOT NULL DEFAULT '{}',
		provenance_json BLOB NOT NULL DEFAULT '{}',
		correlation_id TEXT NOT NULL DEFAULT '',
		causation_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS work_items (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		plan_id TEXT NOT NULL DEFAULT '',
		title TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL,
		risk_tier INTEGER NOT NULL,
		correlation_id TEXT NOT NULL DEFAULT '',
		causation_id TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS plans (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		event_id TEXT NOT NULL,
		status TEXT NOT NULL,
		summary TEXT NOT NULL,
		decision TEXT NOT NULL,
		steps_json BLOB NOT NULL DEFAULT '[]',
		work_item_ids_json BLOB NOT NULL DEFAULT '[]',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS execution_attempts (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		work_item_id TEXT NOT NULL,
		status TEXT NOT NULL,
		attempt_number INTEGER NOT NULL,
		tool TEXT NOT NULL,
		summary TEXT NOT NULL DEFAULT '',
		output_summary TEXT NOT NULL DEFAULT '',
		correlation_id TEXT NOT NULL DEFAULT '',
		causation_id TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		started_at TIMESTAMP NOT NULL,
		completed_at TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS approval_requests (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		work_item_id TEXT NOT NULL,
		status TEXT NOT NULL,
		risk_tier INTEGER NOT NULL,
		requested_action TEXT NOT NULL,
		reason TEXT NOT NULL,
		requested_by TEXT NOT NULL DEFAULT '',
		decided_by TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		decided_at TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS pending_contradictions (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		status TEXT NOT NULL,
		topic TEXT NOT NULL,
		existing_fact TEXT NOT NULL DEFAULT '',
		incoming_fact TEXT NOT NULL DEFAULT '',
		resolution TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS memory_facts (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		category TEXT NOT NULL,
		key_name TEXT NOT NULL,
		value_text TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT '',
		embedding_id TEXT NOT NULL DEFAULT '',
		provenance_json BLOB NOT NULL DEFAULT '{}',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS memory_fact_embeddings (
		fact_id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		category TEXT NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		dimensions INTEGER NOT NULL,
		vector_json BLOB NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS tool_invocations (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		execution_attempt_id TEXT NOT NULL,
		requested_intent TEXT NOT NULL,
		chosen_tool TEXT NOT NULL,
		fallback_candidates_json BLOB NOT NULL DEFAULT '[]',
		risk_tier INTEGER NOT NULL,
		working_directory TEXT NOT NULL DEFAULT '',
		timeout_seconds INTEGER NOT NULL DEFAULT 0,
		input_summary TEXT NOT NULL DEFAULT '',
		output_summary TEXT NOT NULL DEFAULT '',
		result_code TEXT NOT NULL DEFAULT '',
		error_details TEXT NOT NULL DEFAULT '',
		compensation_notes TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		completed_at TIMESTAMP
	)`,
	`CREATE TABLE IF NOT EXISTS artifacts (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		uri TEXT NOT NULL,
		digest TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS adrs (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		title TEXT NOT NULL,
		summary TEXT NOT NULL,
		path TEXT NOT NULL,
		commit_sha TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS integrations (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		external_ref TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE TABLE IF NOT EXISTS credential_refs (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		provider TEXT NOT NULL,
		handle TEXT NOT NULL,
		scope TEXT NOT NULL DEFAULT '',
		metadata_json BLOB NOT NULL DEFAULT '{}',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_events_project_created_at ON events(project_id, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_facts_project_category ON memory_facts(project_id, category, updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_memory_fact_embeddings_project_category ON memory_fact_embeddings(project_id, category, updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_approvals_project_status ON approval_requests(project_id, status, created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_contradictions_project_status ON pending_contradictions(project_id, status, created_at DESC)`,
}
