package activities

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	goruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/policy"
	"github.com/opencto/opencto/internal/runtime/signals"
	toolregistry "github.com/opencto/opencto/internal/tools"
	"github.com/opencto/opencto/internal/tools/git"
	"github.com/opencto/opencto/internal/tools/shell"
)

type ProjectStore interface {
	Append(context.Context, domain.Event) error
	ListByProject(context.Context, string, int) ([]domain.Event, error)
	ListPending(context.Context, string) ([]domain.WorkItem, error)
	ListADRsByProject(context.Context, string) ([]domain.ADR, error)
	ListIntegrationsByProject(context.Context, string) ([]domain.Integration, error)
	UpsertPlan(context.Context, domain.Plan) error
	UpsertWorkItem(context.Context, domain.WorkItem) error
	GetWorkItem(context.Context, string, string) (domain.WorkItem, error)
	UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error
	UpsertApproval(context.Context, domain.ApprovalRequest) error
	GetByID(context.Context, string, string) (domain.ApprovalRequest, error)
	UpsertToolInvocation(context.Context, domain.ToolInvocation) error
	AppendADR(context.Context, domain.ADR) error
	UpsertFact(context.Context, domain.MemoryFact) error
	UpsertFactEmbedding(context.Context, string, string, domain.MemoryCategory, string, []float32) error
	SearchByCategory(context.Context, string, domain.MemoryCategory, string, int) ([]domain.MemoryFact, error)
	SearchByCategorySimilar(context.Context, string, domain.MemoryCategory, []float32, int) ([]domain.MemoryFact, error)
	ListOpen(context.Context, string) ([]domain.PendingContradiction, error)
	UpsertContradiction(context.Context, domain.PendingContradiction) error
}

type SemanticEmbedder interface {
	EmbedDocuments(context.Context, []string) ([][]float32, error)
	EmbedQuery(context.Context, string) ([]float32, error)
}

type Reporter interface {
	Report(context.Context, domain.Event, string) error
}

type Activities struct {
	Store             ProjectStore
	Engine            agent.Engine
	Policy            policy.Engine
	Shell             shell.Executor
	ADRWriter         *git.ADRWriter
	Reporter          Reporter
	Project           domain.Project
	WorkspaceRoot     string
	AutonomyThreshold int
	AvailableSkills   []string
	MemoryEmbedder    SemanticEmbedder
	EmbeddingModel    string
	Logger            *slog.Logger
}

type ToolSelectionRequest struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	Decision           agent.DecisionOutput      `json:"decision"`
	CurrentWorkItemID  string                    `json:"current_work_item_id,omitempty"`
	Feedback           *agent.ExecutionFeedback  `json:"feedback,omitempty"`
	ExecutionCycle     int                       `json:"execution_cycle"`
	ObservationHistory []agent.ExecutionFeedback `json:"observation_history,omitempty"`
}

type ToolSelectionResult struct {
	Action             agent.AgentLoopAction `json:"action,omitempty"`
	WorkItemID         string                `json:"work_item_id,omitempty"`
	WorkItemStatus     domain.WorkItemStatus `json:"work_item_status,omitempty"`
	ObservationSummary string                `json:"observation_summary,omitempty"`
	ToolChoice         *agent.ToolChoice     `json:"tool_choice,omitempty"`
	ResponseMessage    string                `json:"response_message,omitempty"`
}

type ExecuteToolRequest struct {
	ProjectID  string           `json:"project_id"`
	WorkItemID string           `json:"work_item_id"`
	RiskTier   domain.RiskTier  `json:"risk_tier"`
	ToolChoice agent.ToolChoice `json:"tool_choice"`
}

type ExecuteToolResult struct {
	Cycle            int                    `json:"cycle"`
	WorkItemID       string                 `json:"work_item_id,omitempty"`
	Tool             domain.ToolType        `json:"tool,omitempty"`
	Status           domain.ExecutionStatus `json:"status"`
	RequestedAction  string                 `json:"requested_action,omitempty"`
	Command          string                 `json:"command,omitempty"`
	Args             []string               `json:"args,omitempty"`
	Observation      string                 `json:"observation,omitempty"`
	Error            string                 `json:"error,omitempty"`
	WorkingDirectory string                 `json:"working_directory,omitempty"`
	ResultCode       string                 `json:"result_code,omitempty"`
}

const maxObservationSummaryLength = 1500

func (a *Activities) LoadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	return a.loadContext(ctx, event)
}

func (a *Activities) loadDecisionInput(ctx context.Context, event domain.Event) (agent.DecisionInput, error) {
	loaded, err := a.loadContext(ctx, event)
	if err != nil {
		return agent.DecisionInput{}, err
	}
	return agent.DecisionInput{
		ProjectID: event.ProjectID,
		Context:   loaded,
	}, nil
}

func (a *Activities) loadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	recentConversation, err := a.recentConversationFacts(ctx, event, 6)
	if err != nil {
		return agent.Context{}, err
	}
	recentEvents, err := a.Store.ListByProject(ctx, event.ProjectID, 12)
	if err != nil {
		return agent.Context{}, err
	}
	contradictions, err := a.Store.ListOpen(ctx, event.ProjectID)
	if err != nil {
		return agent.Context{}, err
	}
	contradictions, err = a.reconcileOpenContradictions(ctx, event, contradictions, recentConversation, recentEvents)
	if err != nil {
		return agent.Context{}, err
	}
	queryEmbedding, hasQueryEmbedding := a.embedMemoryQuery(ctx, event.ProjectID, event.Body)
	conversation, err := a.searchMemoryWithQueryEmbedding(ctx, event.ProjectID, domain.MemoryCategoryConversation, event.Body, 10, queryEmbedding, hasQueryEmbedding)
	if err != nil {
		return agent.Context{}, err
	}
	conversation = mergeConversationFacts(
		conversation,
		recentConversation,
		syntheticConversationFromEvents(recentEvents, event, 4),
	)
	facts, err := a.searchMemoryWithQueryEmbedding(ctx, event.ProjectID, domain.MemoryCategoryProjectFact, event.Body, 10, queryEmbedding, hasQueryEmbedding)
	if err != nil {
		return agent.Context{}, err
	}
	activeWorkItems, err := a.Store.ListPending(ctx, event.ProjectID)
	if err != nil {
		return agent.Context{}, err
	}
	integrations, err := a.Store.ListIntegrationsByProject(ctx, event.ProjectID)
	if err != nil {
		return agent.Context{}, err
	}
	recentDecisions, err := a.Store.ListADRsByProject(ctx, event.ProjectID)
	if err != nil {
		return agent.Context{}, err
	}
	if len(recentDecisions) > 3 {
		recentDecisions = recentDecisions[:3]
	}

	project := a.Project
	if strings.TrimSpace(project.ID) == "" {
		project.ID = event.ProjectID
	}
	return agent.Context{
		Event:              event,
		Project:            project,
		ConversationMemory: conversation,
		ProjectFacts:       facts,
		OpenContradictions: contradictions,
		Integrations:       integrations,
		ActiveWorkItems:    activeWorkItems,
		RecentDecisions:    recentDecisions,
	}, nil
}

func (a *Activities) recentConversationFacts(ctx context.Context, event domain.Event, limit int) ([]domain.MemoryFact, error) {
	facts, err := a.Store.SearchByCategory(ctx, event.ProjectID, domain.MemoryCategoryConversation, "", limit*3)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || len(facts) == 0 {
		return facts, nil
	}

	prioritized := make([]domain.MemoryFact, 0, len(facts))
	for _, fact := range facts {
		if event.ChannelID != "" && fact.Provenance.SourceID == event.ChannelID {
			prioritized = append(prioritized, fact)
		}
	}
	for _, fact := range facts {
		if event.ChannelID != "" && fact.Provenance.SourceID == event.ChannelID {
			continue
		}
		prioritized = append(prioritized, fact)
	}
	if len(prioritized) > limit {
		prioritized = prioritized[:limit]
	}
	return prioritized, nil
}

func (a *Activities) reconcileOpenContradictions(ctx context.Context, event domain.Event, contradictions []domain.PendingContradiction, recentConversation []domain.MemoryFact, recentEvents []domain.Event) ([]domain.PendingContradiction, error) {
	if len(contradictions) == 0 {
		return contradictions, nil
	}
	if !hasRecentClarificationQuestion(recentConversation) {
		return contradictions, nil
	}

	timeline := chronologicalChannelEvents(recentEvents, event)
	if len(timeline) == 0 {
		return contradictions, nil
	}

	now := time.Now().UTC()
	resolvedAny := false
	for idx := range contradictions {
		if !shouldResolveContradiction(contradictions[idx], timeline, event, len(contradictions)) {
			continue
		}
		if contradictions[idx].Metadata == nil {
			contradictions[idx].Metadata = map[string]string{}
		}
		contradictions[idx].Status = domain.ContradictionStatusResolved
		contradictions[idx].Resolution = contradictionResolutionSummary(timeline, event)
		contradictions[idx].Metadata["resolved_by_event_id"] = event.ID
		contradictions[idx].Metadata["resolution_source"] = "follow_up_context"
		contradictions[idx].UpdatedAt = now
		if err := a.Store.UpsertContradiction(ctx, contradictions[idx]); err != nil {
			return nil, err
		}
		resolvedAny = true
	}

	if !resolvedAny {
		return contradictions, nil
	}
	return a.Store.ListOpen(ctx, event.ProjectID)
}

func mergeConversationFacts(groups ...[]domain.MemoryFact) []domain.MemoryFact {
	merged := make([]domain.MemoryFact, 0)
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, fact := range group {
			key := strings.TrimSpace(fact.ID)
			if key == "" {
				key = strings.TrimSpace(fact.Value)
			}
			if key == "" {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, fact)
		}
	}
	return merged
}

func syntheticConversationFromEvents(events []domain.Event, current domain.Event, limit int) []domain.MemoryFact {
	if limit <= 0 {
		return nil
	}

	prioritized := make([]domain.Event, 0, len(events))
	for _, item := range events {
		if item.ID == current.ID || strings.TrimSpace(item.Body) == "" {
			continue
		}
		if current.ChannelID != "" && item.ChannelID != current.ChannelID {
			continue
		}
		prioritized = append(prioritized, item)
		if len(prioritized) == limit {
			break
		}
	}

	facts := make([]domain.MemoryFact, 0, len(prioritized))
	for _, item := range prioritized {
		actor := strings.TrimSpace(item.ActorName)
		if actor == "" {
			actor = "user"
		}
		facts = append(facts, domain.MemoryFact{
			ID:        "event:" + item.ID,
			ProjectID: item.ProjectID,
			Category:  domain.MemoryCategoryConversation,
			Key:       item.ID,
			Value:     actor + ": " + strings.TrimSpace(item.Body),
			Provenance: domain.Provenance{
				Source:     item.Provenance.Source,
				SourceID:   item.ChannelID,
				Actor:      actor,
				ObservedAt: item.CreatedAt,
			},
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.CreatedAt,
			Metadata: map[string]string{
				"speaker":  "user",
				"event_id": item.ID,
			},
		})
	}
	return facts
}

func chronologicalChannelEvents(events []domain.Event, current domain.Event) []domain.Event {
	filtered := make([]domain.Event, 0, len(events))
	for idx := len(events) - 1; idx >= 0; idx-- {
		item := events[idx]
		if current.ChannelID != "" && item.ChannelID != current.ChannelID {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

func shouldResolveContradiction(contradiction domain.PendingContradiction, timeline []domain.Event, current domain.Event, openCount int) bool {
	sourceID := strings.TrimSpace(contradiction.Metadata["event_id"])
	if sourceID == "" {
		return openCount == 1 && hasContinuationEvent(timeline)
	}

	seenSource := false
	for _, item := range timeline {
		if item.ID == sourceID {
			seenSource = true
			continue
		}
		if !seenSource {
			continue
		}
		if looksLikeContinuationMessage(item.Body) {
			return true
		}
	}

	return openCount == 1 && looksLikeContinuationMessage(current.Body)
}

func hasRecentClarificationQuestion(facts []domain.MemoryFact) bool {
	for _, fact := range facts {
		value := strings.ToLower(strings.TrimSpace(fact.Value))
		if value == "" {
			continue
		}
		if strings.Contains(value, "clarification") ||
			strings.Contains(value, "to continue") ||
			strings.Contains(value, "should i") ||
			strings.Contains(value, "which ") ||
			strings.Contains(value, "what is the exact path") ||
			strings.Contains(value, "blocked by") ||
			strings.Contains(value, "?") {
			return true
		}
	}
	return false
}

func hasContinuationEvent(events []domain.Event) bool {
	for _, item := range events {
		if looksLikeContinuationMessage(item.Body) {
			return true
		}
	}
	return false
}

func looksLikeContinuationMessage(body string) bool {
	lower := strings.ToLower(strings.TrimSpace(body))
	if lower == "" {
		return false
	}

	if isLikelyPath(lower) {
		return true
	}

	switch lower {
	case "yes", "y", "no", "n", "do it", "go ahead", "proceed", "continue":
		return true
	}

	return strings.HasPrefix(lower, "yes ") ||
		strings.HasPrefix(lower, "no ") ||
		strings.HasPrefix(lower, "actually") ||
		strings.HasPrefix(lower, "use ") ||
		strings.HasPrefix(lower, "remove ") ||
		strings.HasPrefix(lower, "set ") ||
		strings.HasPrefix(lower, "run ") ||
		strings.Contains(lower, " instead ") ||
		strings.Contains(lower, " path") ||
		strings.Contains(lower, " folder") ||
		strings.Contains(body, "`")
}

func isLikelyPath(value string) bool {
	return strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "~/") ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		strings.Contains(value, "/") && (strings.Contains(value, ".git") || strings.Contains(value, "hello-world")) ||
		strings.Contains(value, ":\\")
}

func contradictionResolutionSummary(events []domain.Event, current domain.Event) string {
	for idx := len(events) - 1; idx >= 0; idx-- {
		if looksLikeContinuationMessage(events[idx].Body) {
			return "Auto-resolved from follow-up message: " + strings.TrimSpace(events[idx].Body)
		}
	}
	return "Auto-resolved from follow-up message: " + strings.TrimSpace(current.Body)
}

func (a *Activities) searchMemory(ctx context.Context, projectID string, category domain.MemoryCategory, query string, limit int) ([]domain.MemoryFact, error) {
	queryEmbedding, ok := a.embedMemoryQuery(ctx, projectID, query)
	return a.searchMemoryWithQueryEmbedding(ctx, projectID, category, query, limit, queryEmbedding, ok)
}

func (a *Activities) embedMemoryQuery(ctx context.Context, projectID, query string) ([]float32, bool) {
	if a.MemoryEmbedder == nil || strings.TrimSpace(query) == "" {
		return nil, false
	}
	embedding, err := a.MemoryEmbedder.EmbedQuery(ctx, query)
	if err != nil {
		a.warn("semantic memory query failed; falling back to text search",
			slog.String("project_id", projectID),
			slog.String("error", err.Error()),
		)
		return nil, false
	}
	return embedding, true
}

func (a *Activities) searchMemoryWithQueryEmbedding(ctx context.Context, projectID string, category domain.MemoryCategory, query string, limit int, queryEmbedding []float32, hasQueryEmbedding bool) ([]domain.MemoryFact, error) {
	if hasQueryEmbedding {
		facts, err := a.Store.SearchByCategorySimilar(ctx, projectID, category, queryEmbedding, limit)
		if err != nil {
			a.warn("semantic memory search failed; falling back to text search",
				slog.String("project_id", projectID),
				slog.String("category", string(category)),
				slog.String("error", err.Error()),
			)
			return a.Store.SearchByCategory(ctx, projectID, category, query, limit)
		}
		if len(facts) > 0 {
			return facts, nil
		}
	}
	return a.Store.SearchByCategory(ctx, projectID, category, query, limit)
}

func (a *Activities) warn(message string, attrs ...slog.Attr) {
	if a.Logger == nil {
		return
	}
	args := make([]any, 0, len(attrs))
	for _, attr := range attrs {
		args = append(args, attr)
	}
	a.Logger.Warn(message, args...)
}

func (a *Activities) PersistEvent(ctx context.Context, event domain.Event) error {
	return a.Store.Append(ctx, event)
}

func (a *Activities) Classify(ctx context.Context, event domain.Event) (agent.Classification, error) {
	if a.Engine == nil {
		return agent.Classification{}, fmt.Errorf("decision engine is not configured")
	}
	input, err := a.loadDecisionInput(ctx, event)
	if err != nil {
		return agent.Classification{}, err
	}
	return a.Engine.Classify(ctx, input)
}

func (a *Activities) Clarify(ctx context.Context, event domain.Event, classification agent.Classification) (agent.DecisionOutput, error) {
	if a.Engine == nil {
		return agent.DecisionOutput{}, fmt.Errorf("decision engine is not configured")
	}
	input, err := a.loadDecisionInput(ctx, event)
	if err != nil {
		return agent.DecisionOutput{}, err
	}

	clarification, err := a.Engine.Clarify(ctx, agent.ClarificationInput{
		ProjectID:      input.ProjectID,
		Context:        input.Context,
		Classification: classification,
	})
	if err != nil {
		return agent.DecisionOutput{}, err
	}

	return a.buildClarificationDecision(input, classification, clarification)
}

func (a *Activities) buildClarificationDecision(input agent.DecisionInput, classification agent.Classification, clarification *agent.ClarificationRequest) (agent.DecisionOutput, error) {

	now := time.Now().UTC()
	planID, err := domain.NewID()
	if err != nil {
		return agent.DecisionOutput{}, err
	}

	if clarification == nil {
		return agent.DecisionOutput{}, fmt.Errorf("%w: clarification request is nil", agent.ErrInvalidClarification)
	}
	if strings.TrimSpace(clarification.Reason) == "" {
		return agent.DecisionOutput{}, fmt.Errorf("%w: clarification reason is empty", agent.ErrInvalidClarification)
	}

	return agent.DecisionOutput{
		Classification: classification,
		Clarification:  clarification,
		Plan: domain.Plan{
			ID:        planID,
			ProjectID: input.ProjectID,
			EventID:   input.Context.Event.ID,
			Status:    domain.PlanStatusBlocked,
			Decision:  domain.DecisionKindClarify,
			Summary:   clarification.Reason,
			CreatedAt: now,
			UpdatedAt: now,
		},
	}, nil
}

func (a *Activities) Plan(ctx context.Context, event domain.Event, classification agent.Classification) (agent.DecisionOutput, error) {
	if a.Engine == nil {
		return agent.DecisionOutput{}, fmt.Errorf("decision engine is not configured")
	}
	input, err := a.loadDecisionInput(ctx, event)
	if err != nil {
		return agent.DecisionOutput{}, err
	}
	output, err := a.Engine.Plan(ctx, agent.PlanningInput{
		ProjectID:         input.ProjectID,
		Context:           input.Context,
		Classification:    classification,
		AutonomyThreshold: a.AutonomyThreshold,
		AvailableSkills:   a.AvailableSkills,
	})
	if err != nil {
		return agent.DecisionOutput{}, err
	}

	now := time.Now().UTC()
	if output.Plan.ID == "" {
		output.Plan.ID, err = domain.NewID()
		if err != nil {
			return agent.DecisionOutput{}, err
		}
	}
	if output.Plan.ProjectID == "" {
		output.Plan.ProjectID = input.ProjectID
	}
	if output.Plan.EventID == "" {
		output.Plan.EventID = input.Context.Event.ID
	}
	if output.Plan.Status == "" {
		output.Plan.Status = domain.PlanStatusReady
	}
	if output.Plan.Decision == "" {
		output.Plan.Decision = classification.PlanDecision()
	}
	if strings.TrimSpace(output.Plan.Summary) == "" {
		return agent.DecisionOutput{}, fmt.Errorf("%w: plan summary is empty", agent.ErrInvalidPlanningOutput)
	}
	if output.Plan.CreatedAt.IsZero() {
		output.Plan.CreatedAt = now
	}
	if output.Plan.UpdatedAt.IsZero() {
		output.Plan.UpdatedAt = now
	}

	if len(output.WorkItems) == 0 {
		return agent.DecisionOutput{}, fmt.Errorf("%w: plan has no work items", agent.ErrInvalidPlanningOutput)
	}

	output.Plan.WorkItemIDs = output.Plan.WorkItemIDs[:0]
	for idx := range output.WorkItems {
		if output.WorkItems[idx].ID == "" {
			output.WorkItems[idx].ID, err = domain.NewID()
			if err != nil {
				return agent.DecisionOutput{}, err
			}
		}
		if output.WorkItems[idx].ProjectID == "" {
			output.WorkItems[idx].ProjectID = input.ProjectID
		}
		if output.WorkItems[idx].PlanID == "" {
			output.WorkItems[idx].PlanID = output.Plan.ID
		}
		if output.WorkItems[idx].Status == "" {
			output.WorkItems[idx].Status = domain.WorkItemStatusReady
		}
		if output.WorkItems[idx].RiskTier == domain.RiskTierObserve && classification.Tier > domain.RiskTierObserve {
			output.WorkItems[idx].RiskTier = classification.Tier
		}
		if output.WorkItems[idx].CreatedAt.IsZero() {
			output.WorkItems[idx].CreatedAt = now
		}
		if output.WorkItems[idx].UpdatedAt.IsZero() {
			output.WorkItems[idx].UpdatedAt = now
		}
		output.Plan.WorkItemIDs = append(output.Plan.WorkItemIDs, output.WorkItems[idx].ID)
	}

	return agent.DecisionOutput{
		Classification: classification,
		Plan:           output.Plan,
		WorkItems:      output.WorkItems,
	}, nil
}

func (a *Activities) PrepareReadyDecision(_ context.Context, event domain.Event, classification agent.Classification) (agent.DecisionOutput, error) {
	now := time.Now().UTC()
	planID, err := domain.NewID()
	if err != nil {
		return agent.DecisionOutput{}, err
	}
	workItemID, err := domain.NewID()
	if err != nil {
		return agent.DecisionOutput{}, err
	}

	title := "Execute the requested action"
	workItemTitle := "Execute requested action"
	if classification.RoutedTo == agent.ClassificationRouteAnswer {
		title = "Answer the incoming question"
		workItemTitle = "Answer question"
	}

	plan := domain.Plan{
		ID:          planID,
		ProjectID:   event.ProjectID,
		EventID:     event.ID,
		Status:      domain.PlanStatusReady,
		Decision:    classification.PlanDecision(),
		Summary:     classification.Summary,
		WorkItemIDs: []string{workItemID},
		Steps: []domain.PlanStep{{
			ID:          workItemID,
			Title:       title,
			Description: event.Body,
			ToolHint:    domain.ToolTypeShell,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if strings.TrimSpace(plan.Summary) == "" {
		plan.Summary = title
	}

	workItem := domain.WorkItem{
		ID:          workItemID,
		ProjectID:   event.ProjectID,
		PlanID:      planID,
		Title:       workItemTitle,
		Description: event.Body,
		Status:      domain.WorkItemStatusReady,
		RiskTier:    classification.Tier,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	return agent.DecisionOutput{
		Classification: classification,
		Plan:           plan,
		WorkItems:      []domain.WorkItem{workItem},
	}, nil
}

func (a *Activities) Ingest(ctx context.Context, event domain.Event, classification agent.Classification) (agent.DecisionOutput, error) {
	input, err := a.loadDecisionInput(ctx, event)
	if err != nil {
		return agent.DecisionOutput{}, err
	}
	memoryValue := strings.TrimSpace(input.Context.Event.Body)
	if memoryValue == "" {
		memoryValue = classification.Summary
	}
	if memoryValue != "" {
		if err := a.PersistConversationMemory(ctx, input.Context.Event, memoryValue); err != nil {
			return agent.DecisionOutput{}, err
		}
	}
	if classification.ContradictionRisk {
		if err := a.persistPendingContradiction(ctx, input, classification); err != nil {
			return agent.DecisionOutput{}, err
		}
	}
	return agent.DecisionOutput{Classification: classification}, nil
}

func (a *Activities) SelectTool(ctx context.Context, request ToolSelectionRequest) (ToolSelectionResult, error) {
	if a.Engine == nil {
		return ToolSelectionResult{}, fmt.Errorf("decision engine is not configured")
	}
	input, err := a.loadDecisionInput(ctx, request.Event)
	if err != nil {
		return ToolSelectionResult{}, err
	}
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		projectID = input.ProjectID
	}
	decision, err := a.Engine.DecideNextAction(ctx, agent.ToolSelectionInput{
		ProjectID:          projectID,
		Context:            input.Context,
		Classification:     request.Decision.Classification,
		Plan:               request.Decision.Plan,
		WorkItems:          request.Decision.WorkItems,
		CurrentWorkItemID:  request.CurrentWorkItemID,
		Runtime:            buildRuntimeContext(a.WorkspaceRoot),
		ExecutionCycle:     request.ExecutionCycle,
		LastObservation:    request.Feedback,
		ObservationHistory: request.ObservationHistory,
	})
	if err != nil {
		return ToolSelectionResult{}, err
	}
	if message := strings.TrimSpace(decision.ResponseMessage); message != "" {
		decision.ResponseMessage = message
		decision.ToolChoice = nil
	}
	return ToolSelectionResult{
		Action:             decision.Action,
		WorkItemID:         decision.WorkItemID,
		WorkItemStatus:     decision.WorkItemStatus,
		ObservationSummary: decision.ObservationSummary,
		ToolChoice:         decision.ToolChoice,
		ResponseMessage:    decision.ResponseMessage,
	}, nil
}

func (a *Activities) PersistDecision(ctx context.Context, decision agent.DecisionOutput) error {
	if decision.Plan.ID != "" {
		if err := a.Store.UpsertPlan(ctx, decision.Plan); err != nil {
			return err
		}
	}
	for _, item := range decision.WorkItems {
		if item.ID == "" {
			continue
		}
		if err := a.Store.UpsertWorkItem(ctx, item); err != nil {
			return err
		}
	}
	return nil
}

func (a *Activities) EvaluatePolicy(ctx context.Context, event domain.Event, choice agent.ToolChoice) (policy.Result, error) {
	return a.Policy.Evaluate(ctx, policy.Request{
		ProjectID:     event.ProjectID,
		Intent:        choice.Intent,
		ToolType:      choice.Type,
		Command:       choice.Command,
		Args:          choice.Args,
		WorkingDir:    choice.WorkingDir,
		WorkspaceRoot: a.WorkspaceRoot,
		Destructive:   choice.Destructive,
	})
}

func (a *Activities) CreateApprovalRequest(ctx context.Context, decision agent.DecisionOutput, result policy.Result) (domain.ApprovalRequest, error) {
	workItem, err := workItemForChoice(decision)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if workItem.ID == "" {
		return domain.ApprovalRequest{}, fmt.Errorf("no work item available for approval")
	}
	id, err := domain.NewID()
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	now := time.Now().UTC()
	approval := domain.ApprovalRequest{
		ID:              id,
		ProjectID:       decision.Plan.ProjectID,
		WorkItemID:      workItem.ID,
		Status:          domain.ApprovalStatusPending,
		RiskTier:        result.Tier,
		RequestedAction: decision.ToolChoice.Intent,
		Reason:          strings.Join(result.Reasons, "; "),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	return approval, a.Store.UpsertApproval(ctx, approval)
}

func (a *Activities) RevalidateApproval(ctx context.Context, projectID, approvalID string) (domain.ApprovalRequest, error) {
	approval, err := a.Store.GetByID(ctx, projectID, approvalID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if approval.Status != domain.ApprovalStatusApproved {
		return domain.ApprovalRequest{}, domain.ErrApprovalRequired
	}
	return approval, nil
}

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		return ExecuteToolResult{}, fmt.Errorf("project_id is required")
	}
	workItemID := strings.TrimSpace(request.WorkItemID)
	if workItemID == "" {
		return ExecuteToolResult{}, fmt.Errorf("work_item_id is required")
	}
	now := time.Now().UTC()
	executionID, err := domain.NewID()
	if err != nil {
		return ExecuteToolResult{}, err
	}
	invocationID, err := domain.NewID()
	if err != nil {
		return ExecuteToolResult{}, err
	}

	attempt := domain.ExecutionAttempt{
		ID:         executionID,
		ProjectID:  projectID,
		WorkItemID: workItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    executionCycle(request.ToolChoice.Metadata),
		Tool:       request.ToolChoice.Type,
		Summary:    request.ToolChoice.Intent,
		StartedAt:  now,
		Metadata: map[string]string{
			"execution_cycle": fmt.Sprintf("%d", executionCycle(request.ToolChoice.Metadata)),
		},
	}
	if err := a.Store.UpsertExecutionAttempt(ctx, attempt); err != nil {
		return ExecuteToolResult{}, err
	}

	result, err := a.Shell.Run(ctx, shell.Request{
		ProjectID:          projectID,
		Intent:             request.ToolChoice.Intent,
		Command:            request.ToolChoice.Command,
		Args:               request.ToolChoice.Args,
		WorkingDir:         request.ToolChoice.WorkingDir,
		WorkspaceRoot:      a.WorkspaceRoot,
		Timeout:            toolChoiceTimeout(request.ToolChoice),
		RiskTier:           request.RiskTier,
		FallbackCandidates: toolregistry.FallbackCandidates(request.ToolChoice.Type),
	})

	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	invocation := domain.ToolInvocation{
		ID:                 invocationID,
		ProjectID:          projectID,
		ExecutionAttemptID: executionID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: toolregistry.FallbackCandidates(request.ToolChoice.Type),
		RiskTier:           request.RiskTier,
		WorkingDirectory:   request.ToolChoice.WorkingDir,
		TimeoutSeconds:     int(toolChoiceTimeout(request.ToolChoice).Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		OutputSummary:      strings.TrimSpace(result.Stdout),
		ResultCode:         fmt.Sprintf("%d", result.ExitCode),
		CreatedAt:          now,
		CompletedAt:        &completedAt,
		Metadata: map[string]string{
			"shell_exit_status": fmt.Sprintf("%d", result.ExitCode),
			"started_at":        result.StartedAt.UTC().Format(time.RFC3339Nano),
			"completed_at":      result.CompletedAt.UTC().Format(time.RFC3339Nano),
		},
	}

	var errorMessage string
	if err != nil {
		attempt.Status = domain.ExecutionStatusFailed
		attempt.OutputSummary = summarizeObservation(result.Stdout, result.Stderr, err)
		invocation.ErrorDetails = err.Error()
		invocation.OutputSummary = attempt.OutputSummary
		errorMessage = err.Error()
	} else {
		attempt.Status = domain.ExecutionStatusSucceeded
		attempt.OutputSummary = summarizeObservation(result.Stdout, result.Stderr, nil)
		invocation.OutputSummary = attempt.OutputSummary
	}

	if persistErr := a.Store.UpsertExecutionAttempt(ctx, attempt); persistErr != nil {
		return ExecuteToolResult{}, persistErr
	}
	if persistErr := a.Store.UpsertToolInvocation(ctx, invocation); persistErr != nil {
		return ExecuteToolResult{}, persistErr
	}

	return ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       workItemID,
		Tool:             request.ToolChoice.Type,
		Status:           attempt.Status,
		RequestedAction:  request.ToolChoice.Intent,
		Command:          request.ToolChoice.Command,
		Args:             request.ToolChoice.Args,
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: request.ToolChoice.WorkingDir,
		ResultCode:       invocation.ResultCode,
	}, nil
}

func workItemForChoice(decision agent.DecisionOutput) (domain.WorkItem, error) {
	if len(decision.WorkItems) == 0 {
		return domain.WorkItem{}, fmt.Errorf("no work items to execute")
	}

	requestedID := strings.TrimSpace(decision.ToolChoice.Metadata["work_item_id"])
	if requestedID == "" {
		return decision.WorkItems[0], nil
	}
	for _, item := range decision.WorkItems {
		if item.ID == requestedID {
			return item, nil
		}
	}
	return domain.WorkItem{}, fmt.Errorf("work item %q not found for tool execution", requestedID)
}

func executionCycle(metadata map[string]string) int {
	if len(metadata) == 0 {
		return 1
	}
	value := strings.TrimSpace(metadata["execution_cycle"])
	if value == "" {
		return 1
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 1
	}
	return parsed
}

func summarizeObservation(stdout, stderr string, err error) string {
	if text := strings.TrimSpace(stdout); text != "" {
		return compactObservation(text, maxObservationSummaryLength)
	}
	if text := strings.TrimSpace(stderr); text != "" {
		return compactObservation(text, maxObservationSummaryLength)
	}
	if err != nil {
		return err.Error()
	}
	return "Execution completed."
}

func compactObservation(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if limit <= 0 || len(text) <= limit {
		return text
	}

	cut := limit
	for cut > 0 && text[cut-1] != '\n' && text[cut-1] != ' ' && text[cut-1] != '\t' {
		cut--
	}
	if cut < limit/2 {
		cut = limit
	}

	return strings.TrimSpace(text[:cut]) + "\n...[output truncated]"
}

func toolChoiceTimeout(choice agent.ToolChoice) time.Duration {
	if choice.TimeoutMs > 0 {
		return time.Duration(choice.TimeoutMs) * time.Millisecond
	}
	return 60 * time.Second
}

func buildRuntimeContext(workspaceRoot string) agent.RuntimeContext {
	shellPath := strings.TrimSpace(os.Getenv("SHELL"))
	return agent.RuntimeContext{
		OS:            goruntime.GOOS,
		Arch:          goruntime.GOARCH,
		Shell:         shellPath,
		Path:          os.Getenv("PATH"),
		WorkspaceRoot: workspaceRoot,
	}
}

func (a *Activities) PersistConversationMemory(ctx context.Context, event domain.Event, summary string) error {
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	speaker := strings.TrimSpace(event.ActorName)
	speakerKind := "user"
	if normalizeConversationMemoryText(summary) != normalizeConversationMemoryText(event.Body) {
		speaker = "opencto"
		speakerKind = "assistant"
	}
	if speaker == "" {
		speaker = speakerKind
	}
	fact := domain.MemoryFact{
		ID:        id,
		ProjectID: event.ProjectID,
		Category:  domain.MemoryCategoryConversation,
		Key:       event.ID,
		Value:     summary,
		Provenance: domain.Provenance{
			Source:     string(event.ChannelType),
			SourceID:   event.ChannelID,
			Actor:      speaker,
			ObservedAt: now,
		},
		Metadata: map[string]string{
			"speaker": speakerKind,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := a.Store.UpsertFact(ctx, fact); err != nil {
		return err
	}
	if a.MemoryEmbedder == nil {
		return nil
	}

	embeddings, err := a.MemoryEmbedder.EmbedDocuments(ctx, []string{summary})
	if err != nil {
		a.warn("conversation memory embedding failed; stored text memory without vector",
			slog.String("project_id", fact.ProjectID),
			slog.String("fact_id", fact.ID),
			slog.String("error", err.Error()),
		)
		return nil
	}
	if len(embeddings) != 1 {
		a.warn("conversation memory embedding returned unexpected result count; stored text memory without vector",
			slog.String("project_id", fact.ProjectID),
			slog.String("fact_id", fact.ID),
			slog.Int("embedding_count", len(embeddings)),
		)
		return nil
	}
	if err := a.Store.UpsertFactEmbedding(ctx, fact.ProjectID, fact.ID, fact.Category, a.EmbeddingModel, embeddings[0]); err != nil {
		a.warn("conversation memory embedding persistence failed; stored text memory without vector",
			slog.String("project_id", fact.ProjectID),
			slog.String("fact_id", fact.ID),
			slog.String("error", err.Error()),
		)
	}
	return nil
}

func normalizeConversationMemoryText(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func (a *Activities) persistPendingContradiction(ctx context.Context, input agent.DecisionInput, classification agent.Classification) error {
	id, err := domain.NewID()
	if err != nil {
		return err
	}
	now := time.Now().UTC()

	existingFact := ""
	if len(input.Context.ProjectFacts) > 0 {
		existingFact = strings.TrimSpace(input.Context.ProjectFacts[0].Value)
	}
	topic := strings.TrimSpace(classification.Summary)
	if topic == "" {
		topic = "Potential contradiction in inbound event"
	}

	return a.Store.UpsertContradiction(ctx, domain.PendingContradiction{
		ID:           id,
		ProjectID:    input.ProjectID,
		Status:       domain.ContradictionStatusOpen,
		Topic:        topic,
		ExistingFact: existingFact,
		IncomingFact: strings.TrimSpace(input.Context.Event.Body),
		Metadata: map[string]string{
			"classification_intent": string(classification.Intent),
			"routed_to":             string(classification.RoutedTo),
			"event_id":              input.Context.Event.ID,
			"channel_id":            input.Context.Event.ChannelID,
		},
		CreatedAt: now,
		UpdatedAt: now,
	})
}

func (a *Activities) WriteADR(ctx context.Context, projectID, title, summary string, details []string) (domain.ADR, error) {
	adr, err := a.ADRWriter.WriteSummary(ctx, projectID, title, summary, details)
	if err != nil {
		return domain.ADR{}, err
	}
	return adr, a.Store.AppendADR(ctx, adr)
}

func (a *Activities) ReportResult(ctx context.Context, event domain.Event, message string) error {
	if a.Reporter == nil {
		return nil
	}
	return a.Reporter.Report(ctx, event, message)
}

func (a *Activities) ResolveApproval(ctx context.Context, signal signals.ApprovalDecisionSignal) (domain.ApprovalRequest, error) {
	approval, err := a.Store.GetByID(ctx, signal.ProjectID, signal.ApprovalID)
	if err != nil {
		return domain.ApprovalRequest{}, err
	}
	if signal.Approved {
		approval.Status = domain.ApprovalStatusApproved
	} else {
		approval.Status = domain.ApprovalStatusRejected
	}
	approval.DecidedBy = signal.ActorName
	approval.UpdatedAt = signal.DecidedAt.UTC()
	approval.DecidedAt = &approval.UpdatedAt
	if err := a.Store.UpsertApproval(ctx, approval); err != nil {
		return domain.ApprovalRequest{}, err
	}
	return approval, nil
}

func (a *Activities) ResolveContradiction(ctx context.Context, signal signals.ContradictionResolutionSignal) error {
	now := time.Now().UTC()
	return a.Store.UpsertContradiction(ctx, domain.PendingContradiction{
		ID:         signal.ContradictionID,
		ProjectID:  signal.ProjectID,
		Status:     domain.ContradictionStatusResolved,
		Topic:      "resolved contradiction",
		Resolution: signal.Resolution,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}
