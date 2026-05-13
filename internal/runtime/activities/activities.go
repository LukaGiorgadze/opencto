package activities

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	osexec "os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
	"github.com/opencto/opencto/internal/runtime/scheduled"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/storage"
	"github.com/opencto/opencto/internal/textclean"
	toolregistry "github.com/opencto/opencto/internal/tools"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	writetool "github.com/opencto/opencto/internal/tools/write"
	"github.com/opencto/opencto/internal/workflowbundle"
	"github.com/opencto/opencto/internal/workspace"
)

type Reporter interface {
	Report(context.Context, domain.Event, domain.ReportMessage) ([]domain.ReportReceipt, error)
}

type EventEnqueuer interface {
	EnqueueEvent(context.Context, domain.Event) error
}

type TypingReporter interface {
	NotifyTyping(context.Context, domain.Event) error
}

type Activities struct {
	Store                       storage.RuntimeStore
	Engine                      agent.Engine
	Exec                        exectool.Executor
	Edit                        edittool.Executor
	Glob                        globtool.Executor
	Grep                        greptool.Executor
	Read                        readtool.Executor
	Schedule                    scheduletool.Executor
	Skill                       skilltool.Executor
	Write                       writetool.Executor
	Reporter                    Reporter
	EventEnqueuer               EventEnqueuer
	MemoryEmbedder              embedding.Embedder
	ConversationCompressor      agent.ConversationCompressor
	Project                     domain.Project
	WorkspaceRoot               string
	OpenCTORoot                 string
	SkillsRoot                  string
	StateDir                    string
	MemoryEnabled               bool
	MemoryLimit                 int
	ConversationEnabled         bool
	ConversationLimit           int
	ConversationMaxContextChars int
	ConversationSummaryEnabled  bool
	ConversationSummaryTrigger  int
	ConversationSummaryMaxChars int
	ConversationSummaryRecent   int
	ExecTailBytes               int64
	ExecGrace                   time.Duration
	HeartbeatGap                time.Duration
	Logger                      *slog.Logger
}

type NextActionRequest struct {
	ProjectID          string                    `json:"project_id"`
	Event              domain.Event              `json:"event"`
	AdditionalEvents   []domain.Event            `json:"additional_events,omitempty"`
	NextAction         agent.NextAction          `json:"next_action"`
	LastResult         *ExecuteToolResult        `json:"last_result,omitempty"`
	LastResults        []ExecuteToolResult       `json:"last_results,omitempty"`
	ObservationHistory []agent.ExecutionFeedback `json:"observation_history,omitempty"`
	Processes          []domain.ProcessReference `json:"processes,omitempty"`
	ExecutionCycle     int                       `json:"execution_cycle"`
	ForceFinal         bool                      `json:"force_final,omitempty"`
	ResumedFromPause   bool                      `json:"resumed_from_pause,omitempty"`
	Completion         *TaskCompletionRequest    `json:"completion,omitempty"`
}

type NextActionResult struct {
	NextAction   agent.NextAction          `json:"next_action"`
	ToolChoice   *agent.ToolChoice         `json:"tool_choice,omitempty"`
	ToolChoices  []agent.ToolChoice        `json:"tool_choices,omitempty"`
	WorkItemID   string                    `json:"work_item_id,omitempty"`
	Observation  *agent.ExecutionFeedback  `json:"observation,omitempty"`
	Observations []agent.ExecutionFeedback `json:"observations,omitempty"`
	Status       string                    `json:"status"`
	Processes    []domain.ProcessReference `json:"processes,omitempty"`
}

type ExecuteToolRequest struct {
	ProjectID  string           `json:"project_id"`
	WorkItemID string           `json:"work_item_id"`
	Event      domain.Event     `json:"event"`
	ToolChoice agent.ToolChoice `json:"tool_choice"`
}

type TaskCompletionRequest struct {
	Status    string                    `json:"status"`
	Processes []domain.ProcessReference `json:"processes,omitempty"`
}

type ReportResponseRequest struct {
	Event       domain.Event              `json:"event"`
	Message     string                    `json:"message"`
	Attachments []domain.ReportAttachment `json:"attachments,omitempty"`
	ReplyTo     *domain.ReportReply       `json:"reply_to,omitempty"`
}

type ReportResponseResult struct {
	Receipts []domain.ReportReceipt `json:"receipts,omitempty"`
}

type ResponseSessionRequest struct {
	ProjectID              string       `json:"project_id"`
	Event                  domain.Event `json:"event"`
	RefreshIntervalSeconds int          `json:"refresh_interval_seconds,omitempty"`
	MaxDurationSeconds     int          `json:"max_duration_seconds,omitempty"`
}

type PersistEventRequest struct {
	Event domain.Event `json:"event"`
}

type CompressConversationRequest struct {
	Event domain.Event `json:"event"`
}

type CompressConversationResult struct {
	Summarized   bool   `json:"summarized"`
	SummaryID    string `json:"summary_id,omitempty"`
	Scope        string `json:"scope,omitempty"`
	MessageCount int    `json:"message_count,omitempty"`
	SourceChars  int    `json:"source_chars,omitempty"`
}

type PersistNextActionRequest struct {
	Event      domain.Event     `json:"event"`
	NextAction agent.NextAction `json:"next_action"`
	Status     string           `json:"status,omitempty"`
}

type PersistToolResultRequest struct {
	Event  domain.Event      `json:"event"`
	Result ExecuteToolResult `json:"result"`
}

type ExecuteToolResult struct {
	Cycle            int                       `json:"cycle"`
	WorkItemID       string                    `json:"work_item_id,omitempty"`
	ToolCallID       string                    `json:"tool_call_id,omitempty"`
	Tool             domain.ToolType           `json:"tool,omitempty"`
	Status           domain.ExecutionStatus    `json:"status"`
	RequestedAction  string                    `json:"requested_action,omitempty"`
	Command          string                    `json:"command,omitempty"`
	Args             []string                  `json:"args,omitempty"`
	Input            json.RawMessage           `json:"input,omitempty"`
	Observation      string                    `json:"observation,omitempty"`
	Error            string                    `json:"error,omitempty"`
	WorkingDirectory string                    `json:"working_directory,omitempty"`
	ResultCode       string                    `json:"result_code,omitempty"`
	Metadata         map[string]string         `json:"metadata,omitempty"`
	Processes        []domain.ProcessReference `json:"processes,omitempty"`
	ExecutionAttempt domain.ExecutionAttempt   `json:"execution_attempt,omitempty"`
	ToolInvocation   domain.ToolInvocation     `json:"tool_invocation,omitempty"`
}

const (
	NextActionStatusTool      = "tool"
	NextActionStatusCompleted = "completed"
	NextActionStatusBlocked   = "blocked"
	NextActionStatusFailed    = "failed"
	NextActionStatusIgnored   = "ignored"

	defaultResponseSessionRefresh   = 4 * time.Second
	defaultResponseSessionTimeout   = 3 * time.Second
	defaultResponseSessionMaxAge    = 30 * time.Minute
	defaultToolHeartbeatGap         = 2 * time.Second
	defaultExecGrace                = 2 * time.Minute
	defaultExecTailBytes            = 16 << 10
	memoryEmbeddingQueryMaxChars    = 1600
	memoryEmbeddingQueryMaxMessages = 6
)

func (r NextActionResult) IsTerminal() bool {
	return r.Status != NextActionStatusTool
}

type toolExecutionContext struct {
	ProjectID          string
	WorkItemID         string
	ToolCallID         string
	SourceEvent        domain.Event
	Cycle              int
	StartedAt          time.Time
	ExecutionAttemptID string
	InvocationID       string
	Timeout            time.Duration
	FallbackCandidates []domain.ToolType
}

type toolRunResult struct {
	Observation      string
	ResultCode       string
	Input            json.RawMessage
	WorkingDirectory string
	Metadata         map[string]string
	Processes        []domain.ProcessReference
}

func (a *Activities) LoadContext(ctx context.Context, event domain.Event) (agent.Context, error) {
	return a.loadContext(ctx, event, event, nil)
}

func (a *Activities) loadContext(ctx context.Context, event domain.Event, conversationEvent domain.Event, additionalEvents []domain.Event) (agent.Context, error) {
	var activeWorkItems []domain.WorkItem
	if a.Store != nil {
		var err error
		activeWorkItems, err = a.Store.ListPendingWorkItems(ctx, event.ProjectID)
		if err != nil {
			return agent.Context{}, err
		}
	}
	contextEvent := inferDiscordThreadContext(conversationEvent)
	conversation, conversationSummaries, err := a.loadConversationContext(ctx, contextEvent)
	if err != nil {
		return agent.Context{}, err
	}
	var memories []domain.Memory
	if a.Store != nil && a.MemoryEnabled {
		query := strings.TrimSpace(firstNonEmpty(contextEvent.Body, event.Body))
		embeddingQuery := prepareMemoryEmbeddingQuery(query, event, additionalEvents, conversation)
		memories, err = a.searchMemoriesWithEmbeddingQuery(ctx, domain.MemorySearchRequest{
			ProjectID:      strings.TrimSpace(contextEvent.ProjectID),
			UserID:         eventUserID(contextEvent),
			ChannelType:    contextEvent.ChannelType,
			ChannelID:      strings.TrimSpace(contextEvent.ChannelID),
			ThreadID:       strings.TrimSpace(contextEvent.ThreadID),
			Query:          query,
			Scopes:         autoContextMemoryScopes(contextEvent),
			Limit:          storage.DefaultAutoContextLimit(a.MemoryLimit),
			FallbackRecent: true,
		}, embeddingQuery)
		if err != nil {
			return agent.Context{}, err
		}
		memories = excludeMemoriesFromSource(memories, event.ID)
	}

	project := a.Project
	if strings.TrimSpace(project.ID) == "" {
		project.ID = event.ProjectID
	}
	availableSkills, err := skillcatalog.Discover(ctx, a.skillsRoots()...)
	if err != nil {
		return agent.Context{}, err
	}
	return agent.Context{
		Event:                       event,
		Project:                     project,
		ActiveWorkItems:             activeWorkItems,
		Memory:                      memories,
		Conversation:                conversation,
		ConversationSummaries:       conversationSummaries,
		ConversationMaxContextChars: storage.DefaultConversationMaxContextChars(a.ConversationMaxContextChars),
		Skills:                      availableSkills,
	}, nil
}

func autoContextMemoryScopes(event domain.Event) []domain.MemoryScope {
	if strings.TrimSpace(event.ThreadID) != "" {
		return []domain.MemoryScope{domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
	}
	if strings.TrimSpace(event.ChannelID) != "" {
		return []domain.MemoryScope{domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
	}
	return []domain.MemoryScope{domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}
}

func (a *Activities) loadConversationContext(ctx context.Context, event domain.Event) ([]domain.ConversationMessage, []domain.ConversationSummary, error) {
	if a.Store == nil || !a.ConversationEnabled {
		return nil, nil, nil
	}
	boundary, hasBoundary, err := a.conversationThreadBoundary(ctx, event)
	if err != nil {
		return nil, nil, err
	}
	var summaries []domain.ConversationSummary
	if a.ConversationSummaryEnabled {
		summaries, err = a.loadConversationSummaries(ctx, event, boundary, hasBoundary)
		if err != nil {
			return nil, nil, err
		}
	}
	conversation, err := a.loadConversationHistory(ctx, event, summaries, boundary, hasBoundary)
	if err != nil {
		return nil, nil, err
	}
	return conversation, summaries, nil
}

func prepareMemoryEmbeddingQuery(query string, currentEvent domain.Event, additionalEvents []domain.Event, conversation []domain.ConversationMessage) string {
	query = cleanMemoryEmbeddingQueryText(query)
	current := cleanMemoryEmbeddingQueryText(currentEvent.Body)
	if query == "" {
		query = current
	}

	var anchors []string
	if query != "" {
		anchors = append(anchors, "Search memory for: "+query)
	}
	if current != "" && current != query {
		anchors = append(anchors, "Current request: "+current)
	}
	for _, event := range additionalEvents {
		if followup := cleanMemoryEmbeddingQueryText(event.Body); followup != "" {
			anchors = append(anchors, "Follow-up: "+followup)
		}
	}
	if len(anchors) == 0 {
		return ""
	}
	return buildMemoryEmbeddingQuery(anchors, memoryEmbeddingQueryContextLines(currentEvent, additionalEvents, conversation))
}

func buildMemoryEmbeddingQuery(anchors []string, context []string) string {
	var builder memoryEmbeddingQueryBuilder
	for _, line := range anchors {
		if !builder.append(line) {
			return builder.String()
		}
	}
	if len(context) == 0 || !builder.append("Recent context:") {
		return builder.String()
	}
	contextStart := len(builder.lines)
	for _, line := range context {
		if !builder.append(line) {
			break
		}
	}
	if len(builder.lines) == contextStart {
		builder.lines = builder.lines[:contextStart-1]
	}
	return builder.String()
}

func memoryEmbeddingQueryContextLines(currentEvent domain.Event, additionalEvents []domain.Event, conversation []domain.ConversationMessage) []string {
	if len(conversation) == 0 {
		return nil
	}
	excludedEventIDs := map[string]bool{}
	excludedUserBodies := map[string]bool{}
	addExcludedMemoryQueryEvent(excludedEventIDs, excludedUserBodies, currentEvent)
	for _, event := range additionalEvents {
		addExcludedMemoryQueryEvent(excludedEventIDs, excludedUserBodies, event)
	}

	selected := make([]domain.ConversationMessage, 0, memoryEmbeddingQueryMaxMessages)
	for i := len(conversation) - 1; i >= 0 && len(selected) < memoryEmbeddingQueryMaxMessages; i-- {
		message := conversation[i]
		label := memoryEmbeddingQueryRoleLabel(message.Role)
		if label == "" {
			continue
		}
		body := cleanMemoryEmbeddingQueryText(message.Body)
		if body == "" {
			continue
		}
		if message.Role == domain.ConversationRoleUser &&
			(excludedEventIDs[strings.TrimSpace(message.EventID)] || excludedUserBodies[body]) {
			continue
		}
		selected = append(selected, message)
	}

	lines := make([]string, 0, len(selected))
	for _, message := range selected {
		label := memoryEmbeddingQueryRoleLabel(message.Role)
		body := cleanMemoryEmbeddingQueryText(message.Body)
		if label == "" || body == "" {
			continue
		}
		lines = append(lines, label+": "+body)
	}
	return lines
}

func addExcludedMemoryQueryEvent(ids map[string]bool, bodies map[string]bool, event domain.Event) {
	if id := strings.TrimSpace(event.ID); id != "" {
		ids[id] = true
	}
	if body := cleanMemoryEmbeddingQueryText(event.Body); body != "" {
		bodies[body] = true
	}
}

func memoryEmbeddingQueryRoleLabel(role domain.ConversationRole) string {
	switch role {
	case domain.ConversationRoleUser:
		return "user"
	case domain.ConversationRoleAssistant:
		return "assistant"
	default:
		return ""
	}
}

func cleanMemoryEmbeddingQueryText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

type memoryEmbeddingQueryBuilder struct {
	lines []string
	chars int
}

func (b *memoryEmbeddingQueryBuilder) append(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	remaining := memoryEmbeddingQueryMaxChars - b.chars
	if len(b.lines) > 0 {
		remaining--
	}
	if remaining <= 0 {
		return false
	}
	if memoryEmbeddingQueryTextLen(line) > remaining {
		line = truncateMemoryEmbeddingQueryText(line, remaining)
		if line == "" {
			return false
		}
	}
	if len(b.lines) > 0 {
		b.chars++
	}
	b.lines = append(b.lines, line)
	b.chars += memoryEmbeddingQueryTextLen(line)
	return true
}

func (b memoryEmbeddingQueryBuilder) String() string {
	return strings.TrimSpace(strings.Join(b.lines, "\n"))
}

func memoryEmbeddingQueryTextLen(value string) int {
	return len([]rune(value))
}

func truncateMemoryEmbeddingQueryText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max]))
}

type conversationBoundary struct {
	CreatedAt      time.Time
	MessageID      string
	RootMessage    domain.ConversationMessage
	HasRootMessage bool
}

func (a *Activities) conversationThreadBoundary(ctx context.Context, event domain.Event) (conversationBoundary, bool, error) {
	threadID := strings.TrimSpace(event.ThreadID)
	if threadID == "" {
		return conversationBoundary{}, false, nil
	}
	thread := domain.ConversationThread{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    threadID,
	}
	stored, ok, err := a.Store.GetConversationThread(ctx, storage.ConversationThreadQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    threadID,
	})
	if err != nil {
		return conversationBoundary{}, false, err
	}
	if ok {
		thread = stored
	}
	if root, ok, err := a.conversationThreadRootMessage(ctx, event, thread); err != nil {
		return conversationBoundary{}, false, err
	} else if ok {
		boundary := conversationBoundary{
			CreatedAt:      firstNonZeroTime(root.CreatedAt, thread.CreatedAt, event.CreatedAt),
			MessageID:      strings.TrimSpace(root.ID),
			RootMessage:    root,
			HasRootMessage: true,
		}
		return boundary, true, nil
	}
	if !thread.CreatedAt.IsZero() {
		return conversationBoundary{CreatedAt: thread.CreatedAt}, true, nil
	}
	return conversationBoundary{}, false, nil
}

func (a *Activities) conversationThreadRootMessage(ctx context.Context, event domain.Event, thread domain.ConversationThread) (domain.ConversationMessage, bool, error) {
	messageID := strings.TrimSpace(thread.RootMessageID)
	if messageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
		messageID = strings.TrimSpace(event.ThreadID)
	}
	if messageID == "" {
		return domain.ConversationMessage{}, false, nil
	}
	return a.Store.GetConversationRootMessage(ctx, storage.ConversationRootMessageQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		MessageID:   messageID,
	})
}

func (a *Activities) loadConversationHistory(ctx context.Context, event domain.Event, summaries []domain.ConversationSummary, boundary conversationBoundary, hasBoundary bool) ([]domain.ConversationMessage, error) {
	limit := storage.DefaultConversationHistoryLimit(a.ConversationLimit)
	if limit > 50 {
		limit = 50
	}
	base := storage.ConversationQuery{
		ProjectID:      strings.TrimSpace(event.ProjectID),
		ChannelType:    event.ChannelType,
		ChannelID:      strings.TrimSpace(event.ChannelID),
		ThreadID:       strings.TrimSpace(event.ThreadID),
		Roles:          conversationRoles(),
		Limit:          limit,
		ExcludeEventID: strings.TrimSpace(event.ID),
		ExcludeControl: true,
	}
	var messages []domain.ConversationMessage
	seen := map[string]bool{}
	appendMessage := func(message domain.ConversationMessage) {
		if strings.TrimSpace(message.ID) == "" || seen[message.ID] {
			return
		}
		seen[message.ID] = true
		messages = append(messages, message)
	}
	appendMessages := func(scope storage.ConversationScope, cutoff domain.ConversationSummary) error {
		if limit <= 0 {
			return nil
		}
		query := base
		query.Scope = scope
		query.Limit = limit
		query.AfterCreatedAt = cutoff.ToCreatedAt
		query.AfterID = cutoff.ToMessageID
		if hasBoundary && scope != storage.ConversationScopeThread {
			query.BeforeCreatedAt = boundary.CreatedAt
			query.BeforeID = boundary.MessageID
		}
		found, err := a.Store.ListConversationMessages(ctx, query)
		if err != nil {
			return err
		}
		for _, message := range found {
			appendMessage(message)
		}
		return nil
	}
	threadSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeThread)
	channelSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeChannel)
	projectSummary, _ := latestConversationSummary(summaries, domain.ConversationSummaryScopeProject)
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			if err := appendMessages(storage.ConversationScopeThread, threadSummary); err != nil {
				return nil, err
			}
			if err := appendMessages(storage.ConversationScopeChannel, channelSummary); err != nil {
				return nil, err
			}
			if boundary.HasRootMessage {
				appendMessage(boundary.RootMessage)
			}
		} else if err := appendMessages(storage.ConversationScopeChannel, channelSummary); err != nil {
			return nil, err
		}
	} else {
		if err := appendMessages(storage.ConversationScopeProject, projectSummary); err != nil {
			return nil, err
		}
	}
	sortConversationMessages(messages)
	return messages, nil
}

func latestConversationSummary(summaries []domain.ConversationSummary, scope domain.ConversationSummaryScope) (domain.ConversationSummary, bool) {
	var latest domain.ConversationSummary
	found := false
	for _, summary := range summaries {
		if summary.Scope != scope {
			continue
		}
		if !found || summary.ToCreatedAt.After(latest.ToCreatedAt) ||
			(summary.ToCreatedAt.Equal(latest.ToCreatedAt) && summary.ToMessageID > latest.ToMessageID) {
			latest = summary
			found = true
		}
	}
	return latest, found
}

func sortConversationMessages(messages []domain.ConversationMessage) {
	sort.SliceStable(messages, func(i, j int) bool {
		left := messages[i].CreatedAt
		right := messages[j].CreatedAt
		if left.Equal(right) {
			return messages[i].ID < messages[j].ID
		}
		return left.Before(right)
	})
}

func (a *Activities) loadConversationSummaries(ctx context.Context, event domain.Event, boundary conversationBoundary, hasBoundary bool) ([]domain.ConversationSummary, error) {
	base := storage.ConversationSummaryQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
	}
	type summaryScopeQuery struct {
		scope domain.ConversationSummaryScope
		limit int
	}
	var scopes []summaryScopeQuery
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			scopes = append(
				scopes,
				summaryScopeQuery{scope: domain.ConversationSummaryScopeThread, limit: 3},
				summaryScopeQuery{scope: domain.ConversationSummaryScopeChannel, limit: 1},
			)
		} else {
			scopes = append(
				scopes,
				summaryScopeQuery{scope: domain.ConversationSummaryScopeChannel, limit: 3},
			)
		}
	} else {
		scopes = append(scopes, summaryScopeQuery{scope: domain.ConversationSummaryScopeProject, limit: 3})
	}
	var summaries []domain.ConversationSummary
	seen := map[string]bool{}
	for _, item := range scopes {
		query := base
		query.Scope = item.scope
		query.Limit = item.limit
		if hasBoundary && item.scope != domain.ConversationSummaryScopeThread {
			query.BeforeCreatedAt = boundary.CreatedAt
			query.BeforeID = boundary.MessageID
		}
		found, err := a.Store.ListConversationSummaries(ctx, query)
		if err != nil {
			return nil, err
		}
		for _, summary := range found {
			if strings.TrimSpace(summary.ID) == "" || seen[summary.ID] {
				continue
			}
			seen[summary.ID] = true
			summaries = append(summaries, summary)
		}
	}
	return summaries, nil
}

func conversationUserMetadata(event domain.Event) domain.Metadata {
	metadata := domain.Metadata{
		"channel_type": string(event.ChannelType),
		"channel_id":   strings.TrimSpace(event.ChannelID),
		"thread_id":    strings.TrimSpace(event.ThreadID),
		"actor_id":     strings.TrimSpace(event.ActorID),
		"actor_name":   strings.TrimSpace(event.ActorName),
	}
	if control := strings.TrimSpace(event.Metadata[domain.MetadataKeyControl]); control != "" {
		metadata[domain.MetadataKeyControl] = control
	}
	return metadata
}

func eventUserID(event domain.Event) string {
	actorID := strings.TrimSpace(event.ActorID)
	if actorID != "" {
		channelType := strings.TrimSpace(string(event.ChannelType))
		if channelType != "" {
			return channelType + ":" + actorID
		}
		return actorID
	}
	return strings.TrimSpace(event.ActorName)
}

func memoryMetadata(event domain.Event, reason string) domain.Metadata {
	metadata := domain.Metadata{
		"event_id":     strings.TrimSpace(event.ID),
		"reason":       strings.TrimSpace(reason),
		"channel_type": string(event.ChannelType),
		"channel_id":   strings.TrimSpace(event.ChannelID),
		"thread_id":    strings.TrimSpace(event.ThreadID),
		"actor_id":     strings.TrimSpace(event.ActorID),
		"actor_name":   strings.TrimSpace(event.ActorName),
	}
	for key, value := range metadata {
		if strings.TrimSpace(value) == "" {
			delete(metadata, key)
		}
	}
	return metadata
}

func excludeMemoriesFromSource(memories []domain.Memory, sourceID string) []domain.Memory {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || len(memories) == 0 {
		return memories
	}
	filtered := memories[:0]
	for _, memory := range memories {
		if strings.TrimSpace(memory.SourceID) == sourceID {
			continue
		}
		filtered = append(filtered, memory)
	}
	return filtered
}

func (a *Activities) activityLogger() *slog.Logger {
	if a.Logger != nil {
		return a.Logger
	}
	return slog.Default()
}

func (a *Activities) logActivityStep(activity, step string, attrs ...any) {
	base := []any{
		slog.String("activity", activity),
		slog.String("step", step),
	}
	a.activityLogger().Info("runtime activity trace", append(base, attrs...)...)
}

func (a *Activities) ResponseSession(ctx context.Context, request ResponseSessionRequest) error {
	event := request.Event
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	a.logActivityStep(
		"ResponseSession", "start",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("channel_type", string(event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(event.ChannelID)),
	)
	reporter, ok := a.Reporter.(TypingReporter)
	if !ok || reporter == nil {
		a.logActivityStep(
			"ResponseSession", "skip_no_indicator_reporter",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		return nil
	}

	interval := defaultResponseSessionRefresh
	if request.RefreshIntervalSeconds > 0 {
		interval = time.Duration(request.RefreshIntervalSeconds) * time.Second
	}
	maxAge := defaultResponseSessionMaxAge
	if request.MaxDurationSeconds > 0 {
		maxAge = time.Duration(request.MaxDurationSeconds) * time.Second
	}

	heartbeatDetails := map[string]string{
		"project_id":   projectID,
		"event_id":     event.ID,
		"channel_type": string(event.ChannelType),
	}
	stopHeartbeat := startResponseSessionHeartbeat(ctx, defaultResponseSessionRefresh, heartbeatDetails)
	defer stopHeartbeat()

	refresh := func() {
		typingCtx, cancel := context.WithTimeout(ctx, defaultResponseSessionTimeout)
		defer cancel()
		if err := reporter.NotifyTyping(typingCtx, event); err != nil && ctx.Err() == nil {
			a.logActivityStep(
				"ResponseSession", "indicator_error",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
		}
	}

	refresh()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	deadline := time.NewTimer(maxAge)
	defer deadline.Stop()

	for {
		select {
		case <-ctx.Done():
			a.logActivityStep(
				"ResponseSession", "canceled",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
			)
			return nil
		case <-deadline.C:
			a.logActivityStep(
				"ResponseSession", "expired",
				slog.String("project_id", projectID),
				slog.String("event_id", event.ID),
				slog.Duration("max_age", maxAge),
			)
			return nil
		case <-ticker.C:
			refresh()
		}
	}
}

func (a *Activities) ReportResponse(ctx context.Context, request ReportResponseRequest) (ReportResponseResult, error) {
	report := domain.ReportMessage{
		Text:        strings.TrimSpace(request.Message),
		Attachments: append([]domain.ReportAttachment(nil), request.Attachments...),
		ReplyTo:     cleanReportReply(request.ReplyTo),
	}
	if report.Empty() || a.Reporter == nil {
		return ReportResponseResult{}, nil
	}
	a.logActivityStep(
		"ReportResponse", "start",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
		slog.String("channel_type", string(request.Event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(request.Event.ChannelID)),
	)
	receipts, err := a.Reporter.Report(ctx, request.Event, report)
	if err != nil {
		a.logActivityStep(
			"ReportResponse", "error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	if err := a.persistReportedConversationMessages(ctx, request.Event, report, receipts); err != nil {
		a.logActivityStep(
			"ReportResponse", "conversation_error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	if err := a.persistReportedConversationThreads(ctx, request.Event, receipts); err != nil {
		a.logActivityStep(
			"ReportResponse", "thread_error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	a.logActivityStep(
		"ReportResponse", "done",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
	)
	return ReportResponseResult{Receipts: receipts}, nil
}

func (a *Activities) persistReportedConversationMessages(ctx context.Context, event domain.Event, report domain.ReportMessage, receipts []domain.ReportReceipt) error {
	if a.Store == nil || strings.TrimSpace(report.Text) == "" {
		return nil
	}
	projectID := strings.TrimSpace(event.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	if projectID == "" {
		return nil
	}
	targets := reportedConversationTargets(event, receipts)
	for index, target := range targets {
		if !shouldPersistReportedConversationTarget(event, target) {
			continue
		}
		metadata := domain.Metadata{
			"source": "report_response",
		}
		if target.MessageID != "" {
			metadata["message_id"] = target.MessageID
		}
		message := domain.ConversationMessage{
			ID:          stableActivityID("conversation-assistant-report", projectID, event.ID, strconv.Itoa(index), target.MessageID, target.ChannelID, target.ThreadID, report.Text),
			ProjectID:   projectID,
			EventID:     event.ID,
			Role:        domain.ConversationRoleAssistant,
			ChannelType: event.ChannelType,
			ChannelID:   target.ChannelID,
			ThreadID:    target.ThreadID,
			Body:        strings.TrimSpace(report.Text),
			Metadata:    metadata,
			CreatedAt:   time.Now().UTC(),
		}
		if strings.TrimSpace(message.ChannelID) == "" {
			continue
		}
		if err := a.Store.UpsertConversationMessage(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

func shouldPersistReportedConversationTarget(event domain.Event, target reportedConversationTarget) bool {
	return strings.TrimSpace(target.ChannelID) != strings.TrimSpace(event.ChannelID) ||
		strings.TrimSpace(target.ThreadID) != strings.TrimSpace(event.ThreadID)
}

type reportedConversationTarget struct {
	MessageID string
	ChannelID string
	ThreadID  string
}

func reportedConversationTargets(event domain.Event, receipts []domain.ReportReceipt) []reportedConversationTarget {
	seen := map[string]bool{}
	var targets []reportedConversationTarget
	add := func(target reportedConversationTarget) {
		target.MessageID = strings.TrimSpace(target.MessageID)
		target.ChannelID = strings.TrimSpace(target.ChannelID)
		target.ThreadID = strings.TrimSpace(target.ThreadID)
		if target.ChannelID == "" {
			return
		}
		key := target.MessageID + "\x00" + target.ChannelID + "\x00" + target.ThreadID
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, target)
	}
	for _, receipt := range receipts {
		channelID := strings.TrimSpace(firstNonEmpty(receipt.ChannelID, event.ChannelID))
		threadID := strings.TrimSpace(receipt.ThreadID)
		messageID := strings.TrimSpace(receipt.MessageID)
		add(reportedConversationTarget{
			MessageID: messageID,
			ChannelID: channelID,
			ThreadID:  threadID,
		})
		if event.ChannelType == domain.ChannelTypeDiscord && threadID == "" && messageID != "" {
			add(reportedConversationTarget{
				MessageID: messageID,
				ChannelID: channelID,
				ThreadID:  messageID,
			})
		}
	}
	if len(receipts) == 0 {
		add(reportedConversationTarget{
			ChannelID: strings.TrimSpace(event.ChannelID),
			ThreadID:  strings.TrimSpace(event.ThreadID),
		})
	}
	return targets
}

func (a *Activities) persistReportedConversationThreads(ctx context.Context, event domain.Event, receipts []domain.ReportReceipt) error {
	if a.Store == nil {
		return nil
	}
	event = inferDiscordThreadContext(event)
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	for _, target := range reportedConversationTargets(event, receipts) {
		if strings.TrimSpace(target.ThreadID) == "" {
			continue
		}
		thread := domain.ConversationThread{
			ID:            stableActivityID("conversation-thread", event.ProjectID, string(event.ChannelType), target.ChannelID, target.ThreadID),
			ProjectID:     strings.TrimSpace(event.ProjectID),
			ChannelType:   event.ChannelType,
			ChannelID:     strings.TrimSpace(target.ChannelID),
			ThreadID:      strings.TrimSpace(target.ThreadID),
			RootMessageID: strings.TrimSpace(target.MessageID),
			EventID:       strings.TrimSpace(event.ID),
			Metadata: domain.Metadata{
				"source": "report_response",
			},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
			LastMessageAt: time.Now().UTC(),
		}
		if thread.RootMessageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
			thread.RootMessageID = thread.ThreadID
		}
		if err := a.persistConversationThread(ctx, thread); err != nil {
			return err
		}
	}
	return nil
}

func (a *Activities) persistConversationThread(ctx context.Context, thread domain.ConversationThread) error {
	if a.Store == nil {
		return nil
	}
	thread.ProjectID = strings.TrimSpace(thread.ProjectID)
	if thread.ProjectID == "" {
		thread.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	thread.ChannelID = strings.TrimSpace(thread.ChannelID)
	thread.ThreadID = strings.TrimSpace(thread.ThreadID)
	if thread.ProjectID == "" || thread.ChannelID == "" || thread.ThreadID == "" {
		return nil
	}
	if thread.ID == "" {
		thread.ID = stableActivityID("conversation-thread", thread.ProjectID, string(thread.ChannelType), thread.ChannelID, thread.ThreadID)
	}
	return a.Store.UpsertConversationThread(ctx, thread)
}

func conversationThreadFromEvent(event domain.Event) domain.ConversationThread {
	event = inferDiscordThreadContext(event)
	projectID := strings.TrimSpace(event.ProjectID)
	channelID := strings.TrimSpace(event.ChannelID)
	threadID := strings.TrimSpace(event.ThreadID)
	if projectID == "" || channelID == "" || threadID == "" {
		return domain.ConversationThread{}
	}
	createdAt := firstNonZeroTime(event.CreatedAt, time.Now().UTC())
	thread := domain.ConversationThread{
		ID:            stableActivityID("conversation-thread", projectID, string(event.ChannelType), channelID, threadID),
		ProjectID:     projectID,
		ChannelType:   event.ChannelType,
		ChannelID:     channelID,
		ThreadID:      threadID,
		RootMessageID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]),
		EventID:       strings.TrimSpace(event.ID),
		Title:         strings.TrimSpace(event.Body),
		Metadata: domain.Metadata{
			"source": "event",
		},
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		LastMessageAt: createdAt,
	}
	if thread.RootMessageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
		thread.RootMessageID = threadID
	}
	return thread
}

func cleanReportReply(reply *domain.ReportReply) *domain.ReportReply {
	if reply == nil {
		return nil
	}
	cleaned := domain.ReportReply{
		MessageID: strings.TrimSpace(reply.MessageID),
		ChannelID: strings.TrimSpace(reply.ChannelID),
		ContextID: strings.TrimSpace(reply.ContextID),
	}
	if cleaned.Empty() {
		return nil
	}
	return &cleaned
}

func (a *Activities) EnqueueScheduledEvent(ctx context.Context, request scheduled.EnqueueScheduledEventRequest) error {
	if a.EventEnqueuer == nil {
		return fmt.Errorf("event enqueuer is not configured")
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		return fmt.Errorf("project_id is required")
	}
	if strings.TrimSpace(event.ID) == "" {
		return fmt.Errorf("scheduled event id is required")
	}
	a.logActivityStep(
		"EnqueueScheduledEvent", "start",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("schedule_id", scheduled.ScheduleID(event)),
	)
	if err := a.EventEnqueuer.EnqueueEvent(ctx, event); err != nil {
		a.logActivityStep(
			"EnqueueScheduledEvent", "error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	a.logActivityStep(
		"EnqueueScheduledEvent", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
	)
	return nil
}

func (a *Activities) PrepareWorkflowRun(ctx context.Context, request workflowrun.PrepareRequest) (workflowrun.PrepareResult, error) {
	input := request.Input
	projectID := strings.TrimSpace(input.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	workflowID, err := workflowbundle.NormalizeWorkflowID(input.WorkflowID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	commitHash := strings.TrimSpace(input.CommitHash)
	if commitHash == "" {
		return workflowrun.PrepareResult{}, fmt.Errorf("commit_hash is required")
	}
	repoDir, err := workflowbundle.WorkflowDir(a.WorkspaceRoot, workflowID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	scheduledAt := input.ScheduledAt
	if scheduledAt.IsZero() {
		scheduledAt = time.Now().UTC()
	}
	runID := stableActivityID("scheduled-workflow-run", projectID, workflowID, request.TemporalWorkflowID, scheduledAt.UTC().Format(time.RFC3339Nano))
	runDir, err := workflowbundle.WorkflowRunDir(a.WorkspaceRoot, workflowID, runID)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := os.RemoveAll(runDir); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := os.MkdirAll(filepath.Join(runDir, "artifacts"), 0o755); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if err := workflowbundle.ArchiveCommit(ctx, repoDir, commitHash, runDir); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	manifest, err := workflowbundle.LoadManifest(runDir)
	if err != nil {
		return workflowrun.PrepareResult{}, err
	}
	startedAt := time.Now().UTC()
	if err := writeWorkflowRunState(runDir, initialWorkflowRunState(input, manifest, projectID, workflowID, runID, commitHash, request.TemporalWorkflowID, runDir, scheduledAt.UTC(), startedAt)); err != nil {
		return workflowrun.PrepareResult{}, err
	}
	if a.Store != nil {
		if err := a.Store.UpsertScheduledWorkflowRun(ctx, domain.ScheduledWorkflowRun{
			ID:                 runID,
			ProjectID:          projectID,
			WorkflowID:         workflowID,
			CommitHash:         commitHash,
			TemporalWorkflowID: strings.TrimSpace(request.TemporalWorkflowID),
			Status:             domain.ExecutionStatusRunning,
			ScheduledAt:        scheduledAt.UTC(),
			StartedAt:          startedAt,
			RunPath:            runDir,
			Metadata: domain.Metadata{
				"schedule_id": strings.TrimSpace(input.ScheduleID),
			},
		}); err != nil {
			return workflowrun.PrepareResult{}, err
		}
	}
	return workflowrun.PrepareResult{RunID: runID, RunPath: runDir, Manifest: manifest}, nil
}

func (a *Activities) ExecuteWorkflowStep(ctx context.Context, request workflowrun.ExecuteStepRequest) (workflowrun.ExecuteStepResult, error) {
	stepID, err := workflowbundle.NormalizeStepID(request.Step.ID)
	if err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	runPath := strings.TrimSpace(request.RunPath)
	if runPath == "" {
		return workflowrun.ExecuteStepResult{}, fmt.Errorf("run_path is required")
	}
	stepDir := filepath.Join(runPath, "steps", stepID)
	if err := os.MkdirAll(stepDir, 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	startedAt := time.Now().UTC()
	attempt := activityAttempt(ctx)
	stdoutPath, stderrPath := workflowStepAttemptLogPaths(stepDir, attempt)
	if err := os.MkdirAll(filepath.Dir(stdoutPath), 0o755); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	stepRunID := stableActivityID("scheduled-workflow-step", request.RunID, stepID, strconv.Itoa(attempt))
	if err := upsertWorkflowStepState(runPath, workflowRunStepState{
		ID:            stepID,
		Status:        string(domain.ExecutionStatusRunning),
		Attempt:       attempt,
		Command:       request.Step.Command,
		Args:          append([]string(nil), request.Step.Args...),
		StartedAt:     &startedAt,
		StdoutLogPath: stdoutPath,
		StderrLogPath: stderrPath,
	}); err != nil {
		return workflowrun.ExecuteStepResult{}, err
	}
	if a.Store != nil {
		_ = a.Store.UpsertScheduledWorkflowStepRun(ctx, domain.ScheduledWorkflowStepRun{
			ID:            stepRunID,
			ProjectID:     request.ProjectID,
			RunID:         request.RunID,
			WorkflowID:    request.WorkflowID,
			StepID:        stepID,
			Status:        domain.ExecutionStatusRunning,
			Attempt:       attempt,
			Command:       request.Step.Command,
			Args:          append([]string(nil), request.Step.Args...),
			StartedAt:     startedAt,
			StdoutLogPath: stdoutPath,
			StderrLogPath: stderrPath,
		})
	}

	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = failWorkflowStepState(runPath, stepID, attempt, request.Step, stdoutPath, stderrPath, startedAt, err)
		return workflowrun.ExecuteStepResult{}, err
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		_ = stdoutFile.Close()
		_ = failWorkflowStepState(runPath, stepID, attempt, request.Step, stdoutPath, stderrPath, startedAt, err)
		return workflowrun.ExecuteStepResult{}, err
	}

	env, err := workflowStepEnvironment(a.WorkspaceRoot, request.Env)
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		_ = failWorkflowStepState(runPath, stepID, attempt, request.Step, stdoutPath, stderrPath, startedAt, err)
		return workflowrun.ExecuteStepResult{}, err
	}
	cmd := osexec.CommandContext(ctx, request.Step.Command, request.Step.Args...)
	cmd.Dir = runPath
	cmd.Env = env
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	err = cmd.Run()
	stdoutCloseErr := stdoutFile.Close()
	stderrCloseErr := stderrFile.Close()
	if err == nil {
		switch {
		case stdoutCloseErr != nil:
			err = stdoutCloseErr
		case stderrCloseErr != nil:
			err = stderrCloseErr
		}
	}
	completedAt := time.Now().UTC()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *osexec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	_ = copyWorkflowStepLog(stdoutPath, filepath.Join(stepDir, "stdout.log"))
	_ = copyWorkflowStepLog(stderrPath, filepath.Join(stepDir, "stderr.log"))
	stdoutTail, stdoutTruncated := tailWorkflowLog(stdoutPath, a.execTailBytes())
	stderrTail, stderrTruncated := tailWorkflowLog(stderrPath, a.execTailBytes())
	summary := workflowStepSummary(stdoutTail, stderrTail, stdoutTruncated, stderrTruncated)
	status := domain.ExecutionStatusSucceeded
	errorDetails := ""
	if err != nil {
		status = domain.ExecutionStatusFailed
		errorDetails = err.Error()
	}
	if a.Store != nil {
		_ = a.Store.UpsertScheduledWorkflowStepRun(ctx, domain.ScheduledWorkflowStepRun{
			ID:            stepRunID,
			ProjectID:     request.ProjectID,
			RunID:         request.RunID,
			WorkflowID:    request.WorkflowID,
			StepID:        stepID,
			Status:        status,
			Attempt:       attempt,
			Command:       request.Step.Command,
			Args:          append([]string(nil), request.Step.Args...),
			StartedAt:     startedAt,
			CompletedAt:   &completedAt,
			ExitCode:      exitCode,
			StdoutLogPath: stdoutPath,
			StderrLogPath: stderrPath,
			OutputSummary: summary,
			ErrorDetails:  errorDetails,
			Metadata: domain.Metadata{
				"commit_hash": strings.TrimSpace(request.CommitHash),
			},
		})
	}
	_ = upsertWorkflowStepState(runPath, workflowRunStepState{
		ID:            stepID,
		Status:        string(status),
		Attempt:       attempt,
		Command:       request.Step.Command,
		Args:          append([]string(nil), request.Step.Args...),
		StartedAt:     &startedAt,
		CompletedAt:   &completedAt,
		ExitCode:      exitCode,
		StdoutLogPath: stdoutPath,
		StderrLogPath: stderrPath,
		OutputSummary: summary,
		ErrorDetails:  errorDetails,
	})
	result := workflowrun.ExecuteStepResult{
		StepID:        stepID,
		ExitCode:      exitCode,
		StdoutLogPath: stdoutPath,
		StderrLogPath: stderrPath,
		OutputSummary: summary,
	}
	if err != nil {
		return result, err
	}
	return result, nil
}

func (a *Activities) CompleteWorkflowRun(ctx context.Context, request workflowrun.CompleteRequest) error {
	completedAt := time.Now().UTC()
	if strings.TrimSpace(request.RunPath) != "" {
		_ = completeWorkflowRunState(strings.TrimSpace(request.RunPath), request.Status, completedAt, strings.TrimSpace(request.FailureSummary))
	}
	if a.Store == nil {
		return nil
	}
	return a.Store.UpsertScheduledWorkflowRun(ctx, domain.ScheduledWorkflowRun{
		ID:             strings.TrimSpace(request.RunID),
		ProjectID:      strings.TrimSpace(request.ProjectID),
		WorkflowID:     strings.TrimSpace(request.WorkflowID),
		RunPath:        strings.TrimSpace(request.RunPath),
		Status:         request.Status,
		StartedAt:      completedAt,
		ScheduledAt:    completedAt,
		CompletedAt:    &completedAt,
		FailureSummary: strings.TrimSpace(request.FailureSummary),
	})
}

func (a *Activities) NotifyWorkflowFailure(ctx context.Context, request workflowrun.NotifyFailureRequest) error {
	if a.Reporter == nil {
		return nil
	}
	message := "Scheduled workflow failed."
	if name := strings.TrimSpace(request.Input.WorkflowName); name != "" {
		message = "Scheduled workflow failed: " + name
	}
	if runID := strings.TrimSpace(request.RunID); runID != "" {
		message += "\nrun_id: " + runID
	}
	if failure := strings.TrimSpace(request.FailureSummary); failure != "" {
		message += "\nerror: " + failure
	}
	_, err := a.Reporter.Report(ctx, request.Input.SourceEvent, domain.ReportMessage{Text: message})
	return err
}

func (a *Activities) PersistEvent(ctx context.Context, request PersistEventRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	event = inferDiscordThreadContext(event)
	a.logActivityStep(
		"PersistEvent", "begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
	)
	result, err := a.Store.AppendEvent(ctx, event)
	if err != nil {
		a.logActivityStep(
			"PersistEvent", "error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	if result.Updated && result.Changed {
		a.activityLogger().Warn(
			"event id already existed with different content; stored event was updated",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
		)
	}
	if strings.TrimSpace(event.Body) != "" {
		message := domain.ConversationMessage{
			ID:          stableActivityID("conversation-user", event.ProjectID, event.ID),
			ProjectID:   event.ProjectID,
			EventID:     event.ID,
			Role:        domain.ConversationRoleUser,
			ChannelType: event.ChannelType,
			ChannelID:   strings.TrimSpace(event.ChannelID),
			ThreadID:    strings.TrimSpace(event.ThreadID),
			Body:        event.Body,
			Metadata:    conversationUserMetadata(event),
			CreatedAt:   firstNonZeroTime(event.CreatedAt, time.Now().UTC()),
		}
		if err := a.Store.UpsertConversationMessage(ctx, message); err != nil {
			a.logActivityStep(
				"PersistEvent", "conversation_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return err
		}
	}
	if err := a.persistConversationThread(ctx, conversationThreadFromEvent(event)); err != nil {
		a.logActivityStep(
			"PersistEvent", "thread_error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	a.logActivityStep(
		"PersistEvent", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.Bool("inserted", result.Inserted),
		slog.Bool("updated", result.Updated),
		slog.Bool("changed", result.Changed),
	)
	return nil
}

func (a *Activities) CompressConversation(ctx context.Context, request CompressConversationRequest) (CompressConversationResult, error) {
	if a.Store == nil || !a.ConversationEnabled || !a.ConversationSummaryEnabled || a.ConversationCompressor == nil {
		return CompressConversationResult{}, nil
	}
	event := inferDiscordThreadContext(request.Event)
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		return CompressConversationResult{}, nil
	}
	summaryScope, conversationScope := conversationCompressionScopes(event)
	rootMessage, hasRootMessage, err := a.compressionRootMessage(ctx, event, summaryScope)
	if err != nil {
		return CompressConversationResult{}, err
	}
	query := storage.ConversationSummaryQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		Scope:       summaryScope,
		Limit:       1,
	}
	latest, err := a.Store.ListConversationSummaries(ctx, query)
	if err != nil {
		return CompressConversationResult{}, err
	}
	var afterCreatedAt time.Time
	var afterID string
	if len(latest) > 0 {
		last := latest[len(latest)-1]
		afterCreatedAt = last.ToCreatedAt
		afterID = last.ToMessageID
	}
	messages, err := a.Store.ListConversationMessages(ctx, storage.ConversationQuery{
		ProjectID:      strings.TrimSpace(event.ProjectID),
		ChannelType:    event.ChannelType,
		ChannelID:      strings.TrimSpace(event.ChannelID),
		ThreadID:       strings.TrimSpace(event.ThreadID),
		Scope:          conversationScope,
		Roles:          conversationRoles(),
		Limit:          500,
		AfterCreatedAt: afterCreatedAt,
		AfterID:        afterID,
		OldestFirst:    true,
		ExcludeControl: true,
	})
	if err != nil {
		return CompressConversationResult{}, err
	}
	recent := storage.DefaultConversationSummaryRecentMessages(a.ConversationSummaryRecent)
	if len(messages) <= recent {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(messages)}, nil
	}
	candidates := messages[:len(messages)-recent]
	sourceChars := conversationSourceChars(candidates)
	trigger := storage.DefaultConversationSummaryTriggerChars(a.ConversationSummaryTrigger)
	if sourceChars < trigger {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(candidates), SourceChars: sourceChars}, nil
	}
	compressorMessages := conversationCompressionMessagesWithRoot(rootMessage, hasRootMessage, candidates)
	output, err := a.ConversationCompressor.CompressConversation(ctx, agent.ConversationCompressionInput{
		ProjectID:       strings.TrimSpace(event.ProjectID),
		Scope:           summaryScope,
		Messages:        compressorMessages,
		MaxSummaryChars: storage.DefaultConversationSummaryMaxChars(a.ConversationSummaryMaxChars),
	})
	if err != nil {
		return CompressConversationResult{}, err
	}
	summaryText := strings.TrimSpace(output.Summary)
	if summaryText == "" {
		return CompressConversationResult{Scope: string(summaryScope), MessageCount: len(candidates), SourceChars: sourceChars}, nil
	}
	first := candidates[0]
	last := candidates[len(candidates)-1]
	now := time.Now().UTC()
	summary := domain.ConversationSummary{
		ID:            stableActivityID("conversation-summary", event.ProjectID, string(summaryScope), string(event.ChannelType), event.ChannelID, event.ThreadID, first.ID, last.ID),
		ProjectID:     event.ProjectID,
		ChannelType:   event.ChannelType,
		ChannelID:     strings.TrimSpace(event.ChannelID),
		ThreadID:      strings.TrimSpace(event.ThreadID),
		Scope:         summaryScope,
		Summary:       summaryText,
		FromMessageID: strings.TrimSpace(first.ID),
		ToMessageID:   strings.TrimSpace(last.ID),
		FromCreatedAt: first.CreatedAt,
		ToCreatedAt:   last.CreatedAt,
		MessageCount:  len(candidates),
		SourceChars:   sourceChars,
		Metadata:      domain.Metadata{"source": "conversation_compressor"},
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := a.Store.UpsertConversationSummary(ctx, summary); err != nil {
		return CompressConversationResult{}, err
	}
	a.logActivityStep(
		"CompressConversation", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("scope", string(summaryScope)),
		slog.String("summary_id", summary.ID),
		slog.Int("message_count", len(candidates)),
		slog.Int("source_chars", sourceChars),
	)
	return CompressConversationResult{
		Summarized:   true,
		SummaryID:    summary.ID,
		Scope:        string(summaryScope),
		MessageCount: len(candidates),
		SourceChars:  sourceChars,
	}, nil
}

func (a *Activities) compressionRootMessage(ctx context.Context, event domain.Event, scope domain.ConversationSummaryScope) (domain.ConversationMessage, bool, error) {
	if scope != domain.ConversationSummaryScopeThread || strings.TrimSpace(event.ThreadID) == "" {
		return domain.ConversationMessage{}, false, nil
	}
	thread, ok, err := a.Store.GetConversationThread(ctx, storage.ConversationThreadQuery{
		ProjectID:   strings.TrimSpace(event.ProjectID),
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
	})
	if err != nil {
		return domain.ConversationMessage{}, false, err
	}
	if !ok {
		thread = domain.ConversationThread{RootMessageID: strings.TrimSpace(event.ThreadID)}
	}
	return a.conversationThreadRootMessage(ctx, event, thread)
}

func conversationCompressionMessagesWithRoot(root domain.ConversationMessage, ok bool, messages []domain.ConversationMessage) []domain.ConversationMessage {
	if !ok || strings.TrimSpace(root.ID) == "" {
		return messages
	}
	for _, message := range messages {
		if strings.TrimSpace(message.ID) == strings.TrimSpace(root.ID) {
			return messages
		}
	}
	out := make([]domain.ConversationMessage, 0, len(messages)+1)
	out = append(out, root)
	out = append(out, messages...)
	return out
}

func conversationCompressionScopes(event domain.Event) (domain.ConversationSummaryScope, storage.ConversationScope) {
	if strings.TrimSpace(event.ChannelID) != "" {
		if strings.TrimSpace(event.ThreadID) != "" {
			return domain.ConversationSummaryScopeThread, storage.ConversationScopeThread
		}
		return domain.ConversationSummaryScopeChannel, storage.ConversationScopeChannel
	}
	return domain.ConversationSummaryScopeProject, storage.ConversationScopeProject
}

func conversationRoles() []domain.ConversationRole {
	return []domain.ConversationRole{
		domain.ConversationRoleUser,
		domain.ConversationRoleAssistant,
		domain.ConversationRoleTool,
	}
}

func conversationSourceChars(messages []domain.ConversationMessage) int {
	total := 0
	for _, message := range messages {
		total += len(strings.TrimSpace(message.Body))
		if tool := strings.TrimSpace(message.Metadata["tool"]); tool != "" {
			total += len(tool)
		}
		if status := strings.TrimSpace(message.Metadata["status"]); status != "" {
			total += len(status)
		}
		total += len(message.Role)
	}
	return total
}

func (a *Activities) PersistNextAction(ctx context.Context, request PersistNextActionRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	a.logActivityStep(
		"PersistNextAction", "begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", request.Status),
		slog.Int("work_items", len(request.NextAction.WorkItems)),
	)
	if err := a.persistNextAction(ctx, request.NextAction); err != nil {
		a.logActivityStep(
			"PersistNextAction", "work_items_error",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return err
	}
	if shouldPersistNextActionConversation(request.Status) {
		message := strings.TrimSpace(request.NextAction.ResponseMessage)
		if message != "" || len(request.NextAction.ResponseAttachments) > 0 {
			metadata := domain.Metadata{"status": strings.TrimSpace(request.Status)}
			if len(request.NextAction.ResponseAttachments) > 0 {
				metadata["attachment_count"] = strconv.Itoa(len(request.NextAction.ResponseAttachments))
			}
			if err := a.Store.UpsertConversationMessage(ctx, domain.ConversationMessage{
				ID:          stableActivityID("conversation-assistant", event.ProjectID, event.ID, request.Status),
				ProjectID:   event.ProjectID,
				EventID:     event.ID,
				Role:        domain.ConversationRoleAssistant,
				ChannelType: event.ChannelType,
				ChannelID:   strings.TrimSpace(event.ChannelID),
				ThreadID:    strings.TrimSpace(event.ThreadID),
				Body:        message,
				Metadata:    metadata,
				CreatedAt:   time.Now().UTC(),
			}); err != nil {
				a.logActivityStep(
					"PersistNextAction", "conversation_error",
					slog.String("project_id", event.ProjectID),
					slog.String("event_id", event.ID),
					slog.String("error", err.Error()),
				)
				return err
			}
		}
	}
	a.logActivityStep(
		"PersistNextAction", "done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", request.Status),
	)
	return nil
}

func (a *Activities) PersistToolResult(ctx context.Context, request PersistToolResultRequest) error {
	if a.Store == nil {
		return nil
	}
	event := request.Event
	result := request.Result
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(result.ExecutionAttempt.ProjectID)
	}
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(result.ToolInvocation.ProjectID)
	}
	records, err := toolPersistenceRecords(event, result)
	if err != nil {
		return err
	}
	a.logActivityStep(
		"PersistToolResult", "begin",
		slog.String("project_id", records.Attempt.ProjectID),
		slog.String("work_item_id", records.Attempt.WorkItemID),
		slog.String("tool_call_id", result.ToolCallID),
		slog.String("tool_type", string(result.Tool)),
	)
	if err := a.Store.UpsertExecutionAttempt(ctx, records.Attempt); err != nil {
		return err
	}
	if err := a.Store.UpsertToolInvocation(ctx, records.Invocation); err != nil {
		return err
	}
	if strings.TrimSpace(records.Conversation.Body) != "" {
		if err := a.Store.UpsertConversationMessage(ctx, records.Conversation); err != nil {
			return err
		}
	}
	a.logActivityStep(
		"PersistToolResult", "done",
		slog.String("project_id", records.Attempt.ProjectID),
		slog.String("work_item_id", records.Attempt.WorkItemID),
		slog.String("tool_call_id", result.ToolCallID),
		slog.String("tool_type", string(result.Tool)),
	)
	return nil
}

func startResponseSessionHeartbeat(ctx context.Context, gap time.Duration, details any) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	if gap <= 0 {
		gap = defaultResponseSessionRefresh
	}
	recordResponseSessionHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(gap)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recordResponseSessionHeartbeat(ctx, details)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func recordResponseSessionHeartbeat(ctx context.Context, details any) {
	defer func() {
		_ = recover()
	}()
	activity.RecordHeartbeat(ctx, details)
}

func (a *Activities) startToolActivityHeartbeat(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) func() {
	if !activity.IsActivity(ctx) {
		return func() {}
	}
	gap := a.HeartbeatGap
	if gap <= 0 {
		gap = defaultToolHeartbeatGap
	}
	details := map[string]string{
		"command":      choice.Command,
		"intent":       choice.Intent,
		"tool_call_id": execution.ToolCallID,
	}
	recordToolHeartbeat(ctx, details)
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(gap)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				recordToolHeartbeat(ctx, details)
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		close(done)
	}
}

func recordToolHeartbeat(ctx context.Context, details any) {
	defer func() {
		_ = recover()
	}()
	activity.RecordHeartbeat(ctx, details)
}

func (a *Activities) NextAction(ctx context.Context, request NextActionRequest) (NextActionResult, error) {
	if request.Completion != nil {
		return a.completeTask(ctx, request.ProjectID, request.Event, agent.NextAction{}, *request.Completion, false, false)
	}
	a.logActivityStep(
		"NextAction", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("event_id", request.Event.ID),
		slog.Int("execution_cycle", request.ExecutionCycle),
		slog.Bool("force_final", request.ForceFinal),
		slog.Bool("resumed_from_pause", request.ResumedFromPause),
		slog.Bool("has_last_result", request.LastResult != nil),
		slog.Int("observation_history_len", len(request.ObservationHistory)),
	)
	if a.Engine == nil {
		a.logActivityStep("NextAction", "missing_engine")
		return NextActionResult{}, fmt.Errorf("next action engine is not configured")
	}

	projectID := strings.TrimSpace(request.ProjectID)
	event := request.Event
	if projectID == "" {
		projectID = strings.TrimSpace(event.ProjectID)
	}
	if projectID == "" {
		a.logActivityStep("NextAction", "missing_project_id", slog.String("event_project_id", event.ProjectID))
		return NextActionResult{}, fmt.Errorf("project_id is required")
	}
	event.ProjectID = projectID

	a.logActivityStep(
		"NextAction", "load_context_begin",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
	)
	conversationEvent := latestConversationContextEvent(event, request.AdditionalEvents)
	loaded, err := a.loadContext(ctx, event, conversationEvent, request.AdditionalEvents)
	if err != nil {
		a.logActivityStep(
			"NextAction", "load_context_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	loaded.AdditionalEvents = append([]domain.Event(nil), request.AdditionalEvents...)
	a.logActivityStep(
		"NextAction", "load_context_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("active_work_items", len(loaded.ActiveWorkItems)),
	)

	now := time.Now().UTC()
	nextAction := request.NextAction
	if err := ensureNextAction(&nextAction, projectID, event, now); err != nil {
		a.logActivityStep(
			"NextAction", "ensure_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "ensure_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	history := append([]agent.ExecutionFeedback(nil), request.ObservationHistory...)
	var observations []agent.ExecutionFeedback
	if len(request.LastResults) > 0 || request.LastResult != nil {
		a.logActivityStep(
			"NextAction", "apply_last_result_begin",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.Int("last_results", len(request.LastResults)),
		)
		results := request.LastResults
		if len(results) == 0 && request.LastResult != nil {
			results = []ExecuteToolResult{*request.LastResult}
		}
		nextAction.ToolChoice = agent.ToolChoice{}
		for _, result := range results {
			feedback := executionFeedback(result)
			observations = append(observations, feedback)
			history = append(history, feedback)
			if err := applyObservationToNextAction(&nextAction, feedback, now); err != nil {
				a.logActivityStep(
					"NextAction", "apply_last_result_error",
					slog.String("project_id", projectID),
					slog.String("event_id", event.ID),
					slog.String("work_item_id", feedback.WorkItemID),
					slog.String("tool_call_id", feedback.ToolCallID),
					slog.String("error", err.Error()),
				)
				return NextActionResult{}, err
			}
		}
		a.logActivityStep(
			"NextAction", "apply_last_result_done",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.Int("applied_results", len(observations)),
			slog.Int("history_len", len(history)),
		)
	}

	a.logActivityStep(
		"NextAction", "engine_next_action_begin",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("execution_cycle", request.ExecutionCycle),
		slog.Int("history_len", len(history)),
	)
	engineOutput, err := a.Engine.NextAction(ctx, agent.NextActionInput{
		ProjectID:          projectID,
		Context:            loaded,
		NextAction:         nextAction,
		Runtime:            buildRuntimeContext(a.WorkspaceRoot, a.OpenCTORoot),
		ExecutionCycle:     request.ExecutionCycle,
		ForceFinal:         request.ForceFinal,
		ResumedFromPause:   request.ResumedFromPause,
		LastObservation:    lastObservation(observations),
		ObservationHistory: history,
		ChannelType:        event.ChannelType,
	})
	if err != nil {
		a.logActivityStep(
			"NextAction", "engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
		slog.Bool("has_tool_choice", engineOutput.ToolChoice != nil),
		slog.String("work_item_id", strings.TrimSpace(engineOutput.WorkItemID)),
	)
	if len(engineOutput.NextAction.WorkItems) > 0 || !engineOutput.NextAction.ToolChoice.IsZero() || strings.TrimSpace(engineOutput.NextAction.ResponseMessage) != "" || len(engineOutput.NextAction.ResponseAttachments) > 0 {
		nextAction = engineOutput.NextAction
	}
	if err := ensureNextAction(&nextAction, projectID, event, now); err != nil {
		a.logActivityStep(
			"NextAction", "ensure_engine_next_action_error",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "ensure_engine_next_action_done",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	if request.ForceFinal {
		a.logActivityStep(
			"NextAction", "force_final_override_status",
			slog.String("project_id", projectID),
			slog.String("event_id", event.ID),
		)
		engineOutput.Status = NextActionStatusBlocked
		engineOutput.ToolChoice = nil
		if strings.TrimSpace(engineOutput.NextAction.ResponseMessage) == "" {
			engineOutput.NextAction.ResponseMessage = cycleLimitResponseMessage(history)
		}
	}

	a.logActivityStep(
		"NextAction", "dispatch_status",
		slog.String("project_id", projectID),
		slog.String("event_id", event.ID),
		slog.String("status", engineOutput.Status),
	)
	switch engineOutput.Status {
	case NextActionStatusTool:
		return a.prepareToolNextAction(ctx, nextAction, observations, engineOutput, request.ExecutionCycle, now)
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return a.finishNextAction(ctx, event, nextAction, lastObservation(observations), observations, engineOutput, request.Processes, now)
	default:
		return NextActionResult{}, fmt.Errorf("unsupported next action status %q", engineOutput.Status)
	}
}

func latestConversationContextEvent(base domain.Event, additional []domain.Event) domain.Event {
	target := base
	for _, event := range additional {
		if strings.TrimSpace(event.ChannelID) == "" && strings.TrimSpace(event.ThreadID) == "" {
			continue
		}
		target = event
		if strings.TrimSpace(target.ProjectID) == "" {
			target.ProjectID = strings.TrimSpace(base.ProjectID)
		}
		if strings.TrimSpace(string(target.ChannelType)) == "" {
			target.ChannelType = base.ChannelType
		}
		target = inferDiscordThreadContext(target)
	}
	return target
}

func inferDiscordThreadContext(event domain.Event) domain.Event {
	if event.ChannelType != domain.ChannelTypeDiscord {
		return event
	}
	if strings.TrimSpace(event.ThreadID) != "" || strings.TrimSpace(event.ChannelID) == "" {
		return event
	}
	if strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]) != "" {
		return event
	}
	switch strings.TrimSpace(event.Metadata[domain.MetadataKeyControl]) {
	case domain.MetadataControlTaskReply:
		event.ThreadID = strings.TrimSpace(event.ChannelID)
	}
	return event
}

func (a *Activities) prepareToolNextAction(ctx context.Context, nextAction agent.NextAction, observations []agent.ExecutionFeedback, output agent.NextActionOutput, cycle int, now time.Time) (NextActionResult, error) {
	a.logActivityStep(
		"NextAction", "prepare_tool_begin",
		slog.Int("execution_cycle", cycle),
		slog.Int("observations", len(observations)),
		slog.Bool("has_tool_choice", output.ToolChoice != nil),
		slog.Int("tool_choices", len(output.ToolChoices)),
		slog.String("output_work_item_id", strings.TrimSpace(output.WorkItemID)),
	)
	choices := append([]agent.ToolChoice(nil), output.ToolChoices...)
	if len(choices) == 0 && output.ToolChoice != nil {
		choices = []agent.ToolChoice{*output.ToolChoice}
	}
	if len(choices) == 0 {
		a.logActivityStep("NextAction", "prepare_tool_missing_choice")
		return NextActionResult{}, fmt.Errorf("%w: tool status requires at least one tool choice", agent.ErrInvalidToolChoice)
	}

	observation := lastObservation(observations)
	choice := choices[0]
	workItemID := nextActionToolWorkItemID(nextAction, observation)
	if strings.TrimSpace(workItemID) == "" {
		a.logActivityStep(
			"NextAction", "prepare_tool_missing_work_item_id",
			slog.String("tool_call_id", choice.ToolCallID),
		)
		return NextActionResult{}, fmt.Errorf("%w: tool choice is missing work item id", agent.ErrInvalidToolChoice)
	}
	a.logActivityStep(
		"NextAction", "prepare_tool_work_item_resolved",
		slog.String("work_item_id", workItemID),
		slog.String("tool_type", string(choice.Type)),
		slog.String("tool_call_id", choice.ToolCallID),
	)
	if err := ensureToolWorkItem(&nextAction, workItemID, now); err != nil {
		a.logActivityStep(
			"NextAction", "prepare_tool_ensure_work_item_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	if err := completePreviousWorkItemForNextAction(&nextAction, workItemID, observation, now); err != nil {
		a.logActivityStep(
			"NextAction", "prepare_tool_complete_previous_error",
			slog.String("work_item_id", workItemID),
			slog.String("error", err.Error()),
		)
		return NextActionResult{}, err
	}
	a.logActivityStep(
		"NextAction", "prepare_tool_work_items_ready",
		slog.String("work_item_id", workItemID),
		slog.Int("work_items", len(nextAction.WorkItems)),
	)

	index := nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID)
	if index >= 0 {
		nextAction.WorkItems[index].Status = domain.WorkItemStatusRunning
		nextAction.WorkItems[index].UpdatedAt = now
	}

	assistantText := strings.TrimSpace(output.AssistantText)
	if assistantText == "" {
		assistantText = strings.TrimSpace(choice.Intent)
	}
	toolCallIDs := make([]string, 0, len(choices))
	for _, choice := range choices {
		toolCallIDs = append(toolCallIDs, strings.TrimSpace(choice.ToolCallID))
	}
	for index := range choices {
		ensureToolChoiceMetadata(&choices[index], workItemID, cycle, assistantText)
		choices[index].Metadata["tool_call_ids"] = strings.Join(toolCallIDs, ",")
	}
	choice = choices[0]
	nextAction.ToolChoice = choice
	nextAction.ResponseMessage = ""

	a.logActivityStep(
		"NextAction", "prepare_tool_done",
		slog.String("work_item_id", workItemID),
		slog.String("tool_call_id", choice.ToolCallID),
		slog.String("tool_type", string(choice.Type)),
	)

	return NextActionResult{
		NextAction:   nextAction,
		ToolChoice:   &choice,
		ToolChoices:  choices,
		WorkItemID:   workItemID,
		Observation:  observation,
		Observations: observations,
		Status:       NextActionStatusTool,
	}, nil
}

func (a *Activities) finishNextAction(ctx context.Context, event domain.Event, nextAction agent.NextAction, observation *agent.ExecutionFeedback, observations []agent.ExecutionFeedback, output agent.NextActionOutput, processes []domain.ProcessReference, now time.Time) (NextActionResult, error) {
	a.logActivityStep(
		"NextAction", "finish_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", output.Status),
		slog.Bool("has_observation", observation != nil),
	)
	message := strings.TrimSpace(output.NextAction.ResponseMessage)
	attachments := append([]domain.ReportAttachment(nil), output.NextAction.ResponseAttachments...)
	if message == "" && len(attachments) == 0 {
		a.logActivityStep(
			"NextAction", "finish_missing_response_message",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", output.Status),
		)
		return NextActionResult{}, fmt.Errorf("%w: terminal next action is missing response message", agent.ErrInvalidNextAction)
	}

	nextAction.ToolChoice = agent.ToolChoice{}
	nextAction.ResponseMessage = message
	nextAction.ResponseAttachments = attachments
	markFinalNextActionWorkItems(&nextAction, terminalWorkItemStatus(output.Status), observation, now)
	result, err := a.completeTask(ctx, event.ProjectID, event, nextAction, TaskCompletionRequest{
		Status:    output.Status,
		Processes: processes,
	}, false, false)
	if err != nil {
		return NextActionResult{}, err
	}
	result.Observation = observation
	result.Observations = observations
	a.logActivityStep(
		"NextAction", "finish_done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", result.Status),
	)
	return result, nil
}

func (a *Activities) completeTask(ctx context.Context, projectID string, event domain.Event, nextAction agent.NextAction, request TaskCompletionRequest, persist bool, report bool) (NextActionResult, error) {
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(projectID)
	}
	status := strings.TrimSpace(request.Status)
	if status == "" {
		status = NextActionStatusFailed
	}
	a.logActivityStep(
		"NextAction", "complete_task_begin",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", status),
		slog.Int("processes", len(request.Processes)),
		slog.Bool("persist", persist),
		slog.Bool("report", report),
	)

	cleaned, stopped, cleanupFailed := a.cleanupTaskProcesses(ctx, event.ProjectID, request.Processes)
	if cleanupFailed {
		status = NextActionStatusFailed
		markFinalNextActionWorkItems(&nextAction, domain.WorkItemStatusFailed, nil, time.Now().UTC())
	}
	if stopped || cleanupFailed {
		nextAction.ResponseMessage = appendProcessCleanupNotice(nextAction.ResponseMessage, cleaned, stopped, cleanupFailed)
	}

	message := strings.TrimSpace(nextAction.ResponseMessage)
	attachments := append([]domain.ReportAttachment(nil), nextAction.ResponseAttachments...)
	if persist {
		if err := a.persistNextAction(ctx, nextAction); err != nil {
			a.logActivityStep(
				"NextAction", "complete_task_persist_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
	}
	reportMessage := domain.ReportMessage{Text: message, Attachments: attachments}
	if report && status != NextActionStatusIgnored && a.Reporter != nil && !reportMessage.Empty() {
		a.logActivityStep(
			"NextAction", "complete_task_report_begin",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
		if _, err := a.Reporter.Report(ctx, event, reportMessage); err != nil {
			a.logActivityStep(
				"NextAction", "complete_task_report_error",
				slog.String("project_id", event.ProjectID),
				slog.String("event_id", event.ID),
				slog.String("error", err.Error()),
			)
			return NextActionResult{}, err
		}
		a.logActivityStep(
			"NextAction", "complete_task_report_done",
			slog.String("project_id", event.ProjectID),
			slog.String("event_id", event.ID),
			slog.String("status", status),
		)
	}
	a.logActivityStep(
		"NextAction", "complete_task_done",
		slog.String("project_id", event.ProjectID),
		slog.String("event_id", event.ID),
		slog.String("status", status),
		slog.Int("processes", len(cleaned)),
	)
	return NextActionResult{
		NextAction: nextAction,
		Status:     status,
		Processes:  cleaned,
	}, nil
}

func (a *Activities) cleanupTaskProcesses(ctx context.Context, projectID string, processes []domain.ProcessReference) ([]domain.ProcessReference, bool, bool) {
	if len(processes) == 0 {
		return nil, false, false
	}
	updated := append([]domain.ProcessReference(nil), processes...)
	failed := false
	stoppedAny := false
	manager := exectool.NewProcessManager(a.activityLogger())
	for index := range updated {
		process := &updated[index]
		if process.Scope == domain.ProcessScopeProject || process.Status != domain.ProcessStatusRunning {
			continue
		}
		stopped, err := manager.Stop(ctx, a.runtimeStateDir(), process.ID)
		if err != nil {
			failed = true
			continue
		}
		if stopped.Status == domain.ProcessStatusRunning {
			failed = true
			continue
		}
		process.Status = stopped.Status
		stoppedAny = true
	}
	return updated, stoppedAny, failed
}

func appendProcessCleanupNotice(message string, processes []domain.ProcessReference, stopped bool, failed bool) string {
	var notes []string
	if stopped {
		notes = append(notes, "OpenCTO stopped stop-on-finish background process(es) at task completion: "+processRefs(processes, false))
	}
	if failed {
		notes = append(notes, "OpenCTO could not stop one or more stop-on-finish background process(es); they may still be running: "+processRefs(processes, true))
	}
	note := strings.Join(notes, " ")
	if note == "" {
		return strings.TrimSpace(message)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return note
	}
	return message + " · " + note
}

func processRefs(processes []domain.ProcessReference, running bool) string {
	var refs []string
	for _, process := range processes {
		if process.Scope == domain.ProcessScopeProject {
			continue
		}
		if running && process.Status != domain.ProcessStatusRunning {
			continue
		}
		if !running && process.Status == domain.ProcessStatusRunning {
			continue
		}
		label := strings.TrimSpace(process.Description)
		if label == "" {
			label = process.ID
		}
		if process.ID != "" && label != process.ID {
			label += " (" + process.ID + ")"
		}
		refs = append(refs, label)
		if len(refs) == 3 {
			break
		}
	}
	if len(refs) == 0 {
		return "unknown"
	}
	return strings.Join(refs, ", ")
}

func (a *Activities) persistNextAction(ctx context.Context, nextAction agent.NextAction) error {
	if a.Store == nil {
		a.logActivityStep(
			"NextAction", "persist_next_action_skip_no_store",
			slog.Int("work_items", len(nextAction.WorkItems)),
		)
		return nil
	}
	items := make([]domain.WorkItem, 0, len(nextAction.WorkItems))
	for _, item := range nextAction.WorkItems {
		if item.ID == "" {
			a.logActivityStep("NextAction", "persist_next_action_skip_empty_work_item_id")
			continue
		}
		items = append(items, item)
	}
	return a.Store.UpsertWorkItems(ctx, items)
}

func (a *Activities) ExecuteMemoryTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	a.logActivityStep(
		"ExecuteMemoryTool", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("tool_call_id", strings.TrimSpace(request.ToolChoice.ToolCallID)),
	)
	if a.Store == nil || !a.MemoryEnabled {
		return ExecuteToolResult{}, temporal.NewNonRetryableApplicationError("memory store is not configured", "MemoryUnavailable", nil)
	}
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
	}
	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       request.ToolChoice.Type,
		Summary:    request.ToolChoice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
		},
	}
	run, runErr := a.runMemoryTool(ctx, request.ToolChoice, execution)
	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool completed.")
	status := domain.ExecutionStatusSucceeded
	resultCode := "0"
	errorMessage := ""
	if run.Status != "" {
		status = run.Status
	}
	if strings.TrimSpace(run.ResultCode) != "" {
		resultCode = strings.TrimSpace(run.ResultCode)
	}
	if strings.TrimSpace(run.Error) != "" {
		errorMessage = strings.TrimSpace(run.Error)
	}
	if runErr != nil {
		status = domain.ExecutionStatusFailed
		resultCode = "1"
		errorMessage = runErr.Error()
		attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool failed.")
	} else if status == domain.ExecutionStatusFailed {
		attempt.OutputSummary = firstNonEmpty(run.Observation, "Memory tool failed.")
	}
	attempt.Status = status
	metadata := map[string]string{
		"started_at":   execution.StartedAt.UTC().Format(time.RFC3339Nano),
		"completed_at": completedAt.UTC().Format(time.RFC3339Nano),
		"tool_call_id": execution.ToolCallID,
	}
	for key, value := range request.ToolChoice.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	for key, value := range run.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	attempt.Metadata = metadata
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   request.ToolChoice.WorkingDir,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		InputPayload:       cloneRawMessage(request.ToolChoice.Input),
		OutputSummary:      attempt.OutputSummary,
		OutputPayload:      cloneRawMessage(run.Payload),
		ResultCode:         resultCode,
		ErrorDetails:       errorMessage,
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}
	result := ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           status,
		RequestedAction:  request.ToolChoice.Intent,
		Input:            cloneRawMessage(request.ToolChoice.Input),
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       invocation.ResultCode,
		Metadata:         metadata,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = firstRawMessage(run.Payload, executeToolResultPayload(result))
	a.logActivityStep(
		"ExecuteMemoryTool", "done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("status", string(status)),
	)
	return result, runErr
}

func (a *Activities) ExecuteTool(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	if request.ToolChoice.Type == domain.ToolTypeExec && request.ToolChoice.RunMode == domain.ToolRunModeStartBackground {
		return a.startExecProcess(ctx, request)
	}
	a.logActivityStep(
		"ExecuteTool", "start",
		slog.String("project_id", strings.TrimSpace(request.ProjectID)),
		slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("tool_call_id", strings.TrimSpace(request.ToolChoice.ToolCallID)),
		slog.String("command", request.ToolChoice.Command),
		slog.Any("args", request.ToolChoice.Args),
		slog.Int("input_bytes", len(request.ToolChoice.Input)),
	)
	execution, err := newToolExecutionContext(request)
	if err != nil {
		a.logActivityStep(
			"ExecuteTool", "new_execution_context_error",
			slog.String("project_id", strings.TrimSpace(request.ProjectID)),
			slog.String("work_item_id", strings.TrimSpace(request.WorkItemID)),
			slog.String("error", err.Error()),
		)
		return ExecuteToolResult{}, err
	}
	a.logActivityStep(
		"ExecuteTool", "new_execution_context_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.Int("cycle", execution.Cycle),
		slog.Duration("timeout", execution.Timeout),
		slog.Any("fallback_candidates", execution.FallbackCandidates),
	)
	stopHeartbeat := a.startToolActivityHeartbeat(ctx, request.ToolChoice, execution)
	defer stopHeartbeat()

	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       request.ToolChoice.Type,
		Summary:    request.ToolChoice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
		},
	}

	a.logActivityStep(
		"ExecuteTool", "tool_run_begin",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
	)
	toolResult, runErr := a.runChosenTool(ctx, request.ToolChoice, execution)
	a.logActivityStep(
		"ExecuteTool", "tool_run_done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("tool_type", string(request.ToolChoice.Type)),
		slog.String("result_code", toolResult.ResultCode),
		slog.Bool("tool_error", runErr != nil),
	)

	completedAt := time.Now().UTC()
	attempt.CompletedAt = &completedAt
	metadata := map[string]string{
		"started_at":   execution.StartedAt.UTC().Format(time.RFC3339Nano),
		"completed_at": completedAt.UTC().Format(time.RFC3339Nano),
		"tool_call_id": execution.ToolCallID,
	}
	for key, value := range request.ToolChoice.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	for key, value := range toolResult.Metadata {
		if strings.TrimSpace(value) != "" {
			metadata[key] = value
		}
	}
	if request.ToolChoice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(request.ToolChoice.TimeoutMs)
	}
	if request.ToolChoice.Destructive {
		metadata["destructive"] = "true"
	}
	resultInput := cloneRawMessage(request.ToolChoice.Input)
	if len(strings.TrimSpace(string(toolResult.Input))) > 0 {
		resultInput = cloneRawMessage(toolResult.Input)
	}

	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    request.ToolChoice.Intent,
		ChosenTool:         request.ToolChoice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   firstNonEmpty(toolResult.WorkingDirectory, request.ToolChoice.WorkingDir),
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       request.ToolChoice.InputSummary,
		InputPayload:       cloneRawMessage(resultInput),
		OutputSummary:      toolResult.Observation,
		ResultCode:         firstNonEmpty(toolResult.ResultCode, "0"),
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}

	var errorMessage string
	if runErr != nil {
		a.logActivityStep(
			"ExecuteTool", "tool_run_result_error",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
			slog.String("error", runErr.Error()),
		)
		attempt.Status = domain.ExecutionStatusFailed
		attempt.OutputSummary = firstNonEmpty(toolResult.Observation, "Tool execution failed.")
		invocation.ErrorDetails = runErr.Error()
		invocation.OutputSummary = attempt.OutputSummary
		if invocation.ResultCode == "" || invocation.ResultCode == "0" {
			invocation.ResultCode = "1"
		}
		errorMessage = runErr.Error()
	} else {
		a.logActivityStep(
			"ExecuteTool", "tool_run_result_success",
			slog.String("project_id", execution.ProjectID),
			slog.String("work_item_id", execution.WorkItemID),
			slog.String("tool_call_id", execution.ToolCallID),
		)
		attempt.Status = domain.ExecutionStatusSucceeded
		attempt.OutputSummary = firstNonEmpty(toolResult.Observation, "Execution completed.")
		invocation.OutputSummary = attempt.OutputSummary
	}
	a.logActivityStep(
		"ExecuteTool", "done",
		slog.String("project_id", execution.ProjectID),
		slog.String("work_item_id", execution.WorkItemID),
		slog.String("tool_call_id", execution.ToolCallID),
		slog.String("attempt_status", string(attempt.Status)),
		slog.String("result_code", invocation.ResultCode),
	)

	result := ExecuteToolResult{
		Cycle:            attempt.Attempt,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             request.ToolChoice.Type,
		Status:           attempt.Status,
		RequestedAction:  request.ToolChoice.Intent,
		Command:          request.ToolChoice.Command,
		Args:             request.ToolChoice.Args,
		Input:            resultInput,
		Observation:      attempt.OutputSummary,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       invocation.ResultCode,
		Metadata:         invocation.Metadata,
		Processes:        toolResult.Processes,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = executeToolResultPayload(result)
	return result, nil
}

func (a *Activities) startExecProcess(ctx context.Context, request ExecuteToolRequest) (ExecuteToolResult, error) {
	execution, err := newToolExecutionContext(request)
	if err != nil {
		return ExecuteToolResult{}, err
	}
	choice := request.ToolChoice
	if choice.Type != domain.ToolTypeExec {
		return ExecuteToolResult{}, fmt.Errorf("start background process requires exec tool, got %q", choice.Type)
	}
	processScope := toolProcessScope(choice.ProcessScope)
	attempt := domain.ExecutionAttempt{
		ID:         execution.ExecutionAttemptID,
		ProjectID:  execution.ProjectID,
		WorkItemID: execution.WorkItemID,
		Status:     domain.ExecutionStatusRunning,
		Attempt:    execution.Cycle,
		Tool:       choice.Type,
		Summary:    choice.Intent,
		StartedAt:  execution.StartedAt,
		Metadata: map[string]string{
			"execution_cycle": strconv.Itoa(execution.Cycle),
			"tool_call_id":    execution.ToolCallID,
			"run_mode":        string(domain.ToolRunModeStartBackground),
			"process_scope":   string(processScope),
		},
	}
	processID := stableActivityID("managed-process", execution.ProjectID, execution.WorkItemID, execution.ToolCallID)
	stateDir := a.runtimeStateDir()
	manager := exectool.NewProcessManager(a.activityLogger())
	process, runErr := manager.Start(ctx, exectool.StartProcessRequest{
		ProcessID:    processID,
		ProjectID:    execution.ProjectID,
		WorkItemID:   execution.WorkItemID,
		ToolCallID:   execution.ToolCallID,
		Intent:       choice.Intent,
		ProcessScope: processScope,
		Command:      choice.Command,
		Args:         choice.Args,
		WorkingDir:   resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		StateDir:     stateDir,
		Timeout:      execution.Timeout,
		Environment:  workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
	})
	metadata := map[string]string{
		"tool_call_id":                  execution.ToolCallID,
		"work_item_id":                  execution.WorkItemID,
		"execution_cycle":               strconv.Itoa(execution.Cycle),
		"run_mode":                      string(domain.ToolRunModeStartBackground),
		"idempotency":                   string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":                 string(processScope),
		"process_id":                    processID,
		"possible_long_running_process": "true",
	}
	if choice.TimeoutMs > 0 {
		metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
	if process.PID > 0 {
		metadata["pid"] = strconv.Itoa(process.PID)
	}
	if process.PGID > 0 {
		metadata["pgid"] = strconv.Itoa(process.PGID)
	}
	if process.StdoutLogPath != "" {
		metadata["stdout_log_path"] = process.StdoutLogPath
	}
	if process.StderrLogPath != "" {
		metadata["stderr_log_path"] = process.StderrLogPath
	}
	status := domain.ExecutionStatusSucceeded
	resultCode := "0"
	observation := processStartObservation(process)
	var errorMessage string
	if runErr != nil {
		status = domain.ExecutionStatusFailed
		resultCode = "1"
		errorMessage = runErr.Error()
		observation = backgroundStartFailureObservation(ctx, manager, stateDir, process, runErr)
	}
	completedAt := time.Now().UTC()
	attempt.Status = status
	attempt.OutputSummary = observation
	attempt.CompletedAt = &completedAt
	invocation := domain.ToolInvocation{
		ID:                 execution.InvocationID,
		ProjectID:          execution.ProjectID,
		ExecutionAttemptID: execution.ExecutionAttemptID,
		RequestedIntent:    choice.Intent,
		ChosenTool:         choice.Type,
		FallbackCandidates: execution.FallbackCandidates,
		WorkingDirectory:   process.WorkingDirectory,
		TimeoutSeconds:     int(execution.Timeout.Seconds()),
		InputSummary:       choice.InputSummary,
		InputPayload:       cloneRawMessage(choice.Input),
		OutputSummary:      observation,
		ResultCode:         resultCode,
		ErrorDetails:       errorMessage,
		CreatedAt:          execution.StartedAt,
		CompletedAt:        &completedAt,
		Metadata:           metadata,
	}
	processes := []domain.ProcessReference(nil)
	if status == domain.ExecutionStatusSucceeded && strings.TrimSpace(process.ID) != "" {
		processes = []domain.ProcessReference{{
			ID:          process.ID,
			Description: firstNonEmpty(choice.Intent, choice.Command),
			Status:      process.Status,
			Scope:       processScope,
		}}
	}
	result := ExecuteToolResult{
		Cycle:            execution.Cycle,
		WorkItemID:       execution.WorkItemID,
		ToolCallID:       execution.ToolCallID,
		Tool:             choice.Type,
		Status:           status,
		RequestedAction:  choice.Intent,
		Command:          choice.Command,
		Args:             choice.Args,
		Input:            cloneRawMessage(choice.Input),
		Observation:      observation,
		Error:            errorMessage,
		WorkingDirectory: invocation.WorkingDirectory,
		ResultCode:       resultCode,
		Metadata:         metadata,
		Processes:        processes,
		ExecutionAttempt: attempt,
		ToolInvocation:   invocation,
	}
	result.ToolInvocation.OutputPayload = executeToolResultPayload(result)
	return result, nil
}

func (a *Activities) runChosenTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeExec:
		return a.runExecTool(ctx, choice, execution)
	case domain.ToolTypeRead:
		return a.runReadTool(ctx, choice)
	case domain.ToolTypeEdit:
		return a.runEditTool(ctx, choice)
	case domain.ToolTypeWrite:
		return a.runWriteTool(ctx, choice, execution)
	case domain.ToolTypeGlob:
		return a.runGlobTool(ctx, choice, execution)
	case domain.ToolTypeGrep:
		return a.runGrepTool(ctx, choice, execution)
	case domain.ToolTypeSchedule:
		return a.runScheduleTool(ctx, choice, execution)
	case domain.ToolTypeSkill:
		return a.runSkillTool(ctx, choice)
	default:
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("unsupported tool type %q", choice.Type)
	}
}

func (a *Activities) runExecTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	if a.Exec == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("exec executor is not configured")
	}
	req := exectool.Request{
		ProjectID:          execution.ProjectID,
		WorkItemID:         execution.WorkItemID,
		ToolCallID:         execution.ToolCallID,
		ProcessID:          stableActivityID("managed-process", execution.ProjectID, execution.WorkItemID, execution.ToolCallID),
		Intent:             choice.Intent,
		Command:            choice.Command,
		Args:               choice.Args,
		WorkingDir:         resolveRelativeToolPath(firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot),
		StateDir:           a.runtimeStateDir(),
		Timeout:            execution.Timeout,
		GracePeriod:        a.execGrace(execution.Timeout),
		TailBytes:          a.execTailBytes(),
		ProcessScope:       toolProcessScope(choice.ProcessScope),
		Environment:        workspaceEnvironment(a.WorkspaceRoot, a.OpenCTORoot),
		FallbackCandidates: execution.FallbackCandidates,
	}
	result, err := a.Exec.Run(ctx, req)
	metadata := map[string]string{
		"exec_exit_status": strconv.Itoa(result.ExitCode),
		"run_mode":         string(firstNonEmpty(string(choice.RunMode), string(domain.ToolRunModeWaitForExit))),
		"idempotency":      string(firstNonEmpty(string(choice.Idempotency), string(domain.ToolIdempotencyUnknown))),
		"process_scope":    string(toolProcessScope(choice.ProcessScope)),
	}
	if result.StdoutLogPath != "" {
		metadata["stdout_log_path"] = result.StdoutLogPath
	}
	if result.StderrLogPath != "" {
		metadata["stderr_log_path"] = result.StderrLogPath
	}
	if result.StdoutTruncated {
		metadata["stdout_truncated"] = "true"
	}
	if result.StderrTruncated {
		metadata["stderr_truncated"] = "true"
	}
	resultCode := strconv.Itoa(result.ExitCode)
	if errors.Is(err, context.DeadlineExceeded) {
		resultCode = "timeout"
		metadata["possible_long_running_process"] = "true"
		metadata["timeout"] = "true"
	}
	var processes []domain.ProcessReference
	if result.ManagedProcess != nil {
		process := *result.ManagedProcess
		metadata["process_id"] = process.ID
		metadata["possible_long_running_process"] = "true"
		metadata["promoted_to_managed_process"] = "true"
		if process.PID > 0 {
			metadata["pid"] = strconv.Itoa(process.PID)
		}
		if process.PGID > 0 {
			metadata["pgid"] = strconv.Itoa(process.PGID)
		}
		processes = []domain.ProcessReference{{
			ID:          process.ID,
			Description: firstNonEmpty(choice.Intent, choice.Command),
			Status:      process.Status,
			Scope:       toolProcessScope(choice.ProcessScope),
		}}
	}
	if !result.StartedAt.IsZero() {
		metadata["tool_started_at"] = result.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if !result.CompletedAt.IsZero() {
		metadata["tool_completed_at"] = result.CompletedAt.UTC().Format(time.RFC3339Nano)
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      execObservation(result, err),
		ResultCode:       resultCode,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
		Processes:        processes,
	}, err
}

func execObservation(result exectool.Result, err error) string {
	observation := fullObservation(result.Stdout, result.Stderr, err)
	var notes []string
	if result.ManagedProcess != nil {
		notes = append(
			notes,
			"status: running",
			"process_id: "+result.ManagedProcess.ID,
			"possible_long_running_process: true",
		)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		notes = append(notes, "output_truncated: true")
	}
	if result.StdoutLogPath != "" {
		notes = append(notes, "stdout_log_path: "+result.StdoutLogPath)
	}
	if result.StderrLogPath != "" {
		notes = append(notes, "stderr_log_path: "+result.StderrLogPath)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		notes = append(notes, "result_code: timeout", "possible_long_running_process: true")
	}
	if len(notes) == 0 {
		return observation
	}
	return observation + "\n\n" + strings.Join(notes, "\n")
}

func (a *Activities) runReadTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req readtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Read
	if executor == nil {
		executor = readtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"file_path":   result.FilePath,
		"lines_read":  strconv.Itoa(result.LinesRead),
		"total_lines": strconv.Itoa(result.TotalLines),
		"bytes_read":  strconv.Itoa(result.BytesRead),
		"truncated":   strconv.FormatBool(result.Truncated),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	return toolRunResult{
		Observation: readObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runEditTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req edittool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Edit
	if executor == nil {
		executor = edittool.NewTool()
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: editObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"file_path":     result.FilePath,
			"replacements":  strconv.Itoa(result.Replacements),
			"bytes_written": strconv.Itoa(result.BytesWritten),
		},
	}, err
}

func (a *Activities) runWriteTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req writetool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent

	executor := a.Write
	if executor == nil {
		executor = writetool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"file_path":     result.FilePath,
		"bytes_written": strconv.Itoa(result.BytesWritten),
		"overwritten":   strconv.FormatBool(result.Overwritten),
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation: writeObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runGlobTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req globtool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.Cwd = resolveRelativeToolPath(firstNonEmpty(req.Cwd, choice.WorkingDir, a.WorkspaceRoot), a.WorkspaceRoot)
	req.Path = resolveRelativeToolPath(req.Path, req.Cwd)
	for index := range req.Actions {
		req.Actions[index].Cwd = resolveRelativeToolPath(firstNonEmpty(req.Actions[index].Cwd, req.Cwd), a.WorkspaceRoot)
		req.Actions[index].Path = resolveRelativeToolPath(req.Actions[index].Path, req.Actions[index].Cwd)
	}
	req.Timeout = execution.Timeout

	executor := a.Glob
	if executor == nil {
		executor = globtool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"pattern":     result.Pattern,
		"path":        result.Root,
		"cwd":         req.Cwd,
		"match_count": strconv.Itoa(len(result.Matches)),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      globObservation(result, err),
		ResultCode:       resultCodeForError(err),
		WorkingDirectory: req.Cwd,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runGrepTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req greptool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.Intent = choice.Intent
	req.WorkingDir = firstNonEmpty(choice.WorkingDir, a.WorkspaceRoot)
	req.Timeout = execution.Timeout
	req.FallbackCandidates = execution.FallbackCandidates

	executor := a.Grep
	if executor == nil {
		executor = greptool.NewSafeExecutor(a.activityLogger())
	}
	result, err := executor.Run(ctx, req)
	code := strconv.Itoa(result.ExitCode)
	if err != nil && result.ExitCode == 0 && result.StartedAt.IsZero() {
		code = "1"
	}
	metadata := map[string]string{
		"grep_exit_status": strconv.Itoa(result.ExitCode),
	}
	if len(result.Actions) > 0 {
		metadata["action_count"] = strconv.Itoa(len(result.Actions))
	}
	if result.Duration > 0 {
		metadata["duration_ms"] = strconv.FormatInt(result.Duration.Milliseconds(), 10)
	}
	return toolRunResult{
		Observation:      grepObservation(result, err),
		ResultCode:       code,
		WorkingDirectory: result.WorkingDirectory,
		Metadata:         metadata,
	}, err
}

func (a *Activities) runScheduleTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (toolRunResult, error) {
	var req scheduletool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	req.ProjectID = execution.ProjectID
	req.WorkItemID = execution.WorkItemID
	req.ToolCallID = execution.ToolCallID
	req.Intent = choice.Intent
	req.SourceEvent = execution.SourceEvent

	executor := a.Schedule
	if executor == nil {
		return toolRunResult{ResultCode: "1"}, fmt.Errorf("schedule executor is not configured")
	}
	result, err := executor.Run(ctx, req)
	metadata := map[string]string{
		"workflow_schedule_operation": result.Operation,
		"workflow_id":                 result.WorkflowID,
		"schedule_id":                 result.ScheduleID,
		"workflow_name":               result.Name,
		"workflow_description":        result.Description,
		"workflow_commit_hash":        result.CommitHash,
		"workflow_path":               result.WorkflowPath,
		"schedule_time_zone":          result.TimeZone,
	}
	if result.OneShotAt != "" {
		metadata["one_shot_at"] = result.OneShotAt
	}
	if result.Cron != "" {
		metadata["cron"] = result.Cron
	}
	if len(result.NextActionTimes) > 0 {
		metadata["next_action_times"] = strings.Join(result.NextActionTimes, "\n")
	}
	return toolRunResult{
		Observation: result.Observation(),
		ResultCode:  resultCodeForError(err),
		Metadata:    metadata,
	}, err
}

func (a *Activities) runSkillTool(ctx context.Context, choice agent.ToolChoice) (toolRunResult, error) {
	var req skilltool.Request
	if err := decodeChoiceInput(choice, &req); err != nil {
		return toolRunResult{ResultCode: "1"}, err
	}
	executor := a.Skill
	if executor == nil {
		executor = skilltool.NewSafeExecutor(a.skillsRoots()...)
	}
	result, err := executor.Run(ctx, req)
	return toolRunResult{
		Observation: skillObservation(result, err),
		ResultCode:  resultCodeForError(err),
		Metadata: map[string]string{
			"skill_id":   result.SkillID,
			"skill_path": result.Path,
			"bytes_read": strconv.Itoa(result.BytesRead),
		},
	}, err
}

func decodeChoiceInput(choice agent.ToolChoice, target any) error {
	if len(strings.TrimSpace(string(choice.Input))) == 0 {
		return fmt.Errorf("%s tool input is required", choice.Type)
	}
	decoder := json.NewDecoder(strings.NewReader(string(choice.Input)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return fmt.Errorf("tool input contains multiple JSON values")
	case io.EOF:
		return nil
	default:
		return err
	}
}

type memoryToolRunResult struct {
	Observation string
	Payload     json.RawMessage
	Metadata    map[string]string
	Status      domain.ExecutionStatus
	ResultCode  string
	Error       string
}

func (a *Activities) runMemoryTool(ctx context.Context, choice agent.ToolChoice, execution toolExecutionContext) (memoryToolRunResult, error) {
	switch choice.Type {
	case domain.ToolTypeMemoryProposeAdd:
		var req memorytool.ProposeAddRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		if strings.TrimSpace(req.Content) == "" {
			err := fmt.Errorf("memory content is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		scope, err := memoryScopeForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memory := domain.Memory{
			ID:          stableActivityID("memory", execution.ProjectID, execution.ToolCallID, strings.TrimSpace(req.Content)),
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Scope:       scope,
			Kind:        firstNonEmpty(req.Kind, "fact"),
			Content:     strings.TrimSpace(req.Content),
			Tags:        req.Tags,
			Source:      "tool",
			SourceID:    execution.ToolCallID,
			Actor:       strings.TrimSpace(sourceEvent.ActorName),
			Confidence:  req.Confidence,
			Pinned:      req.Pinned,
			Metadata:    memoryMetadata(sourceEvent, req.Reason),
		}
		remembered, err := a.Store.RememberMemory(ctx, memory)
		if err != nil {
			if errors.Is(err, storage.ErrMemoryPolicyRejected) {
				return memoryPolicyRejectedResult(err), nil
			}
			return memoryToolRunResult{}, err
		}
		a.upsertMemoryEmbedding(ctx, remembered)
		payload := mustJSON(memorytool.ProposeAddResult{Memory: remembered})
		return memoryToolRunResult{
			Observation: memoryDetailObservation("Accepted memory add proposal.", remembered),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_id": remembered.ID,
				"scope":     string(remembered.Scope),
			},
		}, nil
	case domain.ToolTypeMemorySearch:
		var req memorytool.SearchRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		if strings.TrimSpace(sourceEvent.ProjectID) == "" {
			sourceEvent.ProjectID = execution.ProjectID
		}
		tags := cleanMemoryTags(req.Tags)
		scopes, err := memorySearchScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		query := strings.TrimSpace(req.Query)
		conversation, _, err := a.loadConversationContext(ctx, sourceEvent)
		if err != nil {
			a.logActivityStep(
				"Memory", "tool_search_conversation_context_failed",
				slog.String("project_id", execution.ProjectID),
				slog.String("tool_call_id", execution.ToolCallID),
				slog.String("error", err.Error()),
			)
			conversation = nil
		}
		memories, err := a.searchMemoriesWithEmbeddingQuery(ctx, domain.MemorySearchRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Query:       query,
			Scopes:      scopes,
			Tags:        tags,
			Limit:       req.Limit,
		}, prepareMemoryEmbeddingQuery(query, sourceEvent, nil, conversation))
		if err != nil {
			return memoryToolRunResult{}, err
		}
		payload := mustJSON(memorytool.SearchResult{Memories: memories})
		return memoryToolRunResult{
			Observation: memorySearchObservation(memories),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_count": strconv.Itoa(len(memories)),
				"tags":         strings.Join(tags, ", "),
			},
		}, nil
	case domain.ToolTypeMemoryList:
		var req memorytool.ListRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		tags := cleanMemoryTags(req.Tags)
		scopes, err := memorySearchScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memories, err := a.Store.ListMemories(ctx, domain.MemoryListRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			Scopes:      scopes,
			Kind:        strings.TrimSpace(req.Kind),
			Tags:        tags,
			Limit:       req.Limit,
		})
		if err != nil {
			return memoryToolRunResult{}, err
		}
		scope := strings.ToLower(strings.TrimSpace(req.Scope))
		if scope == "" {
			scope = memorytool.ScopeAll
		}
		payload := mustJSON(memorytool.ListResult{Memories: memories})
		return memoryToolRunResult{
			Observation: memoryListObservation(memories, scope, strings.TrimSpace(req.Kind), tags),
			Payload:     payload,
			Metadata: map[string]string{
				"memory_count": strconv.Itoa(len(memories)),
				"scope":        scope,
				"kind":         strings.TrimSpace(req.Kind),
				"tags":         strings.Join(tags, ", "),
			},
		}, nil
	case domain.ToolTypeMemoryProposeUpdate:
		var req memorytool.ProposeUpdateRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memoryID := strings.TrimSpace(req.MemoryID)
		if memoryID == "" {
			err := fmt.Errorf("memory_id is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		update := domain.MemoryUpdateRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			MemoryID:    memoryID,
		}
		hasUpdate := false
		if content := strings.TrimSpace(req.Content); content != "" {
			update.Content = content
			hasUpdate = true
		}
		if kind := strings.TrimSpace(req.Kind); kind != "" {
			update.Kind = kind
			hasUpdate = true
		}
		if scope := strings.TrimSpace(req.Scope); scope != "" {
			targetScope, err := memoryScope(scope)
			if err != nil {
				return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
			}
			update.Scope = targetScope
			hasUpdate = true
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.TagsMode)); mode {
		case "", "keep":
		case "replace":
			update.ReplaceTags = true
			update.Tags = cleanMemoryTags(req.Tags)
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported tags_mode %q", req.TagsMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.ConfidenceMode)); mode {
		case "", "keep":
		case "set":
			if req.Confidence < 0 || req.Confidence > 1 {
				err := fmt.Errorf("memory confidence must be between 0 and 1")
				return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
			}
			confidence := req.Confidence
			update.Confidence = &confidence
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported confidence_mode %q", req.ConfidenceMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		switch mode := strings.ToLower(strings.TrimSpace(req.PinnedMode)); mode {
		case "", "keep":
		case "set":
			pinned := req.Pinned
			update.Pinned = &pinned
			hasUpdate = true
		default:
			err := fmt.Errorf("unsupported pinned_mode %q", req.PinnedMode)
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		if !hasUpdate {
			err := fmt.Errorf("at least one memory update field is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		result, err := a.Store.UpdateMemory(ctx, update)
		if err != nil {
			if errors.Is(err, storage.ErrMemoryPolicyRejected) {
				return memoryPolicyRejectedResult(err), nil
			}
			return memoryToolRunResult{}, err
		}
		payload := mustJSON(memorytool.ProposeUpdateResult{Memory: result.Memory, Updated: result.Updated})
		observation := "Memory not found.\nmemory_id: " + memoryID
		metadata := map[string]string{
			"memory_id": memoryID,
			"updated":   strconv.FormatBool(result.Updated),
		}
		if result.Updated {
			if memoryUpdateAffectsEmbedding(update) {
				a.upsertMemoryEmbedding(ctx, result.Memory)
			}
			observation = memoryDetailObservation("Accepted memory update proposal.", result.Memory)
			metadata["scope"] = string(result.Memory.Scope)
			metadata["tags"] = strings.Join(result.Memory.Tags, ", ")
		}
		return memoryToolRunResult{
			Observation: observation,
			Payload:     payload,
			Metadata:    metadata,
		}, nil
	case domain.ToolTypeMemoryProposeForget:
		var req memorytool.ProposeForgetRequest
		if err := decodeChoiceInput(choice, &req); err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		memoryIDs := cleanMemoryIDs(req.MemoryIDs)
		tags := cleanMemoryTags(req.Tags)
		scope := strings.ToLower(strings.TrimSpace(req.Scope))
		if len(memoryIDs) == 0 && len(tags) == 0 && scope == "" {
			err := fmt.Errorf("memory_ids, tags, or scope is required")
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		sourceEvent := inferDiscordThreadContext(execution.SourceEvent)
		scopes, scope, err := memoryForgetScopesForEvent(req.Scope, sourceEvent)
		if err != nil {
			return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
		}
		result, err := a.Store.ForgetMemories(ctx, domain.MemoryForgetRequest{
			ProjectID:   execution.ProjectID,
			UserID:      eventUserID(sourceEvent),
			ChannelType: sourceEvent.ChannelType,
			ChannelID:   strings.TrimSpace(sourceEvent.ChannelID),
			ThreadID:    strings.TrimSpace(sourceEvent.ThreadID),
			MemoryIDs:   memoryIDs,
			Scopes:      scopes,
			Tags:        tags,
		})
		if err != nil {
			return memoryToolRunResult{}, err
		}
		deletedIDs := cleanMemoryIDs(result.DeletedMemoryIDs)
		notFoundIDs := missingMemoryIDs(memoryIDs, deletedIDs)
		deleted := len(deletedIDs) > 0
		payload := mustJSON(memorytool.ProposeForgetResult{
			MemoryIDs:         memoryIDs,
			Deleted:           deleted,
			DeletedCount:      len(deletedIDs),
			DeletedMemoryIDs:  deletedIDs,
			NotFoundMemoryIDs: notFoundIDs,
			Tags:              tags,
			Scope:             scope,
		})
		observation := memoryForgetObservation(memoryIDs, deletedIDs, notFoundIDs, tags, scope)
		return memoryToolRunResult{
			Observation: observation,
			Payload:     payload,
			Metadata: map[string]string{
				"memory_ids":    strings.Join(memoryIDs, ", "),
				"deleted":       strconv.FormatBool(deleted),
				"deleted_count": strconv.Itoa(len(deletedIDs)),
				"deleted_ids":   strings.Join(deletedIDs, ", "),
				"scope":         scope,
				"tags":          strings.Join(tags, ", "),
			},
		}, nil
	default:
		err := fmt.Errorf("unsupported memory tool type %q", choice.Type)
		return memoryToolRunResult{}, temporal.NewNonRetryableApplicationError(err.Error(), "InvalidMemoryToolRequest", err)
	}
}

func resultCodeForError(err error) string {
	if err != nil {
		return "1"
	}
	return "0"
}

func readObservation(result readtool.Result, err error) string {
	if len(result.Actions) > 0 {
		return readBatchObservation(result, err)
	}
	if err != nil {
		return fullObservation("", "", err)
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(
		&builder, "file: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		result.FilePath,
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	if result.Content != "" {
		builder.WriteString("\ncontent:\n")
		builder.WriteString(result.Content)
	}
	return builder.String()
}

func readBatchObservation(result readtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(
		&builder, "files: %d\nlines: %d/%d\nbytes: %d\ntruncated: %t",
		len(result.Actions),
		result.LinesRead,
		result.TotalLines,
		result.BytesRead,
		result.Truncated,
	)
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(
			&builder, "\n\nfile: %s\nlines: %d/%d\nbytes: %d\ntruncated: %t",
			action.FilePath,
			action.LinesRead,
			action.TotalLines,
			action.BytesRead,
			action.Truncated,
		)
		if action.Content != "" {
			builder.WriteString("\ncontent:\n")
			builder.WriteString(action.Content)
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
	}
	return builder.String()
}

func editObservation(result edittool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("edited: %s\nreplacements: %d\nbytes_written: %d", result.FilePath, result.Replacements, result.BytesWritten)
}

func writeObservation(result writetool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf("wrote: %s\nbytes_written: %d\noverwritten: %t", result.FilePath, result.BytesWritten, result.Overwritten)
}

func globObservation(result globtool.Result, err error) string {
	if len(result.Actions) > 0 {
		return globBatchObservation(result, err)
	}
	if err != nil {
		return fullObservation("", "", err)
	}
	if len(result.Matches) == 0 {
		return fmt.Sprintf("pattern: %s\npath: %s\nmatches: 0", result.Pattern, result.Root)
	}
	return fmt.Sprintf(
		"pattern: %s\npath: %s\nmatches: %d\n%s",
		result.Pattern,
		result.Root,
		len(result.Matches),
		strings.Join(result.Matches, "\n"),
	)
}

func globBatchObservation(result globtool.Result, err error) string {
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "patterns: %d\nmatches: %d", len(result.Actions), len(result.Matches))
	for _, action := range result.Actions {
		_, _ = fmt.Fprintf(
			&builder, "\n\npattern: %s\npath: %s\nmatches: %d",
			action.Pattern,
			action.Root,
			len(action.Matches),
		)
		if len(action.Matches) > 0 {
			builder.WriteString("\n")
			builder.WriteString(strings.Join(action.Matches, "\n"))
		}
	}
	if err != nil {
		builder.WriteString("\n\nerror:\n")
		builder.WriteString(err.Error())
	}
	return builder.String()
}

func grepObservation(result greptool.Result, err error) string {
	if err != nil {
		return fullObservation(result.Stdout, result.Stderr, err)
	}
	if strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" && result.ExitCode == 1 {
		return "No matches found."
	}
	return fullObservation(result.Stdout, result.Stderr, nil)
}

func skillObservation(result skilltool.Result, err error) string {
	if err != nil {
		return fullObservation("", "", err)
	}
	return fmt.Sprintf(
		"<skill_content name=%q>\n%s\n\nSkill directory: %s\nRelative paths in this skill are relative to the skill directory.\n</skill_content>",
		result.SkillID,
		strings.TrimSpace(result.Content),
		filepath.Dir(result.Path),
	)
}

func resolveRelativeToolPath(path, base string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if strings.TrimSpace(base) == "" {
		return path
	}
	return filepath.Join(base, path)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func newToolExecutionContext(request ExecuteToolRequest) (toolExecutionContext, error) {
	projectID := strings.TrimSpace(request.ProjectID)
	if projectID == "" {
		return toolExecutionContext{}, fmt.Errorf("project_id is required")
	}
	workItemID := strings.TrimSpace(request.WorkItemID)
	if workItemID == "" {
		return toolExecutionContext{}, fmt.Errorf("work_item_id is required")
	}
	toolCallID := strings.TrimSpace(request.ToolChoice.ToolCallID)
	if toolCallID == "" && request.ToolChoice.Metadata != nil {
		toolCallID = strings.TrimSpace(request.ToolChoice.Metadata["tool_call_id"])
	}
	if toolCallID == "" {
		return toolExecutionContext{}, fmt.Errorf("tool_call_id is required")
	}
	executionID := stableActivityID("execution-attempt", projectID, workItemID, toolCallID)
	invocationID := stableActivityID("tool-invocation", projectID, workItemID, toolCallID)
	return toolExecutionContext{
		ProjectID:          projectID,
		WorkItemID:         workItemID,
		ToolCallID:         toolCallID,
		SourceEvent:        request.Event,
		Cycle:              executionCycle(request.ToolChoice.Metadata),
		StartedAt:          time.Now().UTC(),
		ExecutionAttemptID: executionID,
		InvocationID:       invocationID,
		Timeout:            toolChoiceTimeout(request.ToolChoice),
		FallbackCandidates: toolregistry.FallbackCandidates(request.ToolChoice.Type),
	}, nil
}

func executionFeedback(result ExecuteToolResult) agent.ExecutionFeedback {
	metadata := cloneMetadata(result.Metadata)
	if result.WorkingDirectory != "" {
		metadata["working_directory"] = result.WorkingDirectory
	}
	if result.ResultCode != "" {
		metadata["result_code"] = result.ResultCode
	}
	if len(metadata) == 0 {
		metadata = nil
	}
	return agent.ExecutionFeedback{
		Cycle:           result.Cycle,
		WorkItemID:      result.WorkItemID,
		ToolCallID:      result.ToolCallID,
		Tool:            result.Tool,
		Status:          string(result.Status),
		RequestedAction: result.RequestedAction,
		Command:         result.Command,
		Args:            result.Args,
		Input:           cloneRawMessage(result.Input),
		Observation:     result.Observation,
		Error:           result.Error,
		Metadata:        metadata,
	}
}

func lastObservation(observations []agent.ExecutionFeedback) *agent.ExecutionFeedback {
	if len(observations) == 0 {
		return nil
	}
	return &observations[len(observations)-1]
}

func ensureNextAction(nextAction *agent.NextAction, projectID string, event domain.Event, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	summary := firstNonEmpty(strings.TrimSpace(event.Body), "Handle inbound request.")
	if len(nextAction.WorkItems) == 0 {
		workItemID := stableActivityID("work-item", projectID, event.ID, "1")
		nextAction.WorkItems = []domain.WorkItem{{
			ID:          workItemID,
			ProjectID:   projectID,
			Title:       "Handle request",
			Description: summary,
			Status:      domain.WorkItemStatusReady,
			CreatedAt:   now,
			UpdatedAt:   now,
		}}
		return nil
	}
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ProjectID == "" {
			nextAction.WorkItems[index].ProjectID = projectID
		}
		if nextAction.WorkItems[index].Status == "" {
			nextAction.WorkItems[index].Status = domain.WorkItemStatusReady
		}
		if nextAction.WorkItems[index].CreatedAt.IsZero() {
			nextAction.WorkItems[index].CreatedAt = now
		}
		if nextAction.WorkItems[index].UpdatedAt.IsZero() {
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	return nil
}

func ensureToolWorkItem(nextAction *agent.NextAction, workItemID string, now time.Time) error {
	if nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID) >= 0 {
		return nil
	}
	if strings.TrimSpace(workItemID) == "" {
		return fmt.Errorf("work item id is required")
	}
	item := domain.WorkItem{
		ID:          workItemID,
		ProjectID:   nextActionProjectID(*nextAction),
		Title:       "Handle request",
		Description: "Handle inbound request.",
		Status:      domain.WorkItemStatusReady,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	nextAction.WorkItems = append(nextAction.WorkItems, item)
	return nil
}

func nextActionProjectID(nextAction agent.NextAction) string {
	for _, item := range nextAction.WorkItems {
		if projectID := strings.TrimSpace(item.ProjectID); projectID != "" {
			return projectID
		}
	}
	return ""
}

func currentNextActionWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	if observation != nil {
		workItemID := strings.TrimSpace(observation.WorkItemID)
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, workItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			return workItemID
		}
	}
	for _, item := range nextAction.WorkItems {
		if item.Status != domain.WorkItemStatusCompleted {
			return item.ID
		}
	}
	return ""
}

func nextActionToolWorkItemID(nextAction agent.NextAction, observation *agent.ExecutionFeedback) string {
	return currentNextActionWorkItemID(nextAction, observation)
}

func completePreviousWorkItemForNextAction(nextAction *agent.NextAction, nextWorkItemID string, observation *agent.ExecutionFeedback, now time.Time) error {
	if observation == nil {
		return nil
	}
	if observation.Status != string(domain.ExecutionStatusSucceeded) || strings.TrimSpace(observation.Error) != "" {
		return nil
	}
	previousWorkItemID := strings.TrimSpace(observation.WorkItemID)
	if previousWorkItemID == "" || previousWorkItemID == strings.TrimSpace(nextWorkItemID) {
		return nil
	}
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, previousWorkItemID)
	if index < 0 {
		return fmt.Errorf("work item %q not found for status update", previousWorkItemID)
	}
	nextAction.WorkItems[index].Status = domain.WorkItemStatusCompleted
	nextAction.WorkItems[index].UpdatedAt = now
	return nil
}

func ensureToolChoiceMetadata(choice *agent.ToolChoice, workItemID string, cycle int, assistantText string) {
	if choice.Metadata == nil {
		choice.Metadata = map[string]string{}
	}
	toolCallID := strings.TrimSpace(choice.ToolCallID)
	if toolCallID == "" {
		toolCallID = strings.TrimSpace(choice.Metadata["tool_call_id"])
	}
	if toolCallID == "" {
		toolCallID = "toolu_" + stableActivityID("tool-call", workItemID, strconv.Itoa(cycle))
	}
	choice.ToolCallID = toolCallID
	choice.Metadata["tool_call_id"] = toolCallID
	choice.Metadata["work_item_id"] = workItemID
	choice.Metadata["execution_cycle"] = strconv.Itoa(cycle)
	if strings.TrimSpace(assistantText) != "" {
		choice.Metadata["assistant_text"] = strings.TrimSpace(assistantText)
	}
	if choice.WorkingDir != "" {
		choice.Metadata["working_directory"] = choice.WorkingDir
	}
	if choice.TimeoutMs > 0 {
		choice.Metadata["timeout_ms"] = strconv.Itoa(choice.TimeoutMs)
	}
	if choice.RunMode == "" {
		choice.RunMode = domain.ToolRunModeWaitForExit
	}
	if choice.Idempotency == "" {
		choice.Idempotency = domain.ToolIdempotencyUnknown
	}
	if choice.ProcessScope == "" {
		choice.ProcessScope = domain.ProcessScopeStopOnFinish
	}
	choice.Metadata["run_mode"] = string(choice.RunMode)
	choice.Metadata["idempotency"] = string(choice.Idempotency)
	choice.Metadata["process_scope"] = string(choice.ProcessScope)
}

func toolProcessScope(scope domain.ProcessScope) domain.ProcessScope {
	if scope == domain.ProcessScopeProject {
		return domain.ProcessScopeProject
	}
	return domain.ProcessScopeStopOnFinish
}

func markFinalNextActionWorkItems(nextAction *agent.NextAction, status domain.WorkItemStatus, observation *agent.ExecutionFeedback, now time.Time) {
	if status == "" {
		status = domain.WorkItemStatusCompleted
	}
	if observation != nil && strings.TrimSpace(observation.WorkItemID) != "" {
		index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
		if index >= 0 && nextAction.WorkItems[index].Status != domain.WorkItemStatusCompleted {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
		}
	}
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].Status == domain.WorkItemStatusCompleted {
			continue
		}
		nextAction.WorkItems[index].Status = status
		nextAction.WorkItems[index].UpdatedAt = now
	}
}

func terminalWorkItemStatus(status string) domain.WorkItemStatus {
	switch status {
	case NextActionStatusBlocked:
		return domain.WorkItemStatusBlocked
	case NextActionStatusFailed:
		return domain.WorkItemStatusFailed
	default:
		return domain.WorkItemStatusCompleted
	}
}

func cycleLimitResponseMessage(history []agent.ExecutionFeedback) string {
	if len(history) == 0 {
		return "Stopped after reaching the execution cycle limit before a response was produced."
	}

	var builder strings.Builder
	builder.WriteString("Stopped after reaching the execution cycle limit. Full execution history:")
	for _, feedback := range history {
		builder.WriteString("\n\n")
		builder.WriteString(fmt.Sprintf("cycle: %d", feedback.Cycle))
		if feedback.Tool != "" {
			builder.WriteString(fmt.Sprintf("\ntool: %s", feedback.Tool))
		}
		if feedback.Status != "" {
			builder.WriteString(fmt.Sprintf("\nstatus: %s", feedback.Status))
		}
		if feedback.RequestedAction != "" {
			builder.WriteString(fmt.Sprintf("\nrequested_action: %s", feedback.RequestedAction))
		}
		if feedback.Command != "" {
			builder.WriteString(fmt.Sprintf("\ncommand: %s", feedback.Command))
		}
		if len(feedback.Args) > 0 {
			builder.WriteString(fmt.Sprintf("\nargs: %s", strings.Join(feedback.Args, " ")))
		}
		if text := strings.TrimSpace(feedback.Observation); text != "" {
			builder.WriteString("\nobservation:\n")
			builder.WriteString(text)
		}
		if text := strings.TrimSpace(feedback.Error); text != "" {
			builder.WriteString("\nerror:\n")
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func applyObservationToNextAction(nextAction *agent.NextAction, observation agent.ExecutionFeedback, now time.Time) error {
	if nextAction == nil {
		return fmt.Errorf("next action is required")
	}
	if observation.WorkItemID == "" {
		return nil
	}
	status := domain.WorkItemStatusReady
	if observation.Status == string(domain.ExecutionStatusCanceled) {
		status = domain.WorkItemStatusBlocked
	}
	if err := setNextActionWorkItemStatus(nextAction, observation.WorkItemID, status, now); err != nil {
		return err
	}
	index := nextActionWorkItemIndexByID(nextAction.WorkItems, observation.WorkItemID)
	if index >= 0 {
		if nextAction.WorkItems[index].Metadata == nil {
			nextAction.WorkItems[index].Metadata = map[string]string{}
		}
		nextAction.WorkItems[index].Metadata["last_execution_status"] = observation.Status
		if code := strings.TrimSpace(observation.Metadata["result_code"]); code != "" {
			nextAction.WorkItems[index].Metadata["last_result_code"] = code
		}
	}
	return nil
}

func setNextActionWorkItemStatus(nextAction *agent.NextAction, workItemID string, status domain.WorkItemStatus, now time.Time) error {
	for index := range nextAction.WorkItems {
		if nextAction.WorkItems[index].ID == workItemID {
			nextAction.WorkItems[index].Status = status
			nextAction.WorkItems[index].UpdatedAt = now
			return nil
		}
	}
	return fmt.Errorf("work item %q not found for status update", workItemID)
}

func nextActionWorkItemIndexByID(items []domain.WorkItem, workItemID string) int {
	workItemID = strings.TrimSpace(workItemID)
	if workItemID == "" {
		return -1
	}
	for index, item := range items {
		if item.ID == workItemID {
			return index
		}
	}
	return -1
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

func fullObservation(stdout, stderr string, err error) string {
	stdout = strings.TrimSpace(textclean.TerminalOutput(stdout))
	stderr = strings.TrimSpace(textclean.TerminalOutput(stderr))
	var parts []string
	if stdout != "" {
		parts = append(parts, "stdout:\n"+stdout)
	}
	if stderr != "" {
		parts = append(parts, "stderr:\n"+stderr)
	}
	if err != nil {
		parts = append(parts, "error:\n"+strings.TrimSpace(textclean.TerminalOutput(err.Error())))
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n\n")
	}
	return "Execution completed."
}

func toolChoiceTimeout(choice agent.ToolChoice) time.Duration {
	if choice.TimeoutMs > 0 {
		return time.Duration(choice.TimeoutMs) * time.Millisecond
	}
	return 60 * time.Second
}

func (a *Activities) execGrace(timeout time.Duration) time.Duration {
	if a.ExecGrace > 0 {
		return a.ExecGrace
	}
	if timeout > 0 && timeout < 2*defaultExecGrace {
		return timeout / 2
	}
	return defaultExecGrace
}

func (a *Activities) execTailBytes() int64 {
	if a.ExecTailBytes > 0 {
		return a.ExecTailBytes
	}
	return defaultExecTailBytes
}

func (a *Activities) runtimeStateDir() string {
	stateDir, err := workspace.ResolveStateDir(a.StateDir, a.WorkspaceRoot)
	if err != nil {
		return ""
	}
	return stateDir
}

func (a *Activities) skillsRoots() []string {
	if strings.TrimSpace(a.SkillsRoot) != "" {
		return []string{a.SkillsRoot}
	}
	return skillcatalog.RuntimeRoots(a.WorkspaceRoot, a.OpenCTORoot)
}

func processStartObservation(process domain.ManagedProcess) string {
	if strings.TrimSpace(process.ID) == "" {
		return "Background process did not start."
	}
	var builder strings.Builder
	builder.WriteString("Started background process.")
	builder.WriteString("\nprocess_id: ")
	builder.WriteString(process.ID)
	if process.PID > 0 {
		builder.WriteString("\npid: ")
		builder.WriteString(strconv.Itoa(process.PID))
	}
	if process.PGID > 0 {
		builder.WriteString("\npgid: ")
		builder.WriteString(strconv.Itoa(process.PGID))
	}
	if process.StdoutLogPath != "" {
		builder.WriteString("\nstdout_log: ")
		builder.WriteString(process.StdoutLogPath)
	}
	if process.StderrLogPath != "" {
		builder.WriteString("\nstderr_log: ")
		builder.WriteString(process.StderrLogPath)
	}
	return builder.String()
}

func backgroundStartFailureObservation(ctx context.Context, manager *exectool.ProcessManager, stateDir string, process domain.ManagedProcess, runErr error) string {
	if manager == nil || strings.TrimSpace(process.ID) == "" {
		return fullObservation("", "", runErr)
	}
	logs, err := manager.Logs(ctx, stateDir, process.ID, 0)
	if err != nil {
		return fullObservation("", "", runErr)
	}
	return fullObservation(logs.StdoutTail, logs.StderrTail, runErr)
}

func buildRuntimeContext(workspaceRoot, openCTORoot string) agent.RuntimeContext {
	execPath := strings.TrimSpace(os.Getenv("SHELL"))
	now := time.Now()
	location, timeZone, timeZoneErr := scheduletool.ResolveHostTimeZone()
	localNow := now
	timeZoneError := ""
	if timeZoneErr != nil {
		timeZoneError = timeZoneErr.Error()
	} else if location != nil {
		localNow = now.In(location)
	}
	return agent.RuntimeContext{
		OS:                goruntime.GOOS,
		Arch:              goruntime.GOARCH,
		Exec:              execPath,
		Path:              os.Getenv("PATH"),
		WorkspaceRoot:     workspaceRoot,
		OpenCTORoot:       openCTORoot,
		CurrentLocalTime:  localNow.Format(time.RFC3339),
		CurrentUTCTime:    now.UTC().Format(time.RFC3339),
		HostTimeZone:      timeZone,
		HostTimeZoneError: timeZoneError,
	}
}

func workspaceEnvironment(workspaceRoot, openCTORoot string) map[string]string {
	env := map[string]string{}
	if workspaceRoot = strings.TrimSpace(workspaceRoot); workspaceRoot != "" {
		env[config.EnvOpenCTOWorkspace] = workspaceRoot
	}
	if openCTORoot = strings.TrimSpace(openCTORoot); openCTORoot != "" {
		env["OPENCTO_ROOT"] = openCTORoot
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

type workflowRunState struct {
	SchemaVersion      int                    `json:"schema_version"`
	ProjectID          string                 `json:"project_id"`
	WorkflowID         string                 `json:"workflow_id"`
	WorkflowName       string                 `json:"workflow_name,omitempty"`
	RunID              string                 `json:"run_id"`
	RunPath            string                 `json:"run_path"`
	Status             string                 `json:"status"`
	CommitHash         string                 `json:"commit_hash"`
	TemporalWorkflowID string                 `json:"temporal_workflow_id,omitempty"`
	ScheduleID         string                 `json:"schedule_id,omitempty"`
	SourceEventID      string                 `json:"source_event_id,omitempty"`
	Env                []string               `json:"env,omitempty"`
	ScheduledAt        time.Time              `json:"scheduled_at"`
	StartedAt          time.Time              `json:"started_at"`
	CompletedAt        *time.Time             `json:"completed_at,omitempty"`
	FailureSummary     string                 `json:"failure_summary,omitempty"`
	Steps              []workflowRunStepState `json:"steps"`
	UpdatedAt          time.Time              `json:"updated_at"`
}

type workflowRunStepState struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	Attempt       int        `json:"attempt,omitempty"`
	Command       string     `json:"command,omitempty"`
	Args          []string   `json:"args,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	ExitCode      int        `json:"exit_code,omitempty"`
	StdoutLogPath string     `json:"stdout_log_path,omitempty"`
	StderrLogPath string     `json:"stderr_log_path,omitempty"`
	OutputSummary string     `json:"output_summary,omitempty"`
	ErrorDetails  string     `json:"error_details,omitempty"`
}

func initialWorkflowRunState(input workflowrun.Input, manifest workflowbundle.Manifest, projectID, workflowID, runID, commitHash, temporalWorkflowID, runPath string, scheduledAt, startedAt time.Time) workflowRunState {
	steps := make([]workflowRunStepState, 0, len(manifest.Steps))
	for _, step := range manifest.Steps {
		stepID := strings.TrimSpace(step.ID)
		if normalized, err := workflowbundle.NormalizeStepID(stepID); err == nil {
			stepID = normalized
		}
		steps = append(steps, workflowRunStepState{
			ID:      stepID,
			Status:  string(domain.ExecutionStatusPending),
			Command: step.Command,
			Args:    append([]string(nil), step.Args...),
		})
	}
	now := time.Now().UTC()
	return workflowRunState{
		SchemaVersion:      1,
		ProjectID:          strings.TrimSpace(projectID),
		WorkflowID:         strings.TrimSpace(workflowID),
		WorkflowName:       strings.TrimSpace(manifest.Name),
		RunID:              strings.TrimSpace(runID),
		RunPath:            strings.TrimSpace(runPath),
		Status:             string(domain.ExecutionStatusRunning),
		CommitHash:         strings.TrimSpace(commitHash),
		TemporalWorkflowID: strings.TrimSpace(temporalWorkflowID),
		ScheduleID:         strings.TrimSpace(input.ScheduleID),
		SourceEventID:      strings.TrimSpace(input.SourceEvent.ID),
		Env:                append([]string(nil), manifest.Env...),
		ScheduledAt:        scheduledAt,
		StartedAt:          startedAt,
		Steps:              steps,
		UpdatedAt:          now,
	}
}

func upsertWorkflowStepState(runPath string, step workflowRunStepState) error {
	state, err := readWorkflowRunState(runPath)
	if err != nil {
		return err
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = 1
		state.Status = string(domain.ExecutionStatusRunning)
	}
	replaced := false
	for index := range state.Steps {
		if state.Steps[index].ID == step.ID {
			state.Steps[index] = step
			replaced = true
			break
		}
	}
	if !replaced {
		state.Steps = append(state.Steps, step)
	}
	state.UpdatedAt = time.Now().UTC()
	return writeWorkflowRunState(runPath, state)
}

func failWorkflowStepState(runPath, stepID string, attempt int, step workflowbundle.Step, stdoutPath, stderrPath string, startedAt time.Time, cause error) error {
	completedAt := time.Now().UTC()
	errorDetails := ""
	if cause != nil {
		errorDetails = cause.Error()
	}
	return upsertWorkflowStepState(runPath, workflowRunStepState{
		ID:            stepID,
		Status:        string(domain.ExecutionStatusFailed),
		Attempt:       attempt,
		Command:       step.Command,
		Args:          append([]string(nil), step.Args...),
		StartedAt:     &startedAt,
		CompletedAt:   &completedAt,
		ExitCode:      1,
		StdoutLogPath: stdoutPath,
		StderrLogPath: stderrPath,
		ErrorDetails:  errorDetails,
	})
}

func completeWorkflowRunState(runPath string, status domain.ExecutionStatus, completedAt time.Time, failureSummary string) error {
	state, err := readWorkflowRunState(runPath)
	if err != nil {
		return err
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = 1
	}
	state.Status = string(status)
	state.CompletedAt = &completedAt
	state.FailureSummary = strings.TrimSpace(failureSummary)
	state.UpdatedAt = time.Now().UTC()
	return writeWorkflowRunState(runPath, state)
}

func readWorkflowRunState(runPath string) (workflowRunState, error) {
	path := filepath.Join(strings.TrimSpace(runPath), "state.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return workflowRunState{}, nil
	}
	if err != nil {
		return workflowRunState{}, err
	}
	var state workflowRunState
	if err := json.Unmarshal(data, &state); err != nil {
		return workflowRunState{}, err
	}
	return state, nil
}

func writeWorkflowRunState(runPath string, state workflowRunState) error {
	runPath = strings.TrimSpace(runPath)
	if runPath == "" {
		return fmt.Errorf("run_path is required")
	}
	if err := os.MkdirAll(runPath, 0o755); err != nil {
		return err
	}
	state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := filepath.Join(runPath, "state.json")
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func activityAttempt(ctx context.Context) int {
	if activity.IsActivity(ctx) {
		info := activity.GetInfo(ctx)
		if info.Attempt > 0 {
			return int(info.Attempt)
		}
	}
	return 1
}

func workflowStepAttemptLogPaths(stepDir string, attempt int) (string, string) {
	if attempt <= 0 {
		attempt = 1
	}
	attemptDir := filepath.Join(stepDir, fmt.Sprintf("attempt-%d", attempt))
	return filepath.Join(attemptDir, "stdout.log"), filepath.Join(attemptDir, "stderr.log")
}

func copyWorkflowStepLog(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func workflowStepEnvironment(workspaceRoot string, required []string) ([]string, error) {
	env := os.Environ()
	if workspaceRoot = strings.TrimSpace(workspaceRoot); workspaceRoot != "" {
		env = append(env, config.EnvOpenCTOWorkspace+"="+workspaceRoot)
	}
	for _, name := range required {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if os.Getenv(name) == "" {
			return nil, fmt.Errorf("required env %s is not set", name)
		}
	}
	return env, nil
}

func tailWorkflowLog(path string, limit int64) (string, bool) {
	if limit <= 0 {
		limit = defaultExecTailBytes
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer file.Close()
	size := info.Size()
	offset := int64(0)
	if size > limit {
		offset = size - limit
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", false
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return "", false
	}
	return string(data), size > limit
}

func workflowStepSummary(stdout, stderr string, stdoutTruncated, stderrTruncated bool) string {
	var builder strings.Builder
	if stdout != "" {
		builder.WriteString("stdout:\n")
		builder.WriteString(stdout)
		if stdoutTruncated {
			builder.WriteString("\n[stdout truncated]")
		}
	}
	if stderr != "" {
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString("stderr:\n")
		builder.WriteString(stderr)
		if stderrTruncated {
			builder.WriteString("\n[stderr truncated]")
		}
	}
	if builder.Len() == 0 {
		return "step produced no output"
	}
	return builder.String()
}

type toolPersistenceRecordSet struct {
	Attempt      domain.ExecutionAttempt
	Invocation   domain.ToolInvocation
	Conversation domain.ConversationMessage
}

func toolPersistenceRecords(event domain.Event, result ExecuteToolResult) (toolPersistenceRecordSet, error) {
	projectID := firstNonEmpty(event.ProjectID, result.ExecutionAttempt.ProjectID, result.ToolInvocation.ProjectID)
	if projectID == "" {
		return toolPersistenceRecordSet{}, fmt.Errorf("project_id is required for tool persistence")
	}
	workItemID := firstNonEmpty(result.WorkItemID, result.ExecutionAttempt.WorkItemID)
	toolCallID := firstNonEmpty(result.ToolCallID, result.ToolInvocation.Metadata["tool_call_id"], result.ExecutionAttempt.Metadata["tool_call_id"])
	if toolCallID == "" {
		return toolPersistenceRecordSet{}, fmt.Errorf("tool_call_id is required for tool persistence")
	}
	now := time.Now().UTC()
	attempt := result.ExecutionAttempt
	if strings.TrimSpace(attempt.ID) == "" {
		startedAt := now
		if value := strings.TrimSpace(result.Metadata["started_at"]); value != "" {
			if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
				startedAt = parsed
			}
		}
		completedAt := now
		attempt = domain.ExecutionAttempt{
			ID:            stableActivityID("execution-attempt", projectID, workItemID, toolCallID),
			ProjectID:     projectID,
			WorkItemID:    workItemID,
			Status:        result.Status,
			Attempt:       result.Cycle,
			Tool:          result.Tool,
			Summary:       result.RequestedAction,
			OutputSummary: firstNonEmpty(result.Observation, result.Error),
			Metadata:      cloneMetadata(result.Metadata),
			StartedAt:     startedAt,
			CompletedAt:   &completedAt,
		}
	}
	if attempt.CompletedAt == nil && result.Status != domain.ExecutionStatusRunning {
		completedAt := now
		attempt.CompletedAt = &completedAt
	}
	if attempt.Metadata == nil {
		attempt.Metadata = cloneMetadata(result.Metadata)
	}
	invocation := result.ToolInvocation
	if strings.TrimSpace(invocation.ID) == "" {
		invocation = domain.ToolInvocation{
			ID:                 stableActivityID("tool-invocation", projectID, workItemID, toolCallID),
			ProjectID:          projectID,
			ExecutionAttemptID: attempt.ID,
			RequestedIntent:    result.RequestedAction,
			ChosenTool:         result.Tool,
			WorkingDirectory:   result.WorkingDirectory,
			InputPayload:       cloneRawMessage(result.Input),
			OutputSummary:      firstNonEmpty(result.Observation, result.Error),
			OutputPayload:      executeToolResultPayload(result),
			ResultCode:         result.ResultCode,
			ErrorDetails:       result.Error,
			Metadata:           cloneMetadata(result.Metadata),
			CreatedAt:          firstNonZeroTime(attempt.StartedAt, now),
			CompletedAt:        attempt.CompletedAt,
		}
	}
	if len(strings.TrimSpace(string(invocation.InputPayload))) == 0 {
		invocation.InputPayload = cloneRawMessage(result.Input)
	}
	if len(strings.TrimSpace(string(invocation.OutputPayload))) == 0 {
		invocation.OutputPayload = executeToolResultPayload(result)
	}
	if invocation.Metadata == nil {
		invocation.Metadata = cloneMetadata(result.Metadata)
	}
	if invocation.Metadata == nil {
		invocation.Metadata = domain.Metadata{}
	}
	invocation.Metadata["tool_call_id"] = toolCallID
	conversation := domain.ConversationMessage{
		ID:          stableActivityID("conversation-tool", projectID, event.ID, toolCallID),
		ProjectID:   projectID,
		EventID:     event.ID,
		Role:        domain.ConversationRoleTool,
		ChannelType: event.ChannelType,
		ChannelID:   strings.TrimSpace(event.ChannelID),
		ThreadID:    strings.TrimSpace(event.ThreadID),
		Body:        toolConversationBody(result),
		ToolCallID:  toolCallID,
		Metadata:    toolConversationMetadata(result),
		CreatedAt:   firstNonZeroTime(timeFromMetadata(result.Metadata, "completed_at"), now),
	}
	return toolPersistenceRecordSet{Attempt: attempt, Invocation: invocation, Conversation: conversation}, nil
}

func toolConversationMetadata(result ExecuteToolResult) domain.Metadata {
	metadata := domain.Metadata{}
	for key, value := range result.Metadata {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		metadata[key] = value
	}
	metadata["tool"] = string(result.Tool)
	metadata["status"] = string(result.Status)
	if code := strings.TrimSpace(result.ResultCode); code != "" {
		metadata["result_code"] = code
	}
	return metadata
}

func toolConversationBody(result ExecuteToolResult) string {
	var parts []string
	if result.RequestedAction != "" {
		parts = append(parts, "requested_action: "+result.RequestedAction)
	}
	if result.Observation != "" {
		parts = append(parts, "observation:\n"+result.Observation)
	}
	if result.Error != "" {
		parts = append(parts, "error:\n"+result.Error)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func executeToolResultPayload(result ExecuteToolResult) json.RawMessage {
	payload := struct {
		Cycle            int                       `json:"cycle"`
		WorkItemID       string                    `json:"work_item_id,omitempty"`
		ToolCallID       string                    `json:"tool_call_id,omitempty"`
		Tool             domain.ToolType           `json:"tool,omitempty"`
		Status           domain.ExecutionStatus    `json:"status"`
		RequestedAction  string                    `json:"requested_action,omitempty"`
		Command          string                    `json:"command,omitempty"`
		Args             []string                  `json:"args,omitempty"`
		Input            json.RawMessage           `json:"input,omitempty"`
		Observation      string                    `json:"observation,omitempty"`
		Error            string                    `json:"error,omitempty"`
		WorkingDirectory string                    `json:"working_directory,omitempty"`
		ResultCode       string                    `json:"result_code,omitempty"`
		Metadata         map[string]string         `json:"metadata,omitempty"`
		Processes        []domain.ProcessReference `json:"processes,omitempty"`
	}{
		Cycle:            result.Cycle,
		WorkItemID:       result.WorkItemID,
		ToolCallID:       result.ToolCallID,
		Tool:             result.Tool,
		Status:           result.Status,
		RequestedAction:  result.RequestedAction,
		Command:          result.Command,
		Args:             result.Args,
		Input:            cloneRawMessage(result.Input),
		Observation:      result.Observation,
		Error:            result.Error,
		WorkingDirectory: result.WorkingDirectory,
		ResultCode:       result.ResultCode,
		Metadata:         result.Metadata,
		Processes:        result.Processes,
	}
	return mustJSON(payload)
}

func memoryScope(value string) (domain.MemoryScope, error) {
	switch domain.MemoryScope(strings.ToLower(strings.TrimSpace(value))) {
	case domain.MemoryScopeThread:
		return domain.MemoryScopeThread, nil
	case domain.MemoryScopeChannel:
		return domain.MemoryScopeChannel, nil
	case domain.MemoryScopeGlobal:
		return domain.MemoryScopeGlobal, nil
	case domain.MemoryScopeUser:
		return domain.MemoryScopeUser, nil
	case domain.MemoryScopeProject:
		return domain.MemoryScopeProject, nil
	default:
		return "", fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memoryScopeForEvent(value string, event domain.Event) (domain.MemoryScope, error) {
	if strings.TrimSpace(value) == "" {
		event = inferDiscordThreadContext(event)
		if strings.TrimSpace(event.ThreadID) != "" {
			return domain.MemoryScopeThread, nil
		}
		if strings.TrimSpace(event.ChannelID) != "" {
			return domain.MemoryScopeChannel, nil
		}
		return domain.MemoryScopeProject, nil
	}
	return memoryScope(value)
}

func memorySearchScopes(value string) ([]domain.MemoryScope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case memorytool.ScopeThread:
		return []domain.MemoryScope{domain.MemoryScopeThread}, nil
	case memorytool.ScopeChannel:
		return []domain.MemoryScope{domain.MemoryScopeChannel}, nil
	case memorytool.ScopeProject:
		return []domain.MemoryScope{domain.MemoryScopeProject}, nil
	case memorytool.ScopeUser:
		return []domain.MemoryScope{domain.MemoryScopeUser}, nil
	case memorytool.ScopeGlobal:
		return []domain.MemoryScope{domain.MemoryScopeGlobal}, nil
	default:
		return nil, fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memorySearchScopesForEvent(value string, event domain.Event) ([]domain.MemoryScope, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return autoContextMemoryScopes(inferDiscordThreadContext(event)), nil
	}
	return memorySearchScopes(value)
}

func memoryForgetScopes(value string) ([]domain.MemoryScope, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return []domain.MemoryScope{domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal}, memorytool.ScopeAll, nil
	case memorytool.ScopeThread:
		return []domain.MemoryScope{domain.MemoryScopeThread}, memorytool.ScopeThread, nil
	case memorytool.ScopeChannel:
		return []domain.MemoryScope{domain.MemoryScopeChannel}, memorytool.ScopeChannel, nil
	case memorytool.ScopeProject:
		return []domain.MemoryScope{domain.MemoryScopeProject}, memorytool.ScopeProject, nil
	case memorytool.ScopeUser:
		return []domain.MemoryScope{domain.MemoryScopeUser}, memorytool.ScopeUser, nil
	case memorytool.ScopeGlobal:
		return []domain.MemoryScope{domain.MemoryScopeGlobal}, memorytool.ScopeGlobal, nil
	default:
		return nil, "", fmt.Errorf("unsupported memory scope %q", value)
	}
}

func memoryForgetScopesForEvent(value string, event domain.Event) ([]domain.MemoryScope, string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", memorytool.ScopeAll:
		return autoContextMemoryScopes(inferDiscordThreadContext(event)), memorytool.ScopeAll, nil
	}
	return memoryForgetScopes(value)
}

func (a *Activities) searchMemoriesWithEmbeddingQuery(ctx context.Context, request domain.MemorySearchRequest, embeddingQuery string) ([]domain.Memory, error) {
	if a.Store == nil {
		return nil, nil
	}
	embeddingQuery = strings.TrimSpace(embeddingQuery)
	if a.MemoryEmbedder != nil && embeddingQuery != "" {
		result, err := a.MemoryEmbedder.Embed(ctx, []string{embeddingQuery})
		if err != nil {
			a.logActivityStep("Memory", "embed_search_query_failed", slog.String("error", err.Error()))
		} else if len(result.Embeddings) > 0 && len(result.Embeddings[0]) > 0 {
			request.QueryEmbedding = result.Embeddings[0]
			request.EmbeddingProvider = a.MemoryEmbedder.Provider()
			request.EmbeddingModel = a.MemoryEmbedder.Model()
			request.EmbeddingDimensions = a.MemoryEmbedder.Dimensions()
		}
	}
	memories, err := a.Store.SearchMemories(ctx, request)
	if err == nil || len(request.QueryEmbedding) == 0 {
		return memories, err
	}
	a.logActivityStep(
		"Memory", "vector_search_failed",
		slog.String("error", err.Error()),
		slog.String("embedding_provider", request.EmbeddingProvider),
		slog.String("embedding_model", request.EmbeddingModel),
	)
	request.QueryEmbedding = nil
	request.EmbeddingProvider = ""
	request.EmbeddingModel = ""
	request.EmbeddingDimensions = 0
	return a.Store.SearchMemories(ctx, request)
}

func (a *Activities) upsertMemoryEmbedding(ctx context.Context, memory domain.Memory) {
	if a.Store == nil || a.MemoryEmbedder == nil {
		return
	}
	text := embedding.MemoryText(memory)
	if strings.TrimSpace(text) == "" {
		return
	}
	result, err := a.MemoryEmbedder.Embed(ctx, []string{text})
	if err != nil {
		a.logActivityStep(
			"Memory", "embed_memory_failed",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
			slog.String("error", err.Error()),
		)
		return
	}
	if len(result.Embeddings) == 0 || len(result.Embeddings[0]) == 0 {
		a.logActivityStep(
			"Memory", "embed_memory_empty",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
		)
		return
	}
	if err := a.Store.UpsertMemoryEmbedding(ctx, domain.MemoryEmbedding{
		MemoryID:    memory.ID,
		Provider:    a.MemoryEmbedder.Provider(),
		Model:       a.MemoryEmbedder.Model(),
		Dimensions:  a.MemoryEmbedder.Dimensions(),
		ContentHash: embedding.ContentHash(text),
		Vector:      result.Embeddings[0],
	}); err != nil {
		a.logActivityStep(
			"Memory", "upsert_memory_embedding_failed",
			slog.String("memory_id", strings.TrimSpace(memory.ID)),
			slog.String("error", err.Error()),
		)
	}
}

func memoryUpdateAffectsEmbedding(update domain.MemoryUpdateRequest) bool {
	return strings.TrimSpace(update.Content) != "" || strings.TrimSpace(update.Kind) != "" || update.ReplaceTags
}

func memoryPolicyRejectedResult(err error) memoryToolRunResult {
	reason := memoryPolicyRejectionReason(err)
	return memoryToolRunResult{
		Observation: "Memory rejected by policy: " + reason,
		Payload: mustJSON(map[string]any{
			"rejected": true,
			"reason":   reason,
		}),
		Metadata: map[string]string{
			"policy_rejected": "true",
			"reason":          reason,
		},
		Status:     domain.ExecutionStatusFailed,
		ResultCode: "policy_rejected",
		Error:      reason,
	}
}

func memoryPolicyRejectionReason(err error) string {
	if err == nil {
		return "memory rejected by policy"
	}
	reason := strings.TrimSpace(err.Error())
	reason = strings.TrimPrefix(reason, storage.ErrMemoryPolicyRejected.Error()+": ")
	if reason == "" {
		return "memory rejected by policy"
	}
	return reason
}

func memorySearchObservation(memories []domain.Memory) string {
	if len(memories) == 0 {
		return "No memories found."
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "Memory search results.\ncount: %d", len(memories))
	for i, memory := range memories {
		_, _ = fmt.Fprintf(&builder, "\n\n%d. ", i+1)
		writeMemoryObservationFields(&builder, memory)
	}
	return builder.String()
}

func memoryListObservation(memories []domain.Memory, scope, kind string, tags []string) string {
	if len(memories) == 0 {
		return "No memories found.\nscope: " + firstNonEmpty(scope, memorytool.ScopeAll)
	}
	var builder strings.Builder
	_, _ = fmt.Fprintf(&builder, "Memory list.\ncount: %d\nscope: %s", len(memories), firstNonEmpty(scope, memorytool.ScopeAll))
	if strings.TrimSpace(kind) != "" {
		builder.WriteString("\nkind: ")
		builder.WriteString(strings.TrimSpace(kind))
	}
	if len(tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(tags, ", "))
	}
	for i, memory := range memories {
		_, _ = fmt.Fprintf(&builder, "\n\n%d. ", i+1)
		writeMemoryObservationFields(&builder, memory)
	}
	return builder.String()
}

func memoryDetailObservation(title string, memory domain.Memory) string {
	var builder strings.Builder
	builder.WriteString(title)
	builder.WriteString("\n")
	writeMemoryObservationFields(&builder, memory)
	return builder.String()
}

func writeMemoryObservationFields(builder *strings.Builder, memory domain.Memory) {
	_, _ = fmt.Fprintf(
		builder, "memory_id: %s\nscope: %s\nkind: %s\nconfidence: %.2f\npinned: %t",
		strings.TrimSpace(memory.ID),
		memory.Scope,
		firstNonEmpty(memory.Kind, "fact"),
		memory.Confidence,
		memory.Pinned,
	)
	if !memory.UpdatedAt.IsZero() {
		builder.WriteString("\nupdated_at: ")
		builder.WriteString(memory.UpdatedAt.UTC().Format(time.RFC3339))
	}
	if strings.TrimSpace(memory.UserID) != "" {
		builder.WriteString("\nuser_id: ")
		builder.WriteString(strings.TrimSpace(memory.UserID))
	}
	if strings.TrimSpace(memory.ChannelID) != "" {
		builder.WriteString("\nchannel_type: ")
		builder.WriteString(string(memory.ChannelType))
		builder.WriteString("\nchannel_id: ")
		builder.WriteString(strings.TrimSpace(memory.ChannelID))
	}
	if strings.TrimSpace(memory.ThreadID) != "" {
		builder.WriteString("\nthread_id: ")
		builder.WriteString(strings.TrimSpace(memory.ThreadID))
	}
	if strings.TrimSpace(memory.Actor) != "" {
		builder.WriteString("\nactor: ")
		builder.WriteString(strings.TrimSpace(memory.Actor))
	}
	if len(memory.Tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(memory.Tags, ", "))
	}
	builder.WriteString("\ncontent:\n")
	builder.WriteString(strings.TrimSpace(memory.Content))
}

func memoryForgetObservation(memoryIDs, deletedIDs, notFoundIDs, tags []string, scope string) string {
	if len(memoryIDs) == 1 && len(tags) == 0 {
		if len(deletedIDs) > 0 {
			return "Forgot memory.\ndeleted_count: 1\nmemory_id: " + memoryIDs[0]
		}
		return "Memory not found.\nmemory_id: " + memoryIDs[0]
	}

	var builder strings.Builder
	if len(deletedIDs) > 0 {
		_, _ = fmt.Fprintf(&builder, "Forgot memories.\ndeleted_count: %d", len(deletedIDs))
		builder.WriteString("\ndeleted_memory_ids: ")
		builder.WriteString(strings.Join(deletedIDs, ", "))
	} else {
		builder.WriteString("No memories forgotten.")
	}
	if len(memoryIDs) > 0 {
		builder.WriteString("\nrequested_memory_ids: ")
		builder.WriteString(strings.Join(memoryIDs, ", "))
	}
	if len(notFoundIDs) > 0 {
		builder.WriteString("\nnot_found_memory_ids: ")
		builder.WriteString(strings.Join(notFoundIDs, ", "))
	}
	if len(tags) > 0 {
		builder.WriteString("\ntags: ")
		builder.WriteString(strings.Join(tags, ", "))
	}
	if strings.TrimSpace(scope) != "" {
		builder.WriteString("\nscope: ")
		builder.WriteString(scope)
	}
	return builder.String()
}

func cleanMemoryIDs(memoryIDs []string) []string {
	cleaned := make([]string, 0, len(memoryIDs))
	seen := map[string]bool{}
	for _, memoryID := range memoryIDs {
		memoryID = strings.TrimSpace(memoryID)
		if memoryID == "" || seen[memoryID] {
			continue
		}
		seen[memoryID] = true
		cleaned = append(cleaned, memoryID)
	}
	return cleaned
}

func cleanMemoryTags(tags []string) []string {
	cleaned := make([]string, 0, len(tags))
	seen := map[string]bool{}
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		cleaned = append(cleaned, tag)
	}
	sort.Strings(cleaned)
	return cleaned
}

func missingMemoryIDs(requested, deleted []string) []string {
	if len(requested) == 0 {
		return nil
	}
	deletedSet := map[string]bool{}
	for _, memoryID := range deleted {
		deletedSet[memoryID] = true
	}
	var missing []string
	for _, memoryID := range requested {
		if !deletedSet[memoryID] {
			missing = append(missing, memoryID)
		}
	}
	return missing
}

func shouldPersistNextActionConversation(status string) bool {
	switch status {
	case NextActionStatusCompleted, NextActionStatusBlocked, NextActionStatusFailed, NextActionStatusIgnored:
		return true
	default:
		return false
	}
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func timeFromMetadata(metadata map[string]string, key string) time.Time {
	value := strings.TrimSpace(metadata[key])
	if value == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func mustJSON(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func firstRawMessage(values ...json.RawMessage) json.RawMessage {
	for _, value := range values {
		if strings.TrimSpace(string(value)) != "" {
			return cloneRawMessage(value)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func cloneMetadata(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func stableActivityID(parts ...string) string {
	joined := strings.Join(parts, "\x00")
	sum := sha1.Sum([]byte(joined))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
