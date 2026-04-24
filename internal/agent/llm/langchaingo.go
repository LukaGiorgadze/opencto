package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type OpenAIEngine struct {
	reasoningModel   llms.Model
	reasoningModelID string
	fastModel        llms.Model
	fastModelID      string
	toolModel        llms.Model
}

func NewOpenAIEngine(apiKey, baseURL, reasoningModelID, fastModelID string) (*OpenAIEngine, error) {
	reasoningModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(reasoningModelID),
		openai.WithResponseFormat(openai.ResponseFormatJSON),
	)
	if err != nil {
		return nil, err
	}

	fastModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(fastModelID),
		openai.WithResponseFormat(openai.ResponseFormatJSON),
	)
	if err != nil {
		return nil, err
	}

	toolModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(reasoningModelID),
	)
	if err != nil {
		return nil, err
	}

	return &OpenAIEngine{
		reasoningModel:   reasoningModel,
		reasoningModelID: reasoningModelID,
		fastModel:        fastModel,
		fastModelID:      fastModelID,
		toolModel:        toolModel,
	}, nil
}

func (e *OpenAIEngine) Classify(ctx context.Context, input agent.DecisionInput) (agent.Classification, error) {
	prompt, err := renderClassificationPrompt(input)
	if err != nil {
		return agent.Classification{}, err
	}

	output, err := invokeJSON[agent.Classification](ctx, e.fastModel, prompt, nil)
	if err != nil {
		return agent.Classification{}, err
	}
	return normalizeClassification(input, output)
}

func (e *OpenAIEngine) Clarify(ctx context.Context, input agent.ClarificationInput) (*agent.ClarificationRequest, error) {
	prompt, err := renderClarificationPrompt(input)
	if err != nil {
		return nil, err
	}

	output, err := invokeJSON[clarificationLLMOutput](ctx, e.reasoningModel, prompt, nil)
	if err != nil {
		return nil, err
	}
	request, err := normalizeClarificationOutput(input, output)
	if err != nil {
		return nil, err
	}
	return &request, nil
}

func (e *OpenAIEngine) Plan(ctx context.Context, input agent.PlanningInput) (agent.PlanningOutput, error) {
	prompt, err := renderPlanningPrompt(input)
	if err != nil {
		return agent.PlanningOutput{}, err
	}

	output, err := invokeJSON[planningLLMOutput](ctx, e.reasoningModel, prompt, nil)
	if err != nil {
		return agent.PlanningOutput{}, err
	}
	return normalizePlanningOutput(input, output)
}

func (e *OpenAIEngine) SelectTool(ctx context.Context, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	output, err := e.selectToolWithRegisteredTools(ctx, input)
	if err != nil {
		return agent.ToolChoice{}, err
	}
	return normalizeToolChoice(output, input)
}

func invokeJSON[T any](ctx context.Context, model llms.Model, prompt string, payload any) (T, error) {
	var zero T

	promptText := prompt
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return zero, err
		}
		promptText += "\n\nInput JSON:\n" + string(body)
	}
	raw, err := llms.GenerateFromSinglePrompt(ctx, model, promptText)
	if err != nil {
		return zero, err
	}

	var output T
	if err := json.Unmarshal([]byte(extractJSON(raw)), &output); err != nil {
		return zero, err
	}
	return output, nil
}

type classificationPromptData struct {
	ProjectName          string
	ProjectID            string
	ProjectDescription   string
	RelevantConversation string
	RecentDecisions      string
	AuthorName           string
	ChannelHint          string
	ThreadContext        string
	MessageText          string
	Timestamp            string
}

type clarificationPromptData struct {
	ProjectName                 string
	ProjectID                   string
	ProjectState                string
	ProjectDescription          string
	KnownFacts                  string
	ActiveWorkItems             string
	OpenContradictions          string
	RelevantConversation        string
	AuthorName                  string
	ChannelHint                 string
	ThreadContext               string
	OriginalMessage             string
	ClassifierIntent            agent.ClassificationIntent
	ClassifierRoute             agent.ClassificationRoute
	ClassifierTier              domain.RiskTier
	ClassifierConfidence        float64
	ClassifierContradictionRisk bool
	ClassifierSummary           string
}

type planningPromptData struct {
	ProjectName          string
	ProjectID            string
	ProjectDescription   string
	AutonomyThreshold    int
	AuthorName           string
	OriginalMessage      string
	ClarificationSummary string
	ResolvedAnswers      string
	AvailableSkills      string
}

type clarificationLLMOutput struct {
	KnownSummary    string   `json:"known_summary"`
	BlockingGaps    []string `json:"blocking_gaps"`
	Assumptions     []string `json:"assumptions"`
	Questions       []string `json:"questions"`
	Message         string   `json:"message"`
	ConfidenceAfter float64  `json:"confidence_after"`
}

type planningLLMOutput struct {
	PlanSummary      string                `json:"plan_summary"`
	Assumptions      []string              `json:"assumptions"`
	Risks            []string              `json:"risks"`
	RequiresApproval bool                  `json:"requires_approval"`
	WorkItems        []planningLLMWorkItem `json:"work_items"`
	ExecutionOrder   [][]string            `json:"execution_order"`
	TestStrategy     string                `json:"test_strategy"`
}

type planningLLMWorkItem struct {
	Title              string   `json:"title"`
	Description        string   `json:"description"`
	AcceptanceCriteria []string `json:"acceptance_criteria"`
	Rollback           string   `json:"rollback"`
	Skills             []string `json:"skills"`
	ToolHint           string   `json:"tool_hint"`
	Tier               int      `json:"tier"`
	RequiresApproval   bool     `json:"requires_approval"`
	DependsOn          []string `json:"depends_on"`
	Complexity         string   `json:"complexity"`
}

func renderClassificationPrompt(input agent.DecisionInput) (string, error) {
	event := input.Context.Event
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := classificationPromptData{
		ProjectName:          projectName,
		ProjectID:            input.ProjectID,
		ProjectDescription:   strings.TrimSpace(input.Context.Project.Description),
		RelevantConversation: formatConversationMemory(input.Context.ConversationMemory),
		RecentDecisions:      formatRecentDecisions(input.Context.RecentDecisions),
		AuthorName:           firstNonEmpty(strings.TrimSpace(event.ActorName), strings.TrimSpace(event.Provenance.Actor), "unknown"),
		ChannelHint:          classificationChannelHint(event),
		ThreadContext:        classificationThreadContext(event),
		MessageText:          strings.TrimSpace(event.Body),
		Timestamp:            classificationTimestamp(event),
	}
	return prompts.Render("classify.tmpl", data)
}

func renderClarificationPrompt(input agent.ClarificationInput) (string, error) {
	event := input.Context.Event
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := clarificationPromptData{
		ProjectName:                 projectName,
		ProjectID:                   input.ProjectID,
		ProjectState:                formatProjectState(input.Context.ActiveWorkItems, input.Context.OpenContradictions),
		ProjectDescription:          strings.TrimSpace(input.Context.Project.Description),
		KnownFacts:                  formatKnownFacts(input.Context.ProjectFacts),
		ActiveWorkItems:             formatActiveWorkItems(input.Context.ActiveWorkItems),
		OpenContradictions:          formatOpenContradictions(input.Context.OpenContradictions),
		RelevantConversation:        formatConversationMemory(input.Context.ConversationMemory),
		AuthorName:                  firstNonEmpty(strings.TrimSpace(event.ActorName), strings.TrimSpace(event.Provenance.Actor), "unknown"),
		ChannelHint:                 classificationChannelHint(event),
		ThreadContext:               classificationThreadContext(event),
		OriginalMessage:             strings.TrimSpace(event.Body),
		ClassifierIntent:            input.Classification.Intent,
		ClassifierRoute:             input.Classification.RoutedTo,
		ClassifierTier:              input.Classification.Tier,
		ClassifierConfidence:        input.Classification.Confidence,
		ClassifierContradictionRisk: input.Classification.ContradictionRisk,
		ClassifierSummary:           strings.TrimSpace(input.Classification.Summary),
	}
	return prompts.Render("clarify.tmpl", data)
}

func renderPlanningPrompt(input agent.PlanningInput) (string, error) {
	event := input.Context.Event
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := planningPromptData{
		ProjectName:          projectName,
		ProjectID:            input.ProjectID,
		ProjectDescription:   strings.TrimSpace(input.Context.Project.Description),
		AutonomyThreshold:    clampRiskTierValue(input.AutonomyThreshold),
		AuthorName:           firstNonEmpty(strings.TrimSpace(event.ActorName), strings.TrimSpace(event.Provenance.Actor), "unknown"),
		OriginalMessage:      strings.TrimSpace(event.Body),
		ClarificationSummary: strings.TrimSpace(planningClarificationSummary(event)),
		ResolvedAnswers:      strings.TrimSpace(planningResolvedAnswers(event)),
		AvailableSkills:      formatAvailableSkills(input.AvailableSkills),
	}
	return prompts.Render("plan.tmpl", data)
}

func normalizeClarificationOutput(input agent.ClarificationInput, output clarificationLLMOutput) (agent.ClarificationRequest, error) {
	request := agent.ClarificationRequest{
		KnownSummary:    strings.TrimSpace(output.KnownSummary),
		BlockingGaps:    trimStringList(output.BlockingGaps, 3),
		Assumptions:     trimStringList(output.Assumptions, 3),
		Questions:       trimStringList(output.Questions, 3),
		Message:         strings.TrimSpace(output.Message),
		ConfidenceAfter: clampConfidence(output.ConfidenceAfter),
	}

	request.Reason = clarificationReason(request, input.Classification.Summary)
	if request.Reason == "" {
		return agent.ClarificationRequest{}, fmt.Errorf("%w: clarification response is missing a reason", agent.ErrInvalidClarification)
	}
	if request.Message == "" && len(request.Questions) == 0 {
		return agent.ClarificationRequest{}, fmt.Errorf("%w: clarification response must include a message or question", agent.ErrInvalidClarification)
	}
	return request, nil
}

func normalizePlanningOutput(input agent.PlanningInput, output planningLLMOutput) (agent.PlanningOutput, error) {
	now := time.Now().UTC()
	planID, err := domain.NewID()
	if err != nil {
		return agent.PlanningOutput{}, err
	}

	workItems := make([]domain.WorkItem, 0, len(output.WorkItems))
	steps := make([]domain.PlanStep, 0, len(output.WorkItems))
	workItemIDs := make([]string, 0, len(output.WorkItems))
	for idx, item := range output.WorkItems {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			return agent.PlanningOutput{}, fmt.Errorf("%w: work item %d is missing a title", agent.ErrInvalidPlanningOutput, idx+1)
		}
		description := strings.TrimSpace(item.Description)
		if description == "" {
			return agent.PlanningOutput{}, fmt.Errorf("%w: work item %q is missing a description", agent.ErrInvalidPlanningOutput, title)
		}

		workItemID, err := domain.NewID()
		if err != nil {
			return agent.PlanningOutput{}, err
		}
		tier := normalizePlanningTier(item.Tier, input.Classification.Tier)
		toolHint, err := parsePlanToolHint(item.ToolHint)
		if err != nil {
			return agent.PlanningOutput{}, err
		}
		complexity, err := parseComplexity(item.Complexity)
		if err != nil {
			return agent.PlanningOutput{}, err
		}
		acceptanceCriteria := trimStringList(item.AcceptanceCriteria, 8)
		if len(acceptanceCriteria) == 0 {
			return agent.PlanningOutput{}, fmt.Errorf("%w: work item %q is missing acceptance criteria", agent.ErrInvalidPlanningOutput, title)
		}
		rollback := strings.TrimSpace(item.Rollback)
		if tier >= domain.RiskTierConsequential && rollback == "" {
			return agent.PlanningOutput{}, fmt.Errorf("%w: work item %q requires rollback details", agent.ErrInvalidPlanningOutput, title)
		}
		requiresApproval := item.RequiresApproval || int(tier) > clampRiskTierValue(input.AutonomyThreshold)

		metadata := map[string]string{
			"acceptance_criteria_json": encodeJSONMetadata(acceptanceCriteria),
			"rollback":                 rollback,
			"skills_json":              encodeJSONMetadata(trimStringList(item.Skills, 8)),
			"depends_on_json":          encodeJSONMetadata(trimStringList(item.DependsOn, 8)),
			"requires_approval":        strconv.FormatBool(requiresApproval),
			"complexity":               complexity,
		}

		workItems = append(workItems, domain.WorkItem{
			ID:          workItemID,
			ProjectID:   input.ProjectID,
			PlanID:      planID,
			Title:       title,
			Description: description,
			Status:      domain.WorkItemStatusReady,
			RiskTier:    tier,
			Metadata:    metadata,
			CreatedAt:   now,
			UpdatedAt:   now,
		})
		steps = append(steps, domain.PlanStep{
			ID:          workItemID,
			Title:       title,
			Description: description,
			ToolHint:    toolHint,
			Metadata: map[string]string{
				"depends_on_json": encodeJSONMetadata(trimStringList(item.DependsOn, 8)),
				"complexity":      complexity,
			},
		})
		workItemIDs = append(workItemIDs, workItemID)
	}

	if len(workItems) == 0 {
		return agent.PlanningOutput{}, fmt.Errorf("%w: planning response did not include any work items", agent.ErrInvalidPlanningOutput)
	}

	summary := strings.TrimSpace(output.PlanSummary)
	if summary == "" {
		return agent.PlanningOutput{}, fmt.Errorf("%w: planning response is missing a summary", agent.ErrInvalidPlanningOutput)
	}
	requiresApproval := output.RequiresApproval || planningRequiresApproval(workItems, clampRiskTierValue(input.AutonomyThreshold))
	testStrategy := strings.TrimSpace(output.TestStrategy)
	if testStrategy == "" {
		return agent.PlanningOutput{}, fmt.Errorf("%w: planning response is missing a test strategy", agent.ErrInvalidPlanningOutput)
	}
	executionOrder, err := validateExecutionOrder(output.ExecutionOrder, workItems)
	if err != nil {
		return agent.PlanningOutput{}, err
	}

	plan := domain.Plan{
		ID:          planID,
		ProjectID:   input.ProjectID,
		EventID:     input.Context.Event.ID,
		Status:      domain.PlanStatusReady,
		Summary:     summary,
		Decision:    input.Classification.PlanDecision(),
		Steps:       steps,
		WorkItemIDs: workItemIDs,
		Metadata: map[string]string{
			"assumptions_json":        encodeJSONMetadata(trimStringList(output.Assumptions, 8)),
			"risks_json":              encodeJSONMetadata(trimStringList(output.Risks, 8)),
			"execution_order_json":    encodeJSONMetadata(executionOrder),
			"test_strategy":           testStrategy,
			"requires_approval":       strconv.FormatBool(requiresApproval),
			"clarification_summary":   firstNonEmpty(planningClarificationSummary(input.Context.Event), "none"),
			"resolved_answers":        firstNonEmpty(planningResolvedAnswers(input.Context.Event), "none"),
			"available_skills_json":   encodeJSONMetadata(input.AvailableSkills),
			"known_integrations_json": encodeJSONMetadata(integrationNames(input.Context.Integrations)),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	return agent.PlanningOutput{
		Plan:      plan,
		WorkItems: workItems,
	}, nil
}

func clarificationReason(request agent.ClarificationRequest, fallback string) string {
	parts := make([]string, 0, 2)
	if request.KnownSummary != "" {
		parts = append(parts, request.KnownSummary)
	}
	if len(request.BlockingGaps) > 0 {
		parts = append(parts, "Blocked by: "+strings.Join(request.BlockingGaps, "; "))
	}
	reason := strings.TrimSpace(strings.Join(parts, " "))
	if reason != "" {
		return reason
	}
	if strings.TrimSpace(fallback) != "" {
		return strings.TrimSpace(fallback)
	}
	return ""
}

func normalizeClassification(input agent.DecisionInput, classification agent.Classification) (agent.Classification, error) {
	classification.Summary = strings.TrimSpace(classification.Summary)
	classification.Confidence = clampConfidence(classification.Confidence)

	if !isValidClassificationIntent(classification.Intent) {
		return agent.Classification{}, fmt.Errorf("%w: unsupported intent %q", agent.ErrInvalidClassification, classification.Intent)
	}

	if !isValidRiskTier(classification.Tier) {
		return agent.Classification{}, fmt.Errorf("%w: unsupported tier %d", agent.ErrInvalidClassification, classification.Tier)
	}

	if classification.Intent == agent.ClassificationIntentQuestion ||
		classification.Intent == agent.ClassificationIntentContextUpdate ||
		classification.Intent == agent.ClassificationIntentApproval ||
		classification.Intent == agent.ClassificationIntentRejection ||
		classification.Intent == agent.ClassificationIntentNeutral ||
		classification.Intent == agent.ClassificationIntentAmbiguous {
		classification.Tier = domain.RiskTierObserve
	}

	if !isValidClassificationRoute(classification.RoutedTo) {
		return agent.Classification{}, fmt.Errorf("%w: unsupported route %q", agent.ErrInvalidClassification, classification.RoutedTo)
	}

	resumeBlockedWork := shouldResumeBlockedWork(input, classification)
	if resumeBlockedWork {
		classification.Intent = agent.ClassificationIntentActionRequest
		classification.RoutedTo = agent.ClassificationRoutePlan
		classification.NeedsClarification = false
		if classification.Tier == domain.RiskTierObserve {
			classification.Tier = domain.RiskTierSafeLocalChange
		}
	}

	if classification.NeedsClarification || (len(input.Context.OpenContradictions) > 0 && !resumeBlockedWork) || strings.TrimSpace(input.Context.Event.Body) == "" {
		classification.RoutedTo = agent.ClassificationRouteClarify
		classification.NeedsClarification = true
	}

	if len(input.Context.OpenContradictions) > 0 && !resumeBlockedWork {
		classification.NeedsClarification = true
	}

	if strings.TrimSpace(input.Context.Event.Body) == "" {
		classification.Intent = agent.ClassificationIntentAmbiguous
		classification.Tier = domain.RiskTierObserve
		classification.NeedsClarification = true
		classification.RoutedTo = agent.ClassificationRouteClarify
	}

	if classification.Summary == "" {
		return agent.Classification{}, fmt.Errorf("%w: classification is missing a summary", agent.ErrInvalidClassification)
	}
	return classification, nil
}

func shouldResumeBlockedWork(input agent.DecisionInput, classification agent.Classification) bool {
	if strings.TrimSpace(input.Context.Event.Body) == "" {
		return false
	}
	if !hasRecentClarificationContext(input.Context) {
		return false
	}

	switch classification.Intent {
	case agent.ClassificationIntentQuestion, agent.ClassificationIntentIncident:
		return false
	}

	if classification.Intent == agent.ClassificationIntentActionRequest && classification.RoutedTo == agent.ClassificationRoutePlan && !classification.NeedsClarification {
		return false
	}

	return looksLikeClarificationResponse(input.Context.Event.Body, classification.Intent)
}

func hasRecentClarificationContext(ctx agent.Context) bool {
	candidates := make([]string, 0, len(ctx.ConversationMemory)+len(ctx.RecentDecisions))
	for _, item := range ctx.ConversationMemory {
		candidates = append(candidates, item.Value)
	}
	for _, item := range ctx.RecentDecisions {
		candidates = append(candidates, item.Title+" "+item.Summary)
	}

	for _, candidate := range candidates {
		lower := strings.ToLower(compactWhitespace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "clarification") ||
			strings.Contains(lower, "to continue") ||
			strings.Contains(lower, "should i") ||
			strings.Contains(lower, "couldn't complete") ||
			strings.Contains(lower, "failed") ||
			strings.Contains(lower, "instead of") ||
			strings.Contains(lower, "blocked by") {
			return true
		}
		if strings.Contains(lower, "?") && containsAny(lower, "path", "folder", "target", "environment", "branch", "file", "project", "repo") {
			return true
		}
	}

	return false
}

func looksLikeClarificationResponse(body string, intent agent.ClassificationIntent) bool {
	lower := strings.ToLower(compactWhitespace(body))
	if lower == "" {
		return false
	}
	if isLikelyPathText(lower) {
		return true
	}

	if intent == agent.ClassificationIntentApproval {
		return true
	}
	if intent == agent.ClassificationIntentRejection {
		return strings.HasPrefix(lower, "actually") ||
			strings.Contains(lower, " instead ") ||
			strings.Contains(lower, " use ") ||
			strings.Contains(lower, " remove ") ||
			strings.Contains(lower, " change ") ||
			strings.Contains(lower, " path") ||
			strings.Contains(lower, " folder") ||
			strings.Contains(body, "`")
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
		strings.HasPrefix(lower, "deploy ") ||
		strings.HasPrefix(lower, "do ") ||
		strings.Contains(lower, " you should ") ||
		strings.Contains(lower, " should ") ||
		strings.Contains(lower, " instead ") ||
		strings.Contains(lower, " remove ") ||
		strings.Contains(lower, " use ") ||
		strings.Contains(lower, " path") ||
		strings.Contains(lower, " folder") ||
		strings.Contains(body, "`")
}

func isLikelyPathText(value string) bool {
	return strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, "~/") ||
		strings.HasPrefix(value, "./") ||
		strings.HasPrefix(value, "../") ||
		strings.Contains(value, ":\\")
}

func isValidClassificationIntent(intent agent.ClassificationIntent) bool {
	switch intent {
	case agent.ClassificationIntentActionRequest,
		agent.ClassificationIntentQuestion,
		agent.ClassificationIntentContextUpdate,
		agent.ClassificationIntentApproval,
		agent.ClassificationIntentRejection,
		agent.ClassificationIntentIncident,
		agent.ClassificationIntentNeutral,
		agent.ClassificationIntentAmbiguous:
		return true
	default:
		return false
	}
}

func isValidClassificationRoute(route agent.ClassificationRoute) bool {
	switch route {
	case agent.ClassificationRouteClarify,
		agent.ClassificationRoutePlan,
		agent.ClassificationRouteExecute,
		agent.ClassificationRouteAnswer,
		agent.ClassificationRouteIngest,
		agent.ClassificationRouteIgnore:
		return true
	default:
		return false
	}
}

func isValidRiskTier(tier domain.RiskTier) bool {
	switch tier {
	case domain.RiskTierObserve, domain.RiskTierSafeLocalChange, domain.RiskTierConsequential, domain.RiskTierOwnerApproval:
		return true
	default:
		return false
	}
}

func clampConfidence(value float64) float64 {
	switch {
	case value < 0:
		return 0
	case value > 1:
		return 1
	default:
		return value
	}
}

func classificationChannelHint(event domain.Event) string {
	if hint := strings.TrimSpace(event.Metadata["channel_hint"]); hint != "" {
		return hint
	}
	if event.ChannelID != "" {
		return fmt.Sprintf("%s:%s", event.ChannelType, event.ChannelID)
	}
	if event.ChannelType != "" {
		return string(event.ChannelType)
	}
	return "unknown"
}

func classificationThreadContext(event domain.Event) string {
	if value := strings.TrimSpace(event.Metadata["thread_context"]); value != "" {
		return value
	}
	if value := payloadString(event.Payload, "thread_context"); value != "" {
		return value
	}
	return ""
}

func classificationTimestamp(event domain.Event) string {
	if !event.CreatedAt.IsZero() {
		return event.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !event.Provenance.ObservedAt.IsZero() {
		return event.Provenance.ObservedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return ""
}

func formatProjectState(active []domain.WorkItem, contradictions []domain.PendingContradiction) string {
	if len(active) == 0 && len(contradictions) == 0 {
		return "idle"
	}
	parts := make([]string, 0, 2)
	if len(active) > 0 {
		parts = append(parts, fmt.Sprintf("%d active work item(s)", len(active)))
	}
	if len(contradictions) > 0 {
		parts = append(parts, fmt.Sprintf("%d open contradiction(s)", len(contradictions)))
	}
	return strings.Join(parts, ", ")
}

func formatActiveWorkItems(items []domain.WorkItem) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "untitled work item"
		}
		parts = append(parts, title+" ["+string(item.Status)+"]")
	}
	return strings.Join(parts, "; ")
}

func formatOpenContradictions(items []domain.PendingContradiction) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		topic := strings.TrimSpace(item.Topic)
		if topic == "" {
			topic = "unlabeled contradiction"
		}
		parts = append(parts, topic)
	}
	return strings.Join(parts, "; ")
}

func formatRecentDecisions(items []domain.ADR) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) > 3 {
		items = items[:3]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		summary := strings.TrimSpace(item.Summary)
		switch {
		case title != "" && summary != "":
			parts = append(parts, title+": "+summary)
		case title != "":
			parts = append(parts, title)
		case summary != "":
			parts = append(parts, summary)
		}
	}
	return strings.Join(parts, "; ")
}

func formatConversationMemory(items []domain.MemoryFact) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) > 3 {
		items = items[:3]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		value := compactWhitespace(item.Value)
		if value == "" {
			continue
		}
		if speaker := strings.TrimSpace(item.Metadata["speaker"]); speaker != "" {
			label := speaker
			if speaker == "user" && strings.TrimSpace(item.Provenance.Actor) != "" {
				label = strings.TrimSpace(item.Provenance.Actor)
			}
			if speaker == "assistant" {
				label = "assistant"
			}
			if label != "" && !strings.HasPrefix(strings.ToLower(value), strings.ToLower(label)+": ") {
				value = label + ": " + value
			}
		}
		if len(value) > 280 {
			value = strings.TrimSpace(value[:277]) + "..."
		}
		parts = append(parts, value)
	}
	return strings.Join(parts, "; ")
}

func formatKnownFacts(items []domain.MemoryFact) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) > 8 {
		items = items[:8]
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		key := strings.TrimSpace(item.Key)
		value := strings.TrimSpace(item.Value)
		switch {
		case key != "" && value != "":
			parts = append(parts, key+": "+value)
		case value != "":
			parts = append(parts, value)
		case key != "":
			parts = append(parts, key)
		}
	}
	return strings.Join(parts, "; ")
}

func formatKnownIntegrations(items []domain.Integration) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		kind := strings.TrimSpace(item.Kind)
		if kind == "" {
			kind = "unknown"
		}
		entry := kind + " [" + string(item.Status) + "]"
		if ref := strings.TrimSpace(item.ExternalRef); ref != "" {
			entry += ": " + ref
		}
		parts = append(parts, entry)
	}
	return strings.Join(parts, "; ")
}

func formatAvailableSkills(skills []string) string {
	values := trimStringList(skills, 32)
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func planningClarificationSummary(event domain.Event) string {
	if value := strings.TrimSpace(event.Metadata["clarification_summary"]); value != "" {
		return value
	}
	if value := payloadString(event.Payload, "clarification_summary"); value != "" {
		return value
	}
	return ""
}

func planningResolvedAnswers(event domain.Event) string {
	if value := strings.TrimSpace(event.Metadata["resolved_answers"]); value != "" {
		return value
	}
	if value := payloadString(event.Payload, "resolved_answers"); value != "" {
		return value
	}
	return ""
}

func clampRiskTierValue(value int) int {
	switch {
	case value < int(domain.RiskTierObserve):
		return int(domain.RiskTierObserve)
	case value > int(domain.RiskTierOwnerApproval):
		return int(domain.RiskTierOwnerApproval)
	default:
		return value
	}
}

func normalizePlanningTier(value int, fallback domain.RiskTier) domain.RiskTier {
	value = clampRiskTierValue(value)
	switch domain.RiskTier(value) {
	case domain.RiskTierObserve, domain.RiskTierSafeLocalChange, domain.RiskTierConsequential, domain.RiskTierOwnerApproval:
		return domain.RiskTier(value)
	default:
		return fallback
	}
}

func parsePlanToolHint(value string) (domain.ToolType, error) {
	toolType, ok := toolregistry.ParseToolType(value)
	if !ok {
		return "", fmt.Errorf("%w: unsupported tool hint %q", agent.ErrInvalidPlanningOutput, value)
	}
	return toolType, nil
}

func parseComplexity(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "S", "M", "L", "XL":
		return strings.ToUpper(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("%w: unsupported complexity %q", agent.ErrInvalidPlanningOutput, value)
	}
}

func planningRequiresApproval(items []domain.WorkItem, autonomyThreshold int) bool {
	for _, item := range items {
		if item.RiskTier > domain.RiskTier(autonomyThreshold) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(item.Metadata["requires_approval"]), "true") {
			return true
		}
	}
	return false
}

func validateExecutionOrder(groups [][]string, items []domain.WorkItem) ([][]string, error) {
	if len(groups) == 0 {
		return nil, fmt.Errorf("%w: planning response is missing execution order", agent.ErrInvalidPlanningOutput)
	}

	validTitles := make(map[string]struct{}, len(items))
	for _, item := range items {
		validTitles[item.Title] = struct{}{}
	}

	normalized := make([][]string, 0, len(groups))
	seenTitles := make(map[string]struct{}, len(items))
	for _, group := range groups {
		filtered := trimStringList(group, len(group))
		if len(filtered) == 0 {
			return nil, fmt.Errorf("%w: execution order contains an empty group", agent.ErrInvalidPlanningOutput)
		}
		for _, title := range filtered {
			if _, ok := validTitles[title]; !ok {
				return nil, fmt.Errorf("%w: execution order references unknown work item %q", agent.ErrInvalidPlanningOutput, title)
			}
			if _, duplicated := seenTitles[title]; duplicated {
				return nil, fmt.Errorf("%w: execution order references %q more than once", agent.ErrInvalidPlanningOutput, title)
			}
			seenTitles[title] = struct{}{}
		}
		normalized = append(normalized, filtered)
	}
	if len(seenTitles) != len(validTitles) {
		for _, item := range items {
			if _, ok := seenTitles[item.Title]; !ok {
				return nil, fmt.Errorf("%w: execution order is missing work item %q", agent.ErrInvalidPlanningOutput, item.Title)
			}
		}
	}
	return normalized, nil
}

func integrationNames(items []domain.Integration) []string {
	values := make([]string, 0, len(items))
	for _, item := range items {
		kind := strings.TrimSpace(item.Kind)
		if kind == "" {
			continue
		}
		values = append(values, kind)
	}
	return values
}

func encodeJSONMetadata(value any) string {
	body, err := json.Marshal(value)
	if err != nil {
		return "[]"
	}
	return string(body)
}

func decodeJSONMetadataList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var decoded []string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil
	}
	return trimStringList(decoded, len(decoded))
}

func decodeJSONMetadataMatrix(value string) [][]string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var decoded [][]string
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		return nil
	}
	normalized := make([][]string, 0, len(decoded))
	for _, group := range decoded {
		group = trimStringList(group, len(group))
		if len(group) > 0 {
			normalized = append(normalized, group)
		}
	}
	return normalized
}

func payloadString(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeToolChoice(output agent.ToolChoice, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	output.Intent = strings.TrimSpace(output.Intent)
	output.Command = strings.TrimSpace(output.Command)
	output.WorkingDir = strings.TrimSpace(output.WorkingDir)
	output.InputSummary = strings.TrimSpace(output.InputSummary)
	output.ResponseMessage = strings.TrimSpace(output.ResponseMessage)

	if output.ResponseMessage != "" {
		if output.InputSummary == "" {
			output.InputSummary = strings.TrimSpace(input.Context.Event.Body)
		}
		if output.Intent == "" {
			output.Intent = output.ResponseMessage
		}
		output.Type = ""
		output.Command = ""
		output.Args = nil
		output.TimeoutMs = 0
		output.Destructive = false
		return output, nil
	}

	if output.Type != domain.ToolTypeShell {
		return agent.ToolChoice{}, fmt.Errorf("%w: shell execution requires the %s tool, got %q", agent.ErrInvalidToolChoice, toolregistry.SelectorToolShellName, output.Type)
	}
	if output.Command == "" {
		return agent.ToolChoice{}, fmt.Errorf("%w: shell tool response is missing a command", agent.ErrInvalidToolChoice)
	}
	if output.Intent == "" {
		return agent.ToolChoice{}, fmt.Errorf("%w: shell tool response is missing an intent", agent.ErrInvalidToolChoice)
	}
	if output.InputSummary == "" {
		output.InputSummary = strings.TrimSpace(input.Context.Event.Body)
	}
	if output.WorkingDir == "" && strings.TrimSpace(input.Runtime.WorkspaceRoot) != "" {
		output.WorkingDir = input.Runtime.WorkspaceRoot
	}
	output.TimeoutMs = clampToolTimeoutMs(output.TimeoutMs)
	return output, nil
}

func trimStringList(values []string, max int) []string {
	if max <= 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		trimmed = append(trimmed, value)
		if len(trimmed) == max {
			break
		}
	}
	return trimmed
}

func extractJSON(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}
