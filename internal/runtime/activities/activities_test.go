package activities

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
	"github.com/opencto/opencto/internal/runtime/workflowrun"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/storage"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	"github.com/opencto/opencto/internal/tools/postprocess"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	"github.com/opencto/opencto/internal/workflowbundle"
)

type stubProjectStore struct {
	pending              []domain.WorkItem
	memories             []domain.Memory
	remembered           *[]domain.Memory
	rememberErr          error
	searchRequests       *[]domain.MemorySearchRequest
	listRequests         *[]domain.MemoryListRequest
	updateResult         domain.MemoryUpdateResult
	updateRequests       *[]domain.MemoryUpdateRequest
	embeddingRequests    *[]domain.MemoryEmbedding
	forgetResult         domain.MemoryForgetResult
	forgetRequests       *[]domain.MemoryForgetRequest
	conversationsByScope map[storage.ConversationScope][]domain.ConversationMessage
	conversationQueries  *[]storage.ConversationQuery
	conversationThreads  map[string]domain.ConversationThread
	threadQueries        *[]storage.ConversationThreadQuery
	rootMessages         map[string]domain.ConversationMessage
	workflowRuns         map[string]domain.ScheduledWorkflowRun
	summariesByScope     map[domain.ConversationSummaryScope][]domain.ConversationSummary
	summaryQueries       *[]storage.ConversationSummaryQuery
	upsertedSummaries    *[]domain.ConversationSummary
	upsertedThreads      *[]domain.ConversationThread
	upsertedConversation *[]domain.ConversationMessage
}

type stubEngine struct {
	output  agent.NextActionOutput
	err     error
	input   *agent.NextActionInput
	session *agent.LLMSession
}

func (e stubEngine) NextAction(ctx context.Context, input agent.NextActionInput) (agent.NextActionOutput, error) {
	if e.input != nil {
		*e.input = input
	}
	if e.session != nil {
		*e.session = agent.LLMSessionFromContext(ctx)
	}
	return e.output, e.err
}

type sequenceEngine struct {
	outputs []agent.NextActionOutput
	inputs  []agent.NextActionInput
}

func (e *sequenceEngine) NextAction(_ context.Context, input agent.NextActionInput) (agent.NextActionOutput, error) {
	e.inputs = append(e.inputs, input)
	if len(e.outputs) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("unexpected NextAction call")
	}
	output := e.outputs[0]
	e.outputs = e.outputs[1:]
	return output, nil
}

type stubConversationCompressor struct {
	output agent.ConversationCompressionOutput
	err    error
	input  *agent.ConversationCompressionInput
}

func (c stubConversationCompressor) CompressConversation(_ context.Context, input agent.ConversationCompressionInput) (agent.ConversationCompressionOutput, error) {
	if c.input != nil {
		*c.input = input
	}
	return c.output, c.err
}

type stubAgentObservationCompressor struct {
	output agent.AgentObservationCompressionOutput
	err    error
	input  *agent.AgentObservationCompressionInput
}

func (c stubAgentObservationCompressor) CompressAgentObservations(_ context.Context, input agent.AgentObservationCompressionInput) (agent.AgentObservationCompressionOutput, error) {
	if c.input != nil {
		*c.input = input
	}
	return c.output, c.err
}

type fakeEmbedder struct {
	vector []float32
	inputs *[]string
}

func (e fakeEmbedder) Embed(_ context.Context, inputs []string) (embedding.Result, error) {
	if e.inputs != nil {
		*e.inputs = append(*e.inputs, inputs...)
	}
	vectors := make([][]float32, len(inputs))
	for i := range inputs {
		vectors[i] = append([]float32(nil), e.vector...)
	}
	return embedding.Result{
		Embeddings: vectors,
		Model:      e.Model(),
		Dimensions: e.Dimensions(),
	}, nil
}

func (e fakeEmbedder) Provider() string {
	return embedding.ProviderOpenAI
}

func (e fakeEmbedder) Model() string {
	return embedding.DefaultOpenAIModel
}

func (e fakeEmbedder) Dimensions() int {
	return len(e.vector)
}

type captureReporter struct {
	reports      []domain.ReportMessage
	messages     []string
	receipts     []domain.ReportReceipt
	typingEvents []domain.Event
	typingErr    error
	onTyping     func()
}

func (r *captureReporter) Report(_ context.Context, _ domain.Event, report domain.ReportMessage) ([]domain.ReportReceipt, error) {
	r.reports = append(r.reports, report)
	r.messages = append(r.messages, report.Text)
	return append([]domain.ReportReceipt(nil), r.receipts...), nil
}

func (r *captureReporter) NotifyTyping(_ context.Context, event domain.Event) error {
	r.typingEvents = append(r.typingEvents, event)
	if r.onTyping != nil {
		r.onTyping()
	}
	return r.typingErr
}

func (s stubProjectStore) Close() error {
	return nil
}

func (s stubProjectStore) Migrate(context.Context) error {
	return nil
}

func (s stubProjectStore) VerifySchema(context.Context) error {
	return nil
}

func (s stubProjectStore) EnsureProject(context.Context, domain.Project) error {
	return nil
}

func (s stubProjectStore) AppendEvent(context.Context, domain.Event) (storage.EventAppendResult, error) {
	return storage.EventAppendResult{Inserted: true}, nil
}

func (s stubProjectStore) ListPendingWorkItems(context.Context, string) ([]domain.WorkItem, error) {
	return append([]domain.WorkItem(nil), s.pending...), nil
}

func (s stubProjectStore) UpsertWorkItems(context.Context, []domain.WorkItem) error {
	return nil
}

func (s stubProjectStore) UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error {
	return nil
}

func (s stubProjectStore) UpsertToolInvocation(context.Context, domain.ToolInvocation) error {
	return nil
}

func (s stubProjectStore) UpsertConversationThread(_ context.Context, thread domain.ConversationThread) error {
	if s.upsertedThreads != nil {
		*s.upsertedThreads = append(*s.upsertedThreads, thread)
	}
	return nil
}

func (s stubProjectStore) GetConversationThread(_ context.Context, query storage.ConversationThreadQuery) (domain.ConversationThread, bool, error) {
	if s.threadQueries != nil {
		*s.threadQueries = append(*s.threadQueries, query)
	}
	for _, thread := range s.conversationThreads {
		if strings.TrimSpace(thread.ProjectID) == strings.TrimSpace(query.ProjectID) &&
			thread.ChannelType == query.ChannelType &&
			strings.TrimSpace(thread.ChannelID) == strings.TrimSpace(query.ChannelID) &&
			strings.TrimSpace(thread.ThreadID) == strings.TrimSpace(query.ThreadID) {
			return thread, true, nil
		}
	}
	return domain.ConversationThread{}, false, nil
}

func (s stubProjectStore) GetConversationRootMessage(_ context.Context, query storage.ConversationRootMessageQuery) (domain.ConversationMessage, bool, error) {
	message, ok := s.rootMessages[strings.TrimSpace(query.MessageID)]
	if !ok {
		return domain.ConversationMessage{}, false, nil
	}
	if strings.TrimSpace(message.ProjectID) != "" && strings.TrimSpace(message.ProjectID) != strings.TrimSpace(query.ProjectID) {
		return domain.ConversationMessage{}, false, nil
	}
	if message.ChannelType != "" && message.ChannelType != query.ChannelType {
		return domain.ConversationMessage{}, false, nil
	}
	if strings.TrimSpace(message.ChannelID) != "" && strings.TrimSpace(message.ChannelID) != strings.TrimSpace(query.ChannelID) {
		return domain.ConversationMessage{}, false, nil
	}
	return message, ok, nil
}

func (s stubProjectStore) UpsertConversationMessage(_ context.Context, message domain.ConversationMessage) error {
	if s.upsertedConversation != nil {
		*s.upsertedConversation = append(*s.upsertedConversation, message)
	}
	return nil
}

func (s stubProjectStore) ListConversationMessages(_ context.Context, query storage.ConversationQuery) ([]domain.ConversationMessage, error) {
	if s.conversationQueries != nil {
		*s.conversationQueries = append(*s.conversationQueries, query)
	}
	source := s.conversationsByScope[query.Scope]
	if len(source) == 0 {
		return nil, nil
	}
	filtered := make([]domain.ConversationMessage, 0, len(source))
	for _, message := range source {
		if strings.TrimSpace(message.ProjectID) != "" && strings.TrimSpace(message.ProjectID) != strings.TrimSpace(query.ProjectID) {
			continue
		}
		if message.ChannelType != "" && message.ChannelType != query.ChannelType {
			continue
		}
		if strings.TrimSpace(message.ChannelID) != "" && strings.TrimSpace(message.ChannelID) != strings.TrimSpace(query.ChannelID) {
			continue
		}
		if strings.TrimSpace(message.ThreadID) != "" && strings.TrimSpace(message.ThreadID) != strings.TrimSpace(query.ThreadID) {
			continue
		}
		if !query.AfterCreatedAt.IsZero() {
			if message.CreatedAt.Before(query.AfterCreatedAt) || (message.CreatedAt.Equal(query.AfterCreatedAt) && message.ID <= query.AfterID) {
				continue
			}
		}
		if !query.BeforeCreatedAt.IsZero() {
			if message.CreatedAt.After(query.BeforeCreatedAt) ||
				(strings.TrimSpace(query.BeforeID) != "" && message.CreatedAt.Equal(query.BeforeCreatedAt) && message.ID > query.BeforeID) {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	limit := storage.DefaultConversationHistoryLimit(query.Limit)
	if limit > len(filtered) {
		limit = len(filtered)
	}
	return append([]domain.ConversationMessage(nil), filtered[:limit]...), nil
}

func (s stubProjectStore) UpsertConversationSummary(_ context.Context, summary domain.ConversationSummary) error {
	if s.upsertedSummaries != nil {
		*s.upsertedSummaries = append(*s.upsertedSummaries, summary)
	}
	return nil
}

func (s stubProjectStore) ListConversationSummaries(_ context.Context, query storage.ConversationSummaryQuery) ([]domain.ConversationSummary, error) {
	if s.summaryQueries != nil {
		*s.summaryQueries = append(*s.summaryQueries, query)
	}
	source := s.summariesByScope[query.Scope]
	if len(source) == 0 {
		return nil, nil
	}
	filtered := make([]domain.ConversationSummary, 0, len(source))
	for _, summary := range source {
		if strings.TrimSpace(summary.ProjectID) != "" && strings.TrimSpace(summary.ProjectID) != strings.TrimSpace(query.ProjectID) {
			continue
		}
		if summary.ChannelType != "" && summary.ChannelType != query.ChannelType {
			continue
		}
		if strings.TrimSpace(summary.ChannelID) != "" && strings.TrimSpace(summary.ChannelID) != strings.TrimSpace(query.ChannelID) {
			continue
		}
		if strings.TrimSpace(summary.ThreadID) != "" && strings.TrimSpace(summary.ThreadID) != strings.TrimSpace(query.ThreadID) {
			continue
		}
		if !query.BeforeCreatedAt.IsZero() {
			if summary.ToCreatedAt.After(query.BeforeCreatedAt) ||
				(strings.TrimSpace(query.BeforeID) != "" && summary.ToCreatedAt.Equal(query.BeforeCreatedAt) && summary.ToMessageID > query.BeforeID) {
				continue
			}
		}
		filtered = append(filtered, summary)
	}
	limit := query.Limit
	if limit <= 0 || limit > len(filtered) {
		limit = len(filtered)
	}
	return append([]domain.ConversationSummary(nil), filtered[:limit]...), nil
}

func (s stubProjectStore) RememberMemory(_ context.Context, memory domain.Memory) (domain.Memory, error) {
	if s.remembered != nil {
		*s.remembered = append(*s.remembered, memory)
	}
	if s.rememberErr != nil {
		return domain.Memory{}, s.rememberErr
	}
	return memory, nil
}

func (s stubProjectStore) SearchMemories(_ context.Context, request domain.MemorySearchRequest) ([]domain.Memory, error) {
	if s.searchRequests != nil {
		*s.searchRequests = append(*s.searchRequests, request)
	}
	return append([]domain.Memory(nil), s.memories...), nil
}

func (s stubProjectStore) ListMemories(_ context.Context, request domain.MemoryListRequest) ([]domain.Memory, error) {
	if s.listRequests != nil {
		*s.listRequests = append(*s.listRequests, request)
	}
	return append([]domain.Memory(nil), s.memories...), nil
}

func (s stubProjectStore) UpdateMemory(_ context.Context, request domain.MemoryUpdateRequest) (domain.MemoryUpdateResult, error) {
	if s.updateRequests != nil {
		*s.updateRequests = append(*s.updateRequests, request)
	}
	return s.updateResult, nil
}

func (s stubProjectStore) UpsertMemoryEmbedding(_ context.Context, embedding domain.MemoryEmbedding) error {
	if s.embeddingRequests != nil {
		*s.embeddingRequests = append(*s.embeddingRequests, embedding)
	}
	return nil
}

func (s stubProjectStore) DeleteMemoryEmbeddings(context.Context, []string) error {
	return nil
}

func (s stubProjectStore) ForgetMemory(context.Context, string, string) (bool, error) {
	return false, nil
}

func (s stubProjectStore) ForgetMemories(_ context.Context, request domain.MemoryForgetRequest) (domain.MemoryForgetResult, error) {
	if s.forgetRequests != nil {
		*s.forgetRequests = append(*s.forgetRequests, request)
	}
	return s.forgetResult, nil
}

func (s stubProjectStore) UpsertScheduledWorkflow(context.Context, domain.ScheduledWorkflow) error {
	return nil
}

func (s stubProjectStore) GetScheduledWorkflow(context.Context, string, string) (domain.ScheduledWorkflow, bool, error) {
	return domain.ScheduledWorkflow{}, false, nil
}

func (s stubProjectStore) ListScheduledWorkflows(context.Context, storage.ScheduledWorkflowQuery) ([]domain.ScheduledWorkflow, error) {
	return nil, nil
}

func (s stubProjectStore) DeleteScheduledWorkflow(context.Context, string, string) error {
	return nil
}

func (s stubProjectStore) GetScheduledWorkflowRun(_ context.Context, projectID, runID string) (domain.ScheduledWorkflowRun, bool, error) {
	if s.workflowRuns == nil {
		return domain.ScheduledWorkflowRun{}, false, nil
	}
	run, ok := s.workflowRuns[projectID+"/"+runID]
	return run, ok, nil
}

func (s stubProjectStore) UpsertScheduledWorkflowRun(context.Context, domain.ScheduledWorkflowRun) error {
	return nil
}

func (s stubProjectStore) UpsertScheduledWorkflowStepRun(context.Context, domain.ScheduledWorkflowStepRun) error {
	return nil
}

func idsFromConversation(messages []domain.ConversationMessage) []string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestPersistEventCarriesControlMetadataToConversationMessage(t *testing.T) {
	t.Parallel()

	upserted := []domain.ConversationMessage{}
	activities := Activities{
		Store: stubProjectStore{upsertedConversation: &upserted},
	}
	err := activities.PersistEvent(context.Background(), PersistEventRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			Body:        "/stop",
			Metadata:    domain.Metadata{domain.MetadataKeyControl: "cancel"},
		},
	})
	if err != nil {
		t.Fatalf("persist event: %v", err)
	}
	if len(upserted) != 1 {
		t.Fatalf("expected one conversation message, got %#v", upserted)
	}
	if upserted[0].Metadata[domain.MetadataKeyControl] != "cancel" {
		t.Fatalf("expected control metadata to be carried, got %#v", upserted[0].Metadata)
	}
	if upserted[0].ChannelID != "channel-1" || upserted[0].ChannelType != domain.ChannelTypeDiscord {
		t.Fatalf("expected channel scope to be carried, got %#v", upserted[0])
	}
}

func TestPersistEventUpsertsConversationThread(t *testing.T) {
	t.Parallel()

	upsertedThreads := []domain.ConversationThread{}
	activities := Activities{
		Store: stubProjectStore{upsertedThreads: &upsertedThreads},
	}
	err := activities.PersistEvent(context.Background(), PersistEventRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "thread-1",
			ChannelType: domain.ChannelTypeDiscord,
			ThreadID:    "thread-1",
			Body:        "thread answer",
			CreatedAt:   time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("persist event: %v", err)
	}
	if len(upsertedThreads) != 1 {
		t.Fatalf("expected one thread upsert, got %#v", upsertedThreads)
	}
	thread := upsertedThreads[0]
	if thread.ProjectID != "project-1" || thread.ChannelID != "thread-1" || thread.ThreadID != "thread-1" || thread.EventID != "event-1" {
		t.Fatalf("unexpected thread upsert: %#v", thread)
	}
	if thread.LastMessageAt.IsZero() {
		t.Fatalf("expected last message time to be set: %#v", thread)
	}
}

func TestReportResponseIncludesAttachments(t *testing.T) {
	t.Parallel()

	reporter := &captureReporter{}
	activities := Activities{Reporter: reporter}
	result, err := activities.ReportResponse(context.Background(), ReportResponseRequest{
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
		},
		Message: "see attached",
		Attachments: []domain.ReportAttachment{{
			Path: "/workspace/screenshot.png",
		}},
	})
	if err != nil {
		t.Fatalf("report response: %v", err)
	}
	if len(result.Receipts) != 0 {
		t.Fatalf("unexpected receipts: %#v", result.Receipts)
	}
	if len(reporter.reports) != 1 {
		t.Fatalf("expected one report, got %d", len(reporter.reports))
	}
	report := reporter.reports[0]
	if report.Text != "see attached" || len(report.Attachments) != 1 || report.Attachments[0].Path != "/workspace/screenshot.png" {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestReportResponsePersistsNormalDiscordReportForFutureThread(t *testing.T) {
	t.Parallel()

	upserted := []domain.ConversationMessage{}
	upsertedThreads := []domain.ConversationThread{}
	reporter := &captureReporter{
		receipts: []domain.ReportReceipt{{
			MessageID: "bot-message-1",
			ChannelID: "channel-1",
			ContextID: "guild-1",
		}},
	}
	activities := Activities{
		Reporter: reporter,
		Store: stubProjectStore{
			upsertedConversation: &upserted,
			upsertedThreads:      &upsertedThreads,
		},
	}
	_, err := activities.ReportResponse(context.Background(), ReportResponseRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
		},
		Message: "Here is what I currently have saved in memory.",
	})
	if err != nil {
		t.Fatalf("report response: %v", err)
	}
	if len(upserted) != 1 {
		t.Fatalf("expected only the future-thread conversation row, got %#v", upserted)
	}
	got := map[string]domain.ConversationMessage{}
	for _, message := range upserted {
		got[message.ChannelID+"|"+message.ThreadID] = message
	}
	threadMessage, ok := got["channel-1|bot-message-1"]
	if !ok {
		t.Fatalf("expected normal report stored for the Discord message thread, got %#v", upserted)
	}
	if threadMessage.Role != domain.ConversationRoleAssistant ||
		threadMessage.Body != "Here is what I currently have saved in memory." ||
		threadMessage.Metadata["source"] != "report_response" ||
		threadMessage.Metadata["message_id"] != "bot-message-1" {
		t.Fatalf("unexpected future thread conversation row: %#v", threadMessage)
	}
	if len(upsertedThreads) != 1 ||
		upsertedThreads[0].ChannelID != "channel-1" ||
		upsertedThreads[0].ThreadID != "bot-message-1" ||
		upsertedThreads[0].RootMessageID != "bot-message-1" {
		t.Fatalf("expected future Discord thread ownership, got %#v", upsertedThreads)
	}
}

func TestReportResponsePersistsReceiptBodiesForSplitDiscordReport(t *testing.T) {
	t.Parallel()

	upserted := []domain.ConversationMessage{}
	reporter := &captureReporter{
		receipts: []domain.ReportReceipt{
			{MessageID: "bot-message-1", ChannelID: "channel-1", ContextID: "guild-1", Body: "first chunk"},
			{MessageID: "bot-message-2", ChannelID: "channel-1", ContextID: "guild-1", Body: "second chunk"},
		},
	}
	activities := Activities{
		Reporter: reporter,
		Store: stubProjectStore{
			upsertedConversation: &upserted,
		},
	}
	_, err := activities.ReportResponse(context.Background(), ReportResponseRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
		},
		Message: "first chunk\nsecond chunk",
	})
	if err != nil {
		t.Fatalf("report response: %v", err)
	}
	if len(upserted) != 2 {
		t.Fatalf("expected two future-thread rows, got %#v", upserted)
	}
	got := map[string]string{}
	for _, message := range upserted {
		got[message.ThreadID] = message.Body
	}
	if got["bot-message-1"] != "first chunk" || got["bot-message-2"] != "second chunk" {
		t.Fatalf("expected per-receipt chunk bodies, got %#v", upserted)
	}
}

func TestFullObservationKeepsLongStdout(t *testing.T) {
	stdout := strings.Repeat("file.go\n", 900)

	observation := fullObservation(stdout, "", nil)
	if observation != "stdout:\n"+strings.TrimSpace(stdout) {
		t.Fatalf("expected full stdout, got %q", observation)
	}
}

func TestFullObservationIncludesAllStreamsAndError(t *testing.T) {
	observation := fullObservation("command output", "command failed\nwith stderr", errors.New("exit status 1"))
	expected := "stdout:\ncommand output\n\nstderr:\ncommand failed\nwith stderr\n\nerror:\nexit status 1"
	if observation != expected {
		t.Fatalf("unexpected observation: %q", observation)
	}
}

func TestFullObservationCleansTerminalControls(t *testing.T) {
	observation := fullObservation(
		"vite build\x1b[2K\rtransforming...\x1b]0;ignored title\a done\x00",
		"warn: \x1b[31mred\x1b[0m",
		errors.New("exit\x1b[2K\rstatus 1"),
	)
	if strings.Contains(observation, "\x1b") ||
		strings.Contains(observation, "\r") ||
		strings.Contains(observation, "[2K") ||
		strings.Contains(observation, "ignored title") ||
		strings.Contains(observation, "\x00") {
		t.Fatalf("expected terminal controls to be removed, got %q", observation)
	}
	if !strings.Contains(observation, "vite build\ntransforming... done") ||
		!strings.Contains(observation, "warn: red") ||
		!strings.Contains(observation, "exit\nstatus 1") {
		t.Fatalf("expected readable output to remain, got %q", observation)
	}
}

func TestRuntimeStateDirUsesConfiguredWorkspace(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	got := (&Activities{WorkspaceRoot: workspaceRoot}).runtimeStateDir()
	want := filepath.Join(workspaceRoot, ".state")
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestLoadContextReturnsProjectAndActiveWorkItems(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 4, 23, 17, 31, 0, 0, time.UTC)
	skillsRoot := t.TempDir()
	skillDir := filepath.Join(skillsRoot, "go-testing")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, skillcatalog.SkillFileName), []byte("# Go Testing\n\nUse when testing Go code.\n"), 0o644); err != nil {
		t.Fatalf("write skill: %v", err)
	}
	event := domain.Event{
		ID:          "event-current",
		ProjectID:   "default",
		Kind:        domain.EventKindMessage,
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		ActorName:   "luka",
		Body:        "do it",
		CreatedAt:   base.Add(30 * time.Second),
	}

	workItem := domain.WorkItem{
		ID:        "work-item-1",
		ProjectID: "default",
		Title:     "Inspect workspace",
		Status:    domain.WorkItemStatusPending,
		CreatedAt: base,
		UpdatedAt: base,
	}

	activities := Activities{
		Store:      stubProjectStore{pending: []domain.WorkItem{workItem}},
		Project:    domain.Project{ID: "default", Name: "OpenCTO"},
		SkillsRoot: skillsRoot,
	}

	loaded, err := activities.LoadContext(context.Background(), event)
	if err != nil {
		t.Fatalf("load context: %v", err)
	}

	if loaded.Project.ID != "default" {
		t.Fatalf("expected project id to be carried through, got %q", loaded.Project.ID)
	}
	if len(loaded.ActiveWorkItems) != 1 {
		t.Fatalf("expected one active work item, got %d", len(loaded.ActiveWorkItems))
	}
	if loaded.ActiveWorkItems[0].ID != workItem.ID {
		t.Fatalf("unexpected work item id: %s", loaded.ActiveWorkItems[0].ID)
	}
	if len(loaded.Skills) != 1 || loaded.Skills[0].ID != "go-testing" {
		t.Fatalf("expected project skill to be discovered, got %#v", loaded.Skills)
	}
}

func TestLoadContextDiscoversWorkspaceAndBuiltInSkills(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	openCTORoot := t.TempDir()
	writeActivitySkill(t, filepath.Join(workspaceRoot, "skills"), "workspace-only", "# Workspace Only\n\nUse when workspace skills apply.\n")
	writeActivitySkill(t, filepath.Join(workspaceRoot, ".agents", "skills"), "agents-only", "# Agents Only\n\nUse when cross-client skills apply.\n")
	writeActivitySkill(t, filepath.Join(openCTORoot, "skills"), "built-in-only", "# Built In Only\n\nUse when built-in skills apply.\n")
	writeActivitySkill(t, filepath.Join(openCTORoot, "skills"), "shadowed", "# Built In Shadowed\n\nUse the built-in workflow.\n")
	writeActivitySkill(t, filepath.Join(workspaceRoot, "skills"), "shadowed", "# Workspace Shadowed\n\nUse the workspace workflow.\n")

	loaded, err := (&Activities{
		WorkspaceRoot: workspaceRoot,
		OpenCTORoot:   openCTORoot,
	}).LoadContext(context.Background(), domain.Event{
		ID:        "event-1",
		ProjectID: "default",
		Body:      "do it",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}

	got := map[string]skillcatalog.Summary{}
	for _, summary := range loaded.Skills {
		got[summary.ID] = summary
	}
	for _, id := range []string{"workspace-only", "agents-only", "built-in-only", "shadowed"} {
		if _, ok := got[id]; !ok {
			t.Fatalf("expected skill %q in %#v", id, loaded.Skills)
		}
	}
	if got["shadowed"].Name != "Workspace Shadowed" {
		t.Fatalf("expected workspace skill to shadow built-in skill, got %#v", got["shadowed"])
	}
}

func TestLoadContextIncludesBoundedMemoryWhenEnabled(t *testing.T) {
	t.Parallel()

	memory := domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "preference",
		Content:   "Use SQLite for local storage.",
	}
	var searchRequests []domain.MemorySearchRequest
	activities := Activities{
		Store:         stubProjectStore{memories: []domain.Memory{memory}, searchRequests: &searchRequests},
		Project:       domain.Project{ID: "default", Name: "OpenCTO"},
		MemoryEnabled: true,
		MemoryLimit:   5,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ActorID:     "user-1",
		Body:        "what storage should we use?",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(loaded.Memory) != 1 || loaded.Memory[0].ID != "memory-1" {
		t.Fatalf("expected memory to be loaded, got %#v", loaded.Memory)
	}
	if len(searchRequests) != 1 || searchRequests[0].UserID != "discord:user-1" {
		t.Fatalf("expected memory search to include current user id, got %#v", searchRequests)
	}
	if len(searchRequests[0].Scopes) != 3 || searchRequests[0].Scopes[1] != domain.MemoryScopeUser {
		t.Fatalf("expected memory search to include user scope, got %#v", searchRequests[0].Scopes)
	}
}

func TestPrepareMemoryEmbeddingQueryUsesRawQuery(t *testing.T) {
	t.Parallel()

	got := prepareMemoryEmbeddingQuery("  storage preference  ", domain.Event{}, nil, nil)
	if got != "Search memory for: storage preference" {
		t.Fatalf("unexpected prepared query:\n%s", got)
	}
}

func TestPrepareMemoryEmbeddingQueryIncludesFollowUpContext(t *testing.T) {
	t.Parallel()

	got := prepareMemoryEmbeddingQuery(
		"1",
		domain.Event{ID: "event-current", Body: "create react/vite app"},
		[]domain.Event{{ID: "event-additional", Body: "1"}},
		[]domain.ConversationMessage{
			{ID: "prior", EventID: "event-prior", Role: domain.ConversationRoleUser, Body: "Earlier request"},
			{ID: "prompt", EventID: "event-current", Role: domain.ConversationRoleAssistant, Body: "Where should I create it?"},
			{ID: "additional", EventID: "event-additional", Role: domain.ConversationRoleUser, Body: "1"},
		},
	)
	for _, want := range []string{
		"Search memory for: 1",
		"Current request: create react/vite app",
		"Follow-up: 1",
		"assistant: Where should I create it?",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected prepared query to contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "user: 1") {
		t.Fatalf("current/additional user events should not be duplicated in context:\n%s", got)
	}
}

func TestPrepareMemoryEmbeddingQueryRequiresCurrentAnchor(t *testing.T) {
	t.Parallel()

	got := prepareMemoryEmbeddingQuery("", domain.Event{}, nil, []domain.ConversationMessage{{
		ID:   "prior",
		Role: domain.ConversationRoleAssistant,
		Body: "Use SQLite for local state.",
	}})
	if got != "" {
		t.Fatalf("expected empty prepared query without current anchor, got:\n%s", got)
	}
}

func TestPrepareMemoryEmbeddingQueryIsBounded(t *testing.T) {
	t.Parallel()

	got := prepareMemoryEmbeddingQuery(strings.Repeat("x", memoryEmbeddingQueryMaxChars+500), domain.Event{}, nil, nil)
	if len([]rune(got)) > memoryEmbeddingQueryMaxChars {
		t.Fatalf("prepared query exceeded bound: %d > %d", len([]rune(got)), memoryEmbeddingQueryMaxChars)
	}
}

func TestPrepareMemoryEmbeddingQueryPreservesNewestContextWhenBounded(t *testing.T) {
	t.Parallel()

	got := prepareMemoryEmbeddingQuery(
		"1",
		domain.Event{ID: "event-current", Body: "create react/vite app"},
		nil,
		[]domain.ConversationMessage{
			{ID: "old", EventID: "event-old", Role: domain.ConversationRoleUser, Body: strings.Repeat("older context ", 300)},
			{ID: "prompt", EventID: "event-current", Role: domain.ConversationRoleAssistant, Body: "Where should I create it?"},
		},
	)
	if len([]rune(got)) > memoryEmbeddingQueryMaxChars {
		t.Fatalf("prepared query exceeded bound: %d > %d", len([]rune(got)), memoryEmbeddingQueryMaxChars)
	}
	if !strings.Contains(got, "assistant: Where should I create it?") {
		t.Fatalf("expected newest assistant context to survive bounded query:\n%s", got)
	}
}

func TestLoadContextEmbedsPreparedMemoryQueryFromConversation(t *testing.T) {
	t.Parallel()

	searchRequests := []domain.MemorySearchRequest{}
	embeddingInputs := []string{}
	vector := make([]float32, 1536)
	activities := Activities{
		Store: stubProjectStore{
			searchRequests: &searchRequests,
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{
						ID:          "prompt",
						ProjectID:   "project-1",
						EventID:     "event-1",
						Role:        domain.ConversationRoleAssistant,
						ChannelType: domain.ChannelTypeDiscord,
						ChannelID:   "bot-prompt-1",
						ThreadID:    "bot-prompt-1",
						Body:        "Where should I create it?",
					},
					{
						ID:          "additional",
						ProjectID:   "project-1",
						EventID:     "event-2",
						Role:        domain.ConversationRoleUser,
						ChannelType: domain.ChannelTypeDiscord,
						ChannelID:   "bot-prompt-1",
						ThreadID:    "bot-prompt-1",
						Body:        "1",
					},
				},
			},
		},
		MemoryEnabled:       true,
		MemoryEmbedder:      fakeEmbedder{vector: vector, inputs: &embeddingInputs},
		ConversationEnabled: true,
	}
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "create react/vite app",
	}
	followUp := domain.Event{
		ID:          "event-2",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "bot-prompt-1",
		Body:        "1",
		Metadata:    domain.Metadata{domain.MetadataKeyControl: domain.MetadataControlTaskReply},
	}

	_, err := activities.loadContext(context.Background(), event, followUp, []domain.Event{followUp})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(searchRequests) != 1 || searchRequests[0].Query != "1" {
		t.Fatalf("expected raw query to remain on search request, got %#v", searchRequests)
	}
	if len(embeddingInputs) != 1 {
		t.Fatalf("expected one embedding input, got %#v", embeddingInputs)
	}
	input := embeddingInputs[0]
	for _, want := range []string{
		"Search memory for: 1",
		"Current request: create react/vite app",
		"Follow-up: 1",
		"assistant: Where should I create it?",
	} {
		if !strings.Contains(input, want) {
			t.Fatalf("expected embedding input to contain %q:\n%s", want, input)
		}
	}
	if strings.Contains(input, "user: 1") {
		t.Fatalf("additional user event should not be duplicated in embedding context:\n%s", input)
	}
}

func TestLoadContextIncludesSharedMemoryInChannel(t *testing.T) {
	t.Parallel()

	var searchRequests []domain.MemorySearchRequest
	activities := Activities{
		Store:         stubProjectStore{searchRequests: &searchRequests},
		Project:       domain.Project{ID: "default", Name: "OpenCTO"},
		MemoryEnabled: true,
		MemoryLimit:   5,
	}
	_, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ActorID:     "user-1",
		Body:        "continue here",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(searchRequests) != 1 {
		t.Fatalf("expected one memory search, got %#v", searchRequests)
	}
	request := searchRequests[0]
	if request.ChannelID != "channel-1" ||
		request.ThreadID != "" ||
		len(request.Scopes) != 4 ||
		request.Scopes[0] != domain.MemoryScopeChannel ||
		request.Scopes[1] != domain.MemoryScopeProject ||
		request.Scopes[2] != domain.MemoryScopeUser ||
		request.Scopes[3] != domain.MemoryScopeGlobal {
		t.Fatalf("expected channel plus shared memory search, got %#v", request)
	}
}

func TestLoadContextIncludesSharedMemoryInThread(t *testing.T) {
	t.Parallel()

	var searchRequests []domain.MemorySearchRequest
	activities := Activities{
		Store:         stubProjectStore{searchRequests: &searchRequests},
		Project:       domain.Project{ID: "default", Name: "OpenCTO"},
		MemoryEnabled: true,
		MemoryLimit:   5,
	}
	_, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "thread-1",
		ThreadID:    "thread-1",
		ActorID:     "user-1",
		Body:        "continue here",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(searchRequests) != 1 {
		t.Fatalf("expected one memory search, got %#v", searchRequests)
	}
	request := searchRequests[0]
	if request.ChannelID != "thread-1" ||
		request.ThreadID != "thread-1" ||
		len(request.Scopes) != 5 ||
		request.Scopes[0] != domain.MemoryScopeThread ||
		request.Scopes[1] != domain.MemoryScopeChannel ||
		request.Scopes[2] != domain.MemoryScopeProject ||
		request.Scopes[3] != domain.MemoryScopeUser ||
		request.Scopes[4] != domain.MemoryScopeGlobal {
		t.Fatalf("expected thread, channel, and shared memory search, got %#v", request)
	}
}

func TestLoadContextExcludesMemoryFromCurrentEvent(t *testing.T) {
	t.Parallel()

	memories := []domain.Memory{
		{
			ID:        "current-event-memory",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Current event memory should not echo into the same turn.",
			SourceID:  "event-1",
		},
		{
			ID:        "older-memory",
			ProjectID: "default",
			Scope:     domain.MemoryScopeProject,
			Kind:      "preference",
			Content:   "Older memory remains available.",
			SourceID:  "event-0",
		},
	}
	activities := Activities{
		Store:         stubProjectStore{memories: memories},
		Project:       domain.Project{ID: "default", Name: "OpenCTO"},
		MemoryEnabled: true,
		MemoryLimit:   5,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ActorID:     "user-1",
		Body:        "current user message",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(loaded.Memory) != 1 || loaded.Memory[0].ID != "older-memory" {
		t.Fatalf("expected same-event memory to be excluded, got %#v", loaded.Memory)
	}
}

func TestExecuteMemoryToolForgetsByMemoryIDs(t *testing.T) {
	t.Parallel()

	var forgetRequests []domain.MemoryForgetRequest
	activities := Activities{
		Store: stubProjectStore{
			forgetResult:   domain.MemoryForgetResult{DeletedMemoryIDs: []string{"memory-1", "memory-2"}},
			forgetRequests: &forgetRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeForget,
			Intent:     "forget cleanup memories",
			Input:      []byte(`{"memory_ids":["memory-1","memory-2","memory-2",""],"tags":[],"scope":"all"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory forget: %v", err)
	}
	if len(forgetRequests) != 1 {
		t.Fatalf("expected one forget request, got %d", len(forgetRequests))
	}
	request := forgetRequests[0]
	if strings.Join(request.MemoryIDs, ",") != "memory-1,memory-2" {
		t.Fatalf("unexpected forget memory ids: %#v", request.MemoryIDs)
	}
	if len(request.Scopes) != 3 || request.Scopes[0] != domain.MemoryScopeProject || request.Scopes[1] != domain.MemoryScopeUser || request.Scopes[2] != domain.MemoryScopeGlobal {
		t.Fatalf("unexpected forget scopes: %#v", request.Scopes)
	}
	if len(request.Tags) != 0 {
		t.Fatalf("unexpected forget tags: %#v", request.Tags)
	}
	if result.Metadata["deleted_count"] != "2" || !strings.Contains(result.Observation, "deleted_count: 2") {
		t.Fatalf("unexpected forget result: %#v", result)
	}
}

func TestExecuteMemoryToolForgetsByCombinedFilters(t *testing.T) {
	t.Parallel()

	var forgetRequests []domain.MemoryForgetRequest
	activities := Activities{
		Store: stubProjectStore{
			forgetResult:   domain.MemoryForgetResult{DeletedMemoryIDs: []string{"memory-1"}},
			forgetRequests: &forgetRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeForget,
			Intent:     "forget cleanup memories",
			Input:      []byte(`{"memory_ids":["memory-1"],"tags":["Cleanup"],"scope":"project"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute combined memory forget: %v", err)
	}
	if len(forgetRequests) != 1 {
		t.Fatalf("expected one forget request, got %d", len(forgetRequests))
	}
	request := forgetRequests[0]
	if strings.Join(request.MemoryIDs, ",") != "memory-1" {
		t.Fatalf("unexpected forget memory ids: %#v", request.MemoryIDs)
	}
	if strings.Join(request.Tags, ",") != "cleanup" {
		t.Fatalf("unexpected forget tags: %#v", request.Tags)
	}
	if len(request.Scopes) != 1 || request.Scopes[0] != domain.MemoryScopeProject {
		t.Fatalf("unexpected forget scopes: %#v", request.Scopes)
	}
	if result.Metadata["deleted_count"] != "1" {
		t.Fatalf("unexpected forget result: %#v", result)
	}
}

func TestExecuteMemoryToolListsCurrentUserMemories(t *testing.T) {
	t.Parallel()

	var listRequests []domain.MemoryListRequest
	activities := Activities{
		Store: stubProjectStore{
			memories: []domain.Memory{{
				ID:         "memory-1",
				ProjectID:  "default",
				UserID:     "discord:user-1",
				Scope:      domain.MemoryScopeUser,
				Kind:       "preference",
				Content:    "User prefers concise technical explanations.",
				Tags:       []string{"communication"},
				Confidence: 1,
			}},
			listRequests: &listRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ActorID:     "user-1",
			ActorName:   "luka",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryList,
			Intent:     "list user memory",
			Input:      []byte(`{"scope":"user","kind":"preference","tags":["communication"],"limit":10}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory list: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded || !strings.Contains(result.Observation, "Memory list.") || result.Metadata["memory_count"] != "1" {
		t.Fatalf("unexpected memory list result: %#v", result)
	}
	if len(listRequests) != 1 || listRequests[0].UserID != "discord:user-1" || listRequests[0].Scopes[0] != domain.MemoryScopeUser {
		t.Fatalf("unexpected list request: %#v", listRequests)
	}
}

func TestExecuteMemoryToolRejectsInvalidSearchScope(t *testing.T) {
	t.Parallel()

	var searchRequests []domain.MemorySearchRequest
	activities := Activities{
		Store:         stubProjectStore{searchRequests: &searchRequests},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemorySearch,
			Intent:     "search memory",
			Input:      []byte(`{"scope":"workspace","query":"storage","tags":[],"limit":10}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported memory scope "workspace"`) {
		t.Fatalf("expected invalid scope error, got result=%#v err=%v", result, err)
	}
	if result.Status != domain.ExecutionStatusFailed || len(searchRequests) != 0 {
		t.Fatalf("expected failed result without search request, got result=%#v requests=%#v", result, searchRequests)
	}
}

func TestExecuteMemoryToolSearchEmbedsPreparedQuery(t *testing.T) {
	t.Parallel()

	searchRequests := []domain.MemorySearchRequest{}
	embeddingInputs := []string{}
	vector := make([]float32, 1536)
	activities := Activities{
		Store: stubProjectStore{
			searchRequests: &searchRequests,
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeChannel: {
					{
						ID:          "assistant-context",
						ProjectID:   "default",
						EventID:     "event-prior",
						Role:        domain.ConversationRoleAssistant,
						ChannelType: domain.ChannelTypeDiscord,
						ChannelID:   "channel-1",
						Body:        "Use SQLite for local state.",
					},
				},
			},
		},
		MemoryEnabled:       true,
		MemoryEmbedder:      fakeEmbedder{vector: vector, inputs: &embeddingInputs},
		ConversationEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Body:        "make that the default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemorySearch,
			Intent:     "search memory",
			Input:      []byte(`{"scope":"all","query":"that default","tags":[],"limit":5}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory search: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("unexpected memory search result: %#v", result)
	}
	if len(searchRequests) != 1 || searchRequests[0].Query != "that default" {
		t.Fatalf("expected raw query to remain on search request, got %#v", searchRequests)
	}
	if len(embeddingInputs) != 1 ||
		!strings.Contains(embeddingInputs[0], "Search memory for: that default") ||
		!strings.Contains(embeddingInputs[0], "Current request: make that the default") ||
		!strings.Contains(embeddingInputs[0], "assistant: Use SQLite for local state.") {
		t.Fatalf("unexpected embedding inputs: %#v", embeddingInputs)
	}
}

func TestExecuteMemoryToolRejectsInvalidListScope(t *testing.T) {
	t.Parallel()

	var listRequests []domain.MemoryListRequest
	activities := Activities{
		Store:         stubProjectStore{listRequests: &listRequests},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryList,
			Intent:     "list memory",
			Input:      []byte(`{"scope":"workspace","kind":"","tags":[],"limit":10}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported memory scope "workspace"`) {
		t.Fatalf("expected invalid scope error, got result=%#v err=%v", result, err)
	}
	if result.Status != domain.ExecutionStatusFailed || len(listRequests) != 0 {
		t.Fatalf("expected failed result without list request, got result=%#v requests=%#v", result, listRequests)
	}
}

func TestExecuteMemoryToolRejectsInvalidAddScope(t *testing.T) {
	t.Parallel()

	remembered := []domain.Memory{}
	activities := Activities{
		Store:         stubProjectStore{remembered: &remembered},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeAdd,
			Intent:     "remember memory",
			Input:      []byte(`{"content":"Use SQLite for local state.","scope":"workspace","kind":"fact","tags":[],"confidence":1,"pinned":false,"reason":"test"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), `unsupported memory scope "workspace"`) {
		t.Fatalf("expected invalid scope error, got result=%#v err=%v", result, err)
	}
	if result.Status != domain.ExecutionStatusFailed || len(remembered) != 0 {
		t.Fatalf("expected failed result without remembered memory, got result=%#v remembered=%#v", result, remembered)
	}
}

func TestExecuteMemoryToolDefaultsToThreadScopeInThread(t *testing.T) {
	t.Parallel()

	var listRequests []domain.MemoryListRequest
	activities := Activities{
		Store:         stubProjectStore{listRequests: &listRequests},
		MemoryEnabled: true,
	}
	_, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "thread-1",
			ThreadID:    "thread-1",
			ActorID:     "user-1",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryList,
			Intent:     "list memory",
			Input:      []byte(`{"scope":"all","kind":"","tags":[],"limit":10}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory list: %v", err)
	}
	if len(listRequests) != 1 {
		t.Fatalf("expected one list request, got %#v", listRequests)
	}
	request := listRequests[0]
	if request.ChannelID != "thread-1" ||
		request.ThreadID != "thread-1" ||
		len(request.Scopes) != 5 ||
		request.Scopes[0] != domain.MemoryScopeThread ||
		request.Scopes[1] != domain.MemoryScopeChannel ||
		request.Scopes[2] != domain.MemoryScopeProject ||
		request.Scopes[3] != domain.MemoryScopeUser ||
		request.Scopes[4] != domain.MemoryScopeGlobal {
		t.Fatalf("expected thread list request with shared scopes, got %#v", request)
	}
}

func TestExecuteMemoryToolUpdatesMemory(t *testing.T) {
	t.Parallel()

	var updateRequests []domain.MemoryUpdateRequest
	activities := Activities{
		Store: stubProjectStore{
			updateResult: domain.MemoryUpdateResult{
				Updated: true,
				Memory: domain.Memory{
					ID:         "memory-1",
					ProjectID:  "default",
					Scope:      domain.MemoryScopeProject,
					Kind:       "preference",
					Content:    "Use SQLite for durable local state.",
					Tags:       []string{"storage", "sqlite"},
					Confidence: 0.8,
					Pinned:     true,
					UpdatedAt:  time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC),
				},
			},
			updateRequests: &updateRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeUpdate,
			Intent:     "update stale memory",
			Input:      []byte(`{"memory_id":"memory-1","content":"Use SQLite for durable local state.","kind":"preference","tags_mode":"replace","tags":["SQLite","storage"],"confidence_mode":"set","confidence":0.8,"pinned_mode":"set","pinned":true,"reason":"newer decision"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory update: %v", err)
	}
	if len(updateRequests) != 1 {
		t.Fatalf("expected one update request, got %d", len(updateRequests))
	}
	request := updateRequests[0]
	if request.ProjectID != "default" || request.MemoryID != "memory-1" {
		t.Fatalf("unexpected update request target: %#v", request)
	}
	if request.Content != "Use SQLite for durable local state." || request.Kind != "preference" {
		t.Fatalf("unexpected content update: %#v", request)
	}
	if !request.ReplaceTags || strings.Join(request.Tags, ",") != "sqlite,storage" {
		t.Fatalf("unexpected tag update: %#v", request)
	}
	if request.Confidence == nil || *request.Confidence != 0.8 {
		t.Fatalf("unexpected confidence update: %#v", request.Confidence)
	}
	if request.Pinned == nil || !*request.Pinned {
		t.Fatalf("unexpected pinned update: %#v", request.Pinned)
	}
	if result.Metadata["updated"] != "true" || !strings.Contains(result.Observation, "Accepted memory update proposal.") {
		t.Fatalf("unexpected update result: %#v", result)
	}
	if !strings.Contains(result.Observation, "memory_id: memory-1") || !strings.Contains(result.Observation, "confidence: 0.80") {
		t.Fatalf("expected formatted memory observation, got %q", result.Observation)
	}
}

func TestExecuteMemoryToolUpdatesZeroConfidenceAndUnpins(t *testing.T) {
	t.Parallel()

	var updateRequests []domain.MemoryUpdateRequest
	activities := Activities{
		Store: stubProjectStore{
			updateResult: domain.MemoryUpdateResult{
				Updated: true,
				Memory: domain.Memory{
					ID:         "memory-1",
					ProjectID:  "default",
					Scope:      domain.MemoryScopeProject,
					Kind:       "preference",
					Content:    "Use SQLite for local state.",
					Confidence: 0,
					Pinned:     false,
				},
			},
			updateRequests: &updateRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeUpdate,
			Intent:     "lower confidence and unpin",
			Input:      []byte(`{"memory_id":"memory-1","content":"","kind":"","tags_mode":"keep","tags":[],"confidence_mode":"set","confidence":0,"pinned_mode":"set","pinned":false,"reason":"stale memory"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory update: %v", err)
	}
	if len(updateRequests) != 1 {
		t.Fatalf("expected one update request, got %d", len(updateRequests))
	}
	request := updateRequests[0]
	if request.Confidence == nil || *request.Confidence != 0 {
		t.Fatalf("expected confidence pointer with zero value, got %#v", request.Confidence)
	}
	if request.Pinned == nil || *request.Pinned {
		t.Fatalf("expected pinned pointer with false value, got %#v", request.Pinned)
	}
	if request.Content != "" || request.Kind != "" || request.ReplaceTags {
		t.Fatalf("unexpected extra update fields: %#v", request)
	}
	if result.Metadata["updated"] != "true" || !strings.Contains(result.Observation, "confidence: 0.00") || !strings.Contains(result.Observation, "pinned: false") {
		t.Fatalf("unexpected update result: %#v", result)
	}
}

func TestExecuteMemoryToolUpdatesScope(t *testing.T) {
	t.Parallel()

	var updateRequests []domain.MemoryUpdateRequest
	activities := Activities{
		Store: stubProjectStore{
			updateResult: domain.MemoryUpdateResult{
				Updated: true,
				Memory: domain.Memory{
					ID:          "memory-1",
					ProjectID:   "default",
					ChannelType: domain.ChannelTypeDiscord,
					ChannelID:   "channel-1",
					Scope:       domain.MemoryScopeChannel,
					Kind:        "preference",
					Content:     "Use Go for this channel.",
				},
			},
			updateRequests: &updateRequests,
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeUpdate,
			Intent:     "move memory to channel scope",
			Input:      []byte(`{"memory_id":"memory-1","content":"","kind":"","scope":"channel","tags_mode":"keep","tags":[],"confidence_mode":"keep","confidence":0,"pinned_mode":"keep","pinned":false,"reason":"applies only to this channel"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory scope update: %v", err)
	}
	if len(updateRequests) != 1 {
		t.Fatalf("expected one update request, got %d", len(updateRequests))
	}
	request := updateRequests[0]
	if request.Scope != domain.MemoryScopeChannel || request.ChannelType != domain.ChannelTypeDiscord || request.ChannelID != "channel-1" {
		t.Fatalf("expected channel scope update request, got %#v", request)
	}
	if result.Metadata["updated"] != "true" || result.Metadata["scope"] != "channel" {
		t.Fatalf("unexpected scope update result: %#v", result)
	}
}

func TestExecuteMemoryToolRememberUpsertsEmbedding(t *testing.T) {
	t.Parallel()

	embeddingRequests := []domain.MemoryEmbedding{}
	embeddingInputs := []string{}
	remembered := []domain.Memory{}
	vector := make([]float32, 1536)
	vector[0] = 1
	activities := Activities{
		Store:          stubProjectStore{embeddingRequests: &embeddingRequests, remembered: &remembered},
		MemoryEnabled:  true,
		MemoryEmbedder: fakeEmbedder{vector: vector, inputs: &embeddingInputs},
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ActorID:     "discord-user-1",
			ActorName:   "luka",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeAdd,
			Intent:     "remember storage preference",
			Input:      []byte(`{"content":"Use SQLite for local state.","scope":"project","kind":"preference","tags":["storage","sqlite"],"confidence":1,"pinned":true,"reason":"user preference"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute memory remember: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("unexpected memory remember result: %#v", result)
	}
	if len(remembered) != 1 {
		t.Fatalf("expected one remembered memory, got %#v", remembered)
	}
	memory := remembered[0]
	if memory.UserID != "discord:discord-user-1" || memory.Actor != "luka" || memory.Metadata["actor_name"] != "luka" || memory.Metadata["actor_id"] != "discord-user-1" {
		t.Fatalf("expected discord actor metadata to be persisted, got %#v", memory)
	}
	if _, ok := memory.Metadata["memory_user_id"]; ok {
		t.Fatalf("memory_user_id should live on the typed user_id field, got metadata %#v", memory.Metadata)
	}
	if len(embeddingInputs) != 1 || !strings.Contains(embeddingInputs[0], "kind: preference") || !strings.Contains(embeddingInputs[0], "content: Use SQLite for local state.") {
		t.Fatalf("unexpected embedding inputs: %#v", embeddingInputs)
	}
	if len(embeddingRequests) != 1 {
		t.Fatalf("expected one embedding upsert, got %#v", embeddingRequests)
	}
	request := embeddingRequests[0]
	if request.MemoryID == "" || request.Provider != embedding.ProviderOpenAI || request.Model != embedding.DefaultOpenAIModel || request.Dimensions != 1536 || len(request.Vector) != 1536 {
		t.Fatalf("unexpected embedding request: %#v", request)
	}
	if request.ContentHash == "" {
		t.Fatalf("expected content hash in embedding request")
	}
}

func TestExecuteMemoryToolReturnsPolicyRejectionObservation(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Store: stubProjectStore{
			rememberErr: fmt.Errorf("%w: content appears to contain a secret", storage.ErrMemoryPolicyRejected),
		},
		MemoryEnabled: true,
	}
	result, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event:      domain.Event{ID: "event-1", ProjectID: "default"},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryProposeAdd,
			Intent:     "remember secret",
			Input:      []byte(`{"content":"The API key is secret.","scope":"project","kind":"fact","tags":[],"confidence":1,"pinned":false,"reason":"test"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err != nil {
		t.Fatalf("policy rejection should not fail the activity: %v", err)
	}
	if result.Status != domain.ExecutionStatusFailed || result.ResultCode != "policy_rejected" {
		t.Fatalf("unexpected policy rejection result: %#v", result)
	}
	if !strings.Contains(result.Observation, "Memory rejected by policy: content appears to contain a secret") || result.Metadata["policy_rejected"] != "true" {
		t.Fatalf("expected policy rejection observation, got %#v", result)
	}
}

func TestLoadContextIncludesScopedConversationHistory(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	queries := []storage.ConversationQuery{}
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "thread", CreatedAt: base},
					{ID: "thread-2", ProjectID: "default", Role: domain.ConversationRoleTool, Body: "tool", CreatedAt: base.Add(time.Second)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-1", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "channel", CreatedAt: base.Add(2 * time.Second)},
				},
			},
			conversationQueries: &queries,
		},
		Project:                     domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:         true,
		ConversationLimit:           3,
		ConversationMaxContextChars: 8000,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "thread-1,thread-2,channel-1" {
		t.Fatalf("unexpected conversation history: %#v", loaded.Conversation)
	}
	if len(queries) != 2 ||
		queries[0].Scope != storage.ConversationScopeThread ||
		queries[1].Scope != storage.ConversationScopeChannel {
		t.Fatalf("unexpected conversation queries: %#v", queries)
	}
	for _, query := range queries {
		if query.ExcludeEventID != "current-event" {
			t.Fatalf("expected current event to be excluded, got %#v", query)
		}
		if !query.ExcludeControl {
			t.Fatalf("expected control messages to be excluded from normal history, got %#v", query)
		}
	}
}

func TestLoadContextInfersTaskReplyConversationScope(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	queries := []storage.ConversationQuery{}
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-message", ProjectID: "default", ChannelType: domain.ChannelTypeDiscord, ChannelID: "thread-1", ThreadID: "thread-1", Role: domain.ConversationRoleUser, Body: "thread", CreatedAt: base},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-message", ProjectID: "default", ChannelType: domain.ChannelTypeDiscord, ChannelID: "thread-1", Role: domain.ConversationRoleAssistant, Body: "channel", CreatedAt: base.Add(time.Second)},
				},
			},
			conversationQueries: &queries,
		},
		Project:             domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled: true,
		ConversationLimit:   5,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "thread-1",
		Body:        "continue",
		Metadata:    domain.Metadata{domain.MetadataKeyControl: domain.MetadataControlTaskReply},
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "thread-message,channel-message" {
		t.Fatalf("expected inferred thread and channel history, got %#v", loaded.Conversation)
	}
	if len(queries) != 2 ||
		queries[0].Scope != storage.ConversationScopeThread ||
		queries[0].ThreadID != "thread-1" ||
		queries[1].Scope != storage.ConversationScopeChannel {
		t.Fatalf("expected inferred task reply conversation queries, got %#v", queries)
	}
}

func TestLoadContextWithChannelSummaryIncludesUnsummarizedChannelHistoryBeforeThreadRoot(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	root := domain.ConversationMessage{
		ID:          "root-message",
		ProjectID:   "default",
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "original parent request",
		CreatedAt:   base.Add(2 * time.Minute),
	}
	conversationQueries := []storage.ConversationQuery{}
	summaryQueries := []storage.ConversationSummaryQuery{}
	activities := Activities{
		Store: stubProjectStore{
			conversationThreads: map[string]domain.ConversationThread{
				"thread-1": {
					ProjectID:     "default",
					ChannelType:   domain.ChannelTypeDiscord,
					ChannelID:     "channel-1",
					ThreadID:      "thread-1",
					RootMessageID: "root-source-message",
					CreatedAt:     base.Add(2 * time.Minute),
				},
			},
			rootMessages: map[string]domain.ConversationMessage{
				"root-source-message": root,
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "thread prior", CreatedAt: base.Add(3 * time.Minute)},
					{ID: "thread-2", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "thread answer", CreatedAt: base.Add(4 * time.Minute)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-covered", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "already summarized channel detail", CreatedAt: base},
					{ID: "channel-gap", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "main channel detail before thread", CreatedAt: base.Add(time.Minute)},
					{ID: "channel-after-root", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "main channel detail after thread", CreatedAt: base.Add(5 * time.Minute)},
				},
			},
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeChannel: {
					{
						ID:            "channel-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Channel-level summary.",
						FromMessageID: "channel-old",
						ToMessageID:   "channel-covered",
						FromCreatedAt: base,
						ToCreatedAt:   base,
					},
				},
			},
			conversationQueries: &conversationQueries,
			summaryQueries:      &summaryQueries,
		},
		Project:                    domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:        true,
		ConversationLimit:          10,
		ConversationSummaryEnabled: true,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-gap,root-message,thread-1,thread-2" {
		t.Fatalf("expected unsummarized channel history, thread root, and thread raw history, got %#v", loaded.Conversation)
	}
	if len(loaded.ConversationSummaries) != 1 || loaded.ConversationSummaries[0].ID != "channel-summary" {
		t.Fatalf("expected channel summary, got %#v", loaded.ConversationSummaries)
	}
	if len(conversationQueries) != 2 ||
		conversationQueries[0].Scope != storage.ConversationScopeThread ||
		conversationQueries[1].Scope != storage.ConversationScopeChannel {
		t.Fatalf("expected thread and channel raw history queries, got %#v", conversationQueries)
	}
	if !conversationQueries[1].AfterCreatedAt.Equal(base) ||
		!conversationQueries[1].BeforeCreatedAt.Equal(root.CreatedAt) ||
		conversationQueries[1].BeforeID != root.ID {
		t.Fatalf("expected channel history after summary and before root, got %#v", conversationQueries[1])
	}
	if len(summaryQueries) < 2 ||
		summaryQueries[0].Scope != domain.ConversationSummaryScopeThread ||
		summaryQueries[1].Scope != domain.ConversationSummaryScopeChannel {
		t.Fatalf("expected thread then channel summary lookups, got %#v", summaryQueries)
	}
}

func TestLoadContextWithChannelAndThreadSummariesIncludesChannelGapRootAndThreadRaw(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	root := domain.ConversationMessage{
		ID:          "root-message",
		ProjectID:   "default",
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "original parent request",
		CreatedAt:   base.Add(3 * time.Minute),
	}
	activities := Activities{
		Store: stubProjectStore{
			conversationThreads: map[string]domain.ConversationThread{
				"thread-1": {
					ProjectID:     "default",
					ChannelType:   domain.ChannelTypeDiscord,
					ChannelID:     "channel-1",
					ThreadID:      "thread-1",
					RootMessageID: "root-source-message",
					CreatedAt:     base.Add(3 * time.Minute),
				},
			},
			rootMessages: map[string]domain.ConversationMessage{
				"root-source-message": root,
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-covered", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "covered by thread summary", CreatedAt: base.Add(4 * time.Minute)},
					{ID: "thread-gap", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "unsummarized thread answer", CreatedAt: base.Add(5 * time.Minute)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-covered", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "covered by channel summary", CreatedAt: base},
					{ID: "channel-gap-1", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "channel answer before root", CreatedAt: base.Add(time.Minute)},
					{ID: "channel-gap-2", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "channel request before root", CreatedAt: base.Add(2 * time.Minute)},
					{ID: "channel-after-root", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "channel request after root", CreatedAt: base.Add(10 * time.Minute)},
				},
			},
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeThread: {
					{
						ID:            "thread-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						ThreadID:      "thread-1",
						Scope:         domain.ConversationSummaryScopeThread,
						Summary:       "Thread-level summary.",
						FromMessageID: "thread-old",
						ToMessageID:   "thread-covered",
						FromCreatedAt: base.Add(4 * time.Minute),
						ToCreatedAt:   base.Add(4 * time.Minute),
					},
				},
				domain.ConversationSummaryScopeChannel: {
					{
						ID:            "channel-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Channel-level summary.",
						FromMessageID: "channel-old",
						ToMessageID:   "channel-covered",
						FromCreatedAt: base,
						ToCreatedAt:   base,
					},
				},
			},
		},
		Project:                    domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:        true,
		ConversationLimit:          20,
		ConversationSummaryEnabled: true,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-gap-1,channel-gap-2,root-message,thread-gap" {
		t.Fatalf("expected channel gap, root, and unsummarized thread raw history, got %#v", loaded.Conversation)
	}
	gotSummaries := make([]string, 0, len(loaded.ConversationSummaries))
	for _, summary := range loaded.ConversationSummaries {
		gotSummaries = append(gotSummaries, summary.ID)
	}
	if strings.Join(gotSummaries, ",") != "thread-summary,channel-summary" {
		t.Fatalf("expected thread and channel summaries, got %#v", loaded.ConversationSummaries)
	}
}

func TestLoadContextFallsBackToDiscordThreadRootWhenThreadRecordIsMissing(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	root := domain.ConversationMessage{
		ID:          "root-message",
		ProjectID:   "default",
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "original parent request",
		CreatedAt:   base.Add(2 * time.Minute),
	}
	activities := Activities{
		Store: stubProjectStore{
			rootMessages: map[string]domain.ConversationMessage{
				"thread-1": root,
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-recent", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "thread answer", CreatedAt: base.Add(3 * time.Minute)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-before-root", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "channel context before root", CreatedAt: base.Add(time.Minute)},
					{ID: "channel-after-root", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "channel context after root", CreatedAt: base.Add(4 * time.Minute)},
				},
			},
		},
		Project:             domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled: true,
		ConversationLimit:   20,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-before-root,root-message,thread-recent" {
		t.Fatalf("expected fallback root boundary without thread record, got %#v", loaded.Conversation)
	}
}

func TestLoadContextForOldThreadExcludesChannelMessagesAfterThreadStart(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	threadStart := base.Add(time.Minute)
	activities := Activities{
		Store: stubProjectStore{
			conversationThreads: map[string]domain.ConversationThread{
				"thread-1": {
					ProjectID:   "default",
					ChannelType: domain.ChannelTypeDiscord,
					ChannelID:   "channel-1",
					ThreadID:    "thread-1",
					CreatedAt:   threadStart,
				},
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-recent", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "old thread follow-up", CreatedAt: base.Add(10 * time.Minute)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-before", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "channel context before thread", CreatedAt: base},
					{ID: "channel-after", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "newer channel context should not leak", CreatedAt: base.Add(5 * time.Minute)},
				},
			},
		},
		Project:             domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled: true,
		ConversationLimit:   20,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue old thread",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-before,thread-recent" {
		t.Fatalf("expected old channel context plus thread context only, got %#v", loaded.Conversation)
	}
}

func TestLoadContextLooksUpThreadMetadataByChannel(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	threadQueries := []storage.ConversationThreadQuery{}
	activities := Activities{
		Store: stubProjectStore{
			threadQueries: &threadQueries,
			conversationThreads: map[string]domain.ConversationThread{
				"channel-1/thread-1": {
					ProjectID:   "default",
					ChannelType: domain.ChannelTypeDiscord,
					ChannelID:   "channel-1",
					ThreadID:    "thread-1",
					CreatedAt:   base.Add(time.Minute),
				},
				"channel-2/thread-1": {
					ProjectID:   "default",
					ChannelType: domain.ChannelTypeDiscord,
					ChannelID:   "channel-2",
					ThreadID:    "thread-1",
					CreatedAt:   base.Add(5 * time.Minute),
				},
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-recent", ProjectID: "default", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", ThreadID: "thread-1", Role: domain.ConversationRoleUser, Body: "thread follow-up", CreatedAt: base.Add(10 * time.Minute)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-before", ProjectID: "default", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Role: domain.ConversationRoleUser, Body: "channel context before thread", CreatedAt: base},
					{ID: "channel-after-start", ProjectID: "default", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Role: domain.ConversationRoleUser, Body: "should be after the real thread start", CreatedAt: base.Add(2 * time.Minute)},
				},
			},
		},
		Project:             domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled: true,
		ConversationLimit:   20,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue old thread",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(threadQueries) != 1 || threadQueries[0].ChannelID != "channel-1" {
		t.Fatalf("expected thread metadata lookup to include channel id, got %#v", threadQueries)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-before,thread-recent" {
		t.Fatalf("expected context bounded by channel-1 thread metadata, got %#v", loaded.Conversation)
	}
}

func TestLoadContextForOldThreadExcludesChannelSummariesAfterThreadStart(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	threadStart := base.Add(time.Minute)
	activities := Activities{
		Store: stubProjectStore{
			conversationThreads: map[string]domain.ConversationThread{
				"thread-1": {
					ProjectID:   "default",
					ChannelType: domain.ChannelTypeDiscord,
					ChannelID:   "channel-1",
					ThreadID:    "thread-1",
					CreatedAt:   threadStart,
				},
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-recent", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "old thread follow-up", CreatedAt: base.Add(10 * time.Minute)},
				},
			},
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeChannel: {
					{
						ID:            "channel-summary-before",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Channel context before thread.",
						FromMessageID: "channel-old",
						ToMessageID:   "channel-before",
						FromCreatedAt: base,
						ToCreatedAt:   base,
					},
					{
						ID:            "channel-summary-after",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Newer channel context should not leak.",
						FromMessageID: "channel-new",
						ToMessageID:   "channel-after",
						FromCreatedAt: base.Add(2 * time.Minute),
						ToCreatedAt:   base.Add(5 * time.Minute),
					},
				},
			},
		},
		Project:                    domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:        true,
		ConversationLimit:          20,
		ConversationSummaryEnabled: true,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue old thread",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	gotSummaries := make([]string, 0, len(loaded.ConversationSummaries))
	for _, summary := range loaded.ConversationSummaries {
		gotSummaries = append(gotSummaries, summary.ID)
	}
	if strings.Join(gotSummaries, ",") != "channel-summary-before" {
		t.Fatalf("expected only pre-thread channel summary, got %#v", loaded.ConversationSummaries)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "thread-recent" {
		t.Fatalf("expected only thread raw history when pre-thread channel summary exists, got %#v", loaded.Conversation)
	}
}

func TestLoadContextIncludesOnlyChannelScopedConversationSummaries(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	queries := []storage.ConversationSummaryQuery{}
	activities := Activities{
		Store: stubProjectStore{
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeThread: {
					{ID: "thread-summary", ProjectID: "default", Scope: domain.ConversationSummaryScopeThread, Summary: "thread", ToCreatedAt: base},
				},
				domain.ConversationSummaryScopeChannel: {
					{ID: "channel-summary", ProjectID: "default", Scope: domain.ConversationSummaryScopeChannel, Summary: "channel", ToCreatedAt: base.Add(time.Second)},
				},
				domain.ConversationSummaryScopeProject: {
					{ID: "project-summary", ProjectID: "default", Scope: domain.ConversationSummaryScopeProject, Summary: "project", ToCreatedAt: base.Add(2 * time.Second)},
				},
			},
			summaryQueries: &queries,
		},
		ConversationEnabled:        true,
		ConversationSummaryEnabled: true,
	}

	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "event-1",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		ThreadID:    "thread-a",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	got := make([]string, 0, len(loaded.ConversationSummaries))
	for _, summary := range loaded.ConversationSummaries {
		got = append(got, summary.ID)
	}
	if strings.Join(got, ",") != "thread-summary,channel-summary" {
		t.Fatalf("unexpected summaries: %#v", loaded.ConversationSummaries)
	}
	if len(queries) != 2 ||
		queries[0].Scope != domain.ConversationSummaryScopeThread ||
		queries[1].Scope != domain.ConversationSummaryScopeChannel {
		t.Fatalf("unexpected summary queries: %#v", queries)
	}
}

func TestLoadContextWithChannelAndThreadSummariesUsesUnsummarizedThreadRawHistory(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "thread-old-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "covered by thread summary", CreatedAt: base},
					{ID: "thread-old-2", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "also covered by thread summary", CreatedAt: base.Add(time.Second)},
					{ID: "thread-recent-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "recent thread detail", CreatedAt: base.Add(2 * time.Second)},
					{ID: "thread-recent-2", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "recent thread answer", CreatedAt: base.Add(3 * time.Second)},
				},
				storage.ConversationScopeChannel: {
					{ID: "channel-covered", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "covered by channel summary", CreatedAt: base},
				},
			},
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeThread: {
					{
						ID:            "thread-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						ThreadID:      "thread-1",
						Scope:         domain.ConversationSummaryScopeThread,
						Summary:       "Thread-level summary.",
						FromMessageID: "thread-old-1",
						ToMessageID:   "thread-old-2",
						FromCreatedAt: base,
						ToCreatedAt:   base.Add(time.Second),
					},
				},
				domain.ConversationSummaryScopeChannel: {
					{
						ID:            "channel-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Channel-level summary.",
						FromMessageID: "channel-covered",
						ToMessageID:   "channel-covered",
						FromCreatedAt: base,
						ToCreatedAt:   base,
					},
				},
			},
		},
		Project:                    domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:        true,
		ConversationLimit:          20,
		ConversationSummaryEnabled: true,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "thread-recent-1,thread-recent-2" {
		t.Fatalf("expected only unsummarized thread raw history, got %#v", loaded.Conversation)
	}
	gotSummaries := make([]string, 0, len(loaded.ConversationSummaries))
	for _, summary := range loaded.ConversationSummaries {
		gotSummaries = append(gotSummaries, summary.ID)
	}
	if strings.Join(gotSummaries, ",") != "thread-summary,channel-summary" {
		t.Fatalf("expected thread and channel summaries, got %#v", loaded.ConversationSummaries)
	}
}

func TestLoadContextWithChannelSummaryUsesUnsummarizedChannelRawHistory(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeChannel: {
					{ID: "channel-old-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "covered by channel summary", CreatedAt: base},
					{ID: "channel-old-2", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "also covered by channel summary", CreatedAt: base.Add(time.Second)},
					{ID: "channel-recent-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "recent channel detail", CreatedAt: base.Add(2 * time.Second)},
					{ID: "channel-recent-2", ProjectID: "default", Role: domain.ConversationRoleAssistant, Body: "recent channel answer", CreatedAt: base.Add(3 * time.Second)},
				},
			},
			summariesByScope: map[domain.ConversationSummaryScope][]domain.ConversationSummary{
				domain.ConversationSummaryScopeChannel: {
					{
						ID:            "channel-summary",
						ProjectID:     "default",
						ChannelType:   domain.ChannelTypeDiscord,
						ChannelID:     "channel-1",
						Scope:         domain.ConversationSummaryScopeChannel,
						Summary:       "Channel-level summary.",
						FromMessageID: "channel-old-1",
						ToMessageID:   "channel-old-2",
						FromCreatedAt: base,
						ToCreatedAt:   base.Add(time.Second),
					},
				},
			},
		},
		Project:                    domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled:        true,
		ConversationLimit:          20,
		ConversationSummaryEnabled: true,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:          "current-event",
		ProjectID:   "default",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "channel-recent-1,channel-recent-2" {
		t.Fatalf("expected only unsummarized channel raw history, got %#v", loaded.Conversation)
	}
	if len(loaded.ConversationSummaries) != 1 || loaded.ConversationSummaries[0].ID != "channel-summary" {
		t.Fatalf("expected channel summary, got %#v", loaded.ConversationSummaries)
	}
}

func TestLoadContextUsesProjectConversationOnlyWithoutChannel(t *testing.T) {
	t.Parallel()

	queries := []storage.ConversationQuery{}
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeProject: {
					{ID: "project-1", ProjectID: "default", Role: domain.ConversationRoleUser, Body: "local prior"},
				},
			},
			conversationQueries: &queries,
		},
		Project:             domain.Project{ID: "default", Name: "OpenCTO"},
		ConversationEnabled: true,
		ConversationLimit:   10,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:        "current-event",
		ProjectID: "default",
		Body:      "continue",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if got := idsFromConversation(loaded.Conversation); strings.Join(got, ",") != "project-1" {
		t.Fatalf("unexpected project conversation history: %#v", loaded.Conversation)
	}
	if len(queries) != 1 || queries[0].Scope != storage.ConversationScopeProject {
		t.Fatalf("unexpected conversation queries: %#v", queries)
	}
}

func TestResponseSessionUsesOptionalReporter(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	reporter := &captureReporter{onTyping: cancel}
	activities := Activities{Reporter: reporter}
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
	}

	if err := activities.ResponseSession(ctx, ResponseSessionRequest{ProjectID: "project-1", Event: event}); err != nil {
		t.Fatalf("response session: %v", err)
	}
	if len(reporter.typingEvents) != 1 || reporter.typingEvents[0].ChannelID != "channel-1" {
		t.Fatalf("expected response indicator event to be passed to reporter, got %#v", reporter.typingEvents)
	}
}

func TestResponseSessionIgnoresTypingErrors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	reporter := &captureReporter{
		typingErr: errors.New("discord unavailable"),
		onTyping:  cancel,
	}
	activities := Activities{Reporter: reporter}
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
	}

	if err := activities.ResponseSession(ctx, ResponseSessionRequest{ProjectID: "project-1", Event: event}); err != nil {
		t.Fatalf("response session should be best-effort, got %v", err)
	}
	if len(reporter.typingEvents) != 1 {
		t.Fatalf("expected one typing attempt, got %#v", reporter.typingEvents)
	}
}

func TestExecuteToolRunsDedicatedFileTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "example.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	skillsRoot := filepath.Join(dir, "skills")
	skillDir := filepath.Join(skillsRoot, "go-testing")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, skillcatalog.SkillFileName), []byte("# Go Testing\n\nUse when testing Go code.\n"), 0o644); err != nil {
		t.Fatalf("write skill fixture: %v", err)
	}

	activities := Activities{
		WorkspaceRoot: dir,
		SkillsRoot:    skillsRoot,
		Grep: fakeGrepExecutor{result: greptool.Result{
			Stdout:   filePath + ":hi\n",
			ExitCode: 0,
		}},
	}

	readResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeRead, "read-1", map[string]any{
		"file_path": filePath,
	}))
	if err != nil {
		t.Fatalf("read tool: %v", err)
	}
	if readResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(readResult.Observation, "hello") {
		t.Fatalf("unexpected read result: %#v", readResult)
	}

	editResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeEdit, "edit-1", map[string]any{
		"file_path":   filePath,
		"old_string":  "hello",
		"new_string":  "hi",
		"replace_all": false,
	}))
	if err != nil {
		t.Fatalf("edit tool: %v", err)
	}
	if editResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(editResult.Observation, "replacements: 1") {
		t.Fatalf("unexpected edit result: %#v", editResult)
	}
	edited, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read edited file: %v", err)
	}
	if string(edited) != "hi\n" {
		t.Fatalf("unexpected edited content: %q", edited)
	}

	writePath := filepath.Join(dir, "new.txt")
	writeResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeWrite, "write-1", map[string]any{
		"file_path": writePath,
		"content":   "new file\n",
	}))
	if err != nil {
		t.Fatalf("write tool: %v", err)
	}
	if writeResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(writeResult.Observation, "overwritten: false") {
		t.Fatalf("unexpected write result: %#v", writeResult)
	}

	globResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeGlob, "glob-1", map[string]any{
		"pattern": "*.txt",
		"path":    dir,
	}))
	if err != nil {
		t.Fatalf("glob tool: %v", err)
	}
	if globResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(globResult.Observation, filePath) || !strings.Contains(globResult.Observation, writePath) {
		t.Fatalf("unexpected glob result: %#v", globResult)
	}

	grepResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeGrep, "grep-1", map[string]any{
		"pattern":     "hi",
		"path":        ".",
		"output_mode": "content",
	}))
	if err != nil {
		t.Fatalf("grep tool: %v", err)
	}
	if grepResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(grepResult.Observation, filePath+":hi") {
		t.Fatalf("unexpected grep result: %#v", grepResult)
	}

	skillResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeSkill, "skill-1", map[string]any{
		"skill_id": "go-testing",
	}))
	if err != nil {
		t.Fatalf("skill tool: %v", err)
	}
	if skillResult.Status != domain.ExecutionStatusSucceeded || !strings.Contains(skillResult.Observation, "# Go Testing") {
		t.Fatalf("unexpected skill result: %#v", skillResult)
	}

	scheduleExecutor := &fakeScheduleExecutor{result: scheduletool.Result{
		Operation:  scheduletool.OperationCreate,
		WorkflowID: "daily-hello",
		ScheduleID: "opencto:project-1:workflow-schedule:daily-hello",
		Name:       "daily hello",
		TimeZone:   "Asia/Tbilisi",
		Cron:       "0 9 * * *",
		Message:    "schedule created",
	}}
	activities.Schedule = scheduleExecutor
	scheduleResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeWorkflowCreate, "schedule-1", map[string]any{
		"workflow_id":    "daily-hello",
		"prompt":         "Create a daily hello workflow.",
		"commit_message": "",
	}))
	if err != nil {
		t.Fatalf("schedule tool: %v", err)
	}
	if scheduleResult.Status != domain.ExecutionStatusSucceeded ||
		scheduleResult.Metadata["schedule_id"] != "opencto:project-1:workflow-schedule:daily-hello" ||
		scheduleExecutor.createRequest.SourceEvent.ChannelID != "channel-1" {
		t.Fatalf("unexpected schedule result: %#v request=%#v", scheduleResult, scheduleExecutor.createRequest)
	}
}

func TestExecuteAgentToolIsNotRunInsideActivity(t *testing.T) {
	t.Parallel()

	result, err := (&Activities{
		WorkspaceRoot: t.TempDir(),
	}).ExecuteTool(context.Background(), executeRequest(domain.ToolTypeAgent, "agent-1", map[string]any{
		"goal":   "Nested",
		"prompt": "Run as workflow.",
	}))
	if err != nil {
		t.Fatalf("agent tool should return structured failure, got error: %v", err)
	}
	if result.Status != domain.ExecutionStatusFailed || !strings.Contains(result.Error, "unsupported tool type") {
		t.Fatalf("expected unsupported Agent tool activity failure, got %#v", result)
	}
}

func TestCompressAgentObservationsSummarizesOlderHistory(t *testing.T) {
	t.Parallel()

	compressorInput := agent.AgentObservationCompressionInput{}
	result, err := (&Activities{
		AgentObservationCompressor: stubAgentObservationCompressor{
			output: agent.AgentObservationCompressionOutput{Summary: "Older agent tool context."},
			input:  &compressorInput,
		},
		ConversationSummaryTrigger:  10,
		ConversationSummaryRecent:   1,
		ConversationSummaryMaxChars: 200,
		ConversationMaxContextChars: 1000,
	}).CompressAgentObservations(context.Background(), CompressAgentObservationsRequest{
		ProjectID:       "project-1",
		Goal:            "Audit files",
		PreviousSummary: "Previous context.",
		Observations: []agent.ExecutionFeedback{
			{Cycle: 1, Tool: domain.ToolTypeRead, Status: string(domain.ExecutionStatusSucceeded), Observation: strings.Repeat("old ", 20)},
			{Cycle: 2, Tool: domain.ToolTypeExec, Status: string(domain.ExecutionStatusSucceeded), Observation: strings.Repeat("older ", 20)},
			{Cycle: 3, Tool: domain.ToolTypeGrep, Status: string(domain.ExecutionStatusSucceeded), Observation: "recent"},
		},
	})
	if err != nil {
		t.Fatalf("compress agent observations: %v", err)
	}
	if !result.Summarized || result.Summary != "Older agent tool context." {
		t.Fatalf("unexpected compression result: %#v", result)
	}
	if len(result.RemainingObservations) != 1 || result.RemainingObservations[0].Cycle != 3 {
		t.Fatalf("expected only recent observation to remain raw, got %#v", result.RemainingObservations)
	}
	if compressorInput.Goal != "Audit files" || len(compressorInput.Observations) != 2 {
		t.Fatalf("unexpected compressor input: %#v", compressorInput)
	}
}

func TestCompressAgentObservationsCountsToolInputTowardBudget(t *testing.T) {
	t.Parallel()

	writeInput, err := json.Marshal(map[string]string{
		"file_path": "/tmp/large.txt",
		"content":   strings.Repeat("x", 200),
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	compressorInput := agent.AgentObservationCompressionInput{}
	result, err := (&Activities{
		AgentObservationCompressor: stubAgentObservationCompressor{
			output: agent.AgentObservationCompressionOutput{Summary: "Large write input."},
			input:  &compressorInput,
		},
		ConversationSummaryTrigger:  80,
		ConversationSummaryRecent:   1,
		ConversationSummaryMaxChars: 200,
		ConversationMaxContextChars: 1000,
	}).CompressAgentObservations(context.Background(), CompressAgentObservationsRequest{
		ProjectID: "project-1",
		Goal:      "Write large file",
		Observations: []agent.ExecutionFeedback{
			{Cycle: 1, Tool: domain.ToolTypeWrite, Status: string(domain.ExecutionStatusSucceeded), Input: writeInput},
			{Cycle: 2, Tool: domain.ToolTypeRead, Status: string(domain.ExecutionStatusSucceeded), Observation: "recent"},
		},
	})
	if err != nil {
		t.Fatalf("compress agent observations: %v", err)
	}
	if !result.Summarized || result.Summary != "Large write input." {
		t.Fatalf("expected large input to trigger compression, got %#v", result)
	}
	if len(compressorInput.Observations) != 1 || string(compressorInput.Observations[0].Input) != string(writeInput) {
		t.Fatalf("unexpected compressor input: %#v", compressorInput)
	}
}

func TestCompressAgentObservationsRejectsEmptySummaryOverBudget(t *testing.T) {
	t.Parallel()

	result, err := (&Activities{
		AgentObservationCompressor: stubAgentObservationCompressor{
			output: agent.AgentObservationCompressionOutput{Summary: ""},
		},
		ConversationSummaryTrigger:  10,
		ConversationSummaryRecent:   1,
		ConversationSummaryMaxChars: 200,
		ConversationMaxContextChars: 80,
	}).CompressAgentObservations(context.Background(), CompressAgentObservationsRequest{
		ProjectID: "project-1",
		Goal:      "Audit files",
		Observations: []agent.ExecutionFeedback{
			{Cycle: 1, Tool: domain.ToolTypeExec, Status: string(domain.ExecutionStatusSucceeded), Observation: strings.Repeat("old ", 50)},
			{Cycle: 2, Tool: domain.ToolTypeRead, Status: string(domain.ExecutionStatusSucceeded), Observation: "recent"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "empty summary") {
		t.Fatalf("expected empty-summary budget error, got result=%#v err=%v", result, err)
	}
}

func TestExecuteToolPostProcessorsRunForFailedToolResult(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "example.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	var sawFailure bool
	activities := Activities{
		WorkspaceRoot: dir,
		ToolResultProcessors: []postprocess.Processor{
			postprocess.ProcessorFunc(func(_ context.Context, req postprocess.Request, result postprocess.Result) postprocess.Result {
				if req.Tool == domain.ToolTypeEdit && req.Status == domain.ExecutionStatusFailed && strings.Contains(req.Error, "old_string") {
					sawFailure = true
				}
				result.Metadata = postprocess.EnsureMetadata(result.Metadata)
				result.Metadata["post_processed"] = "true"
				result.Observation = postprocess.AppendObservationNote(result.Observation, "post_processed: true")
				return result
			}),
		},
	}

	result, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeEdit, "edit-fail-1", map[string]any{
		"file_path":   filePath,
		"old_string":  "missing",
		"new_string":  "hi",
		"replace_all": false,
	}))
	if err != nil {
		t.Fatalf("execute tool: %v", err)
	}
	if result.Status != domain.ExecutionStatusFailed || !sawFailure {
		t.Fatalf("expected failed edit to be post-processed, got result=%#v sawFailure=%t", result, sawFailure)
	}
	if result.Metadata["post_processed"] != "true" || !strings.Contains(result.Observation, "post_processed: true") {
		t.Fatalf("expected post-processed result, got %#v", result)
	}
}

func TestExecuteToolLoadsWorkspaceSkillBeforeBuiltInSkill(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	openCTORoot := t.TempDir()
	writeActivitySkill(t, filepath.Join(workspaceRoot, "skills"), "go-testing", "# Workspace Go Testing\n\nUse the workspace workflow.\n")
	writeActivitySkill(t, filepath.Join(openCTORoot, "skills"), "go-testing", "# Built In Go Testing\n\nUse the built-in workflow.\n")

	result, err := (&Activities{
		WorkspaceRoot: workspaceRoot,
		OpenCTORoot:   openCTORoot,
	}).ExecuteTool(context.Background(), executeRequest(domain.ToolTypeSkill, "skill-1", map[string]any{
		"skill_id": "go-testing",
	}))
	if err != nil {
		t.Fatalf("skill tool: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded || !strings.Contains(result.Observation, "# Workspace Go Testing") {
		t.Fatalf("unexpected skill result: %#v", result)
	}
	if strings.Contains(result.Observation, "# Built In Go Testing") {
		t.Fatalf("expected workspace skill to shadow built-in skill, got %q", result.Observation)
	}
	if !strings.Contains(filepath.ToSlash(result.Metadata["skill_path"]), "/skills/go-testing/SKILL.md") {
		t.Fatalf("unexpected skill metadata: %#v", result.Metadata)
	}
}

func TestExecuteGlobUsesCwdForRelativePath(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	appDir := filepath.Join(workspace, "example-app")
	appFile := filepath.Join(appDir, "src", "main.go")
	otherFile := filepath.Join(workspace, "src", "main.go")
	if err := os.MkdirAll(filepath.Dir(appFile), 0o755); err != nil {
		t.Fatalf("create app fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(otherFile), 0o755); err != nil {
		t.Fatalf("create workspace fixture: %v", err)
	}
	if err := os.WriteFile(appFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write app fixture: %v", err)
	}
	if err := os.WriteFile(otherFile, []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write workspace fixture: %v", err)
	}

	activities := Activities{WorkspaceRoot: workspace}
	result, err := activities.ExecuteTool(context.Background(), executeRequest(domain.ToolTypeGlob, "glob-cwd", map[string]any{
		"cwd":     "example-app",
		"path":    "src",
		"pattern": "*.go",
	}))
	if err != nil {
		t.Fatalf("glob tool: %v", err)
	}
	if result.WorkingDirectory != appDir {
		t.Fatalf("expected glob working directory %q, got %q", appDir, result.WorkingDirectory)
	}
	if !strings.Contains(result.Observation, appFile) {
		t.Fatalf("expected glob result to include app file %q, got %q", appFile, result.Observation)
	}
	if strings.Contains(result.Observation, otherFile) {
		t.Fatalf("expected glob result not to include workspace file %q, got %q", otherFile, result.Observation)
	}
}

func TestNextActionAssignsWorkItemInternallyForToolChoice(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Engine: stubEngine{output: agent.NextActionOutput{
			ToolChoices: []agent.ToolChoice{{
				ToolCallID: "toolu_next",
				Type:       domain.ToolTypeExec,
				Intent:     "inspect workspace",
				Command:    "pwd",
				Metadata: map[string]string{
					"tool_call_id": "toolu_next",
					"work_item_id": "model-supplied-work-item",
				},
			}},
			WorkItemID: "model-supplied-work-item",
			Status:     NextActionStatusTool,
		}},
	}

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	result, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          event,
		ExecutionCycle: 1,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	wantWorkItemID := stableActivityID("work-item", "project-1", "event-1", "1")
	if result.WorkItemID != wantWorkItemID {
		t.Fatalf("expected runtime-assigned work item %q, got %q", wantWorkItemID, result.WorkItemID)
	}
	if len(result.ToolChoices) != 1 || result.ToolChoices[0].Metadata["work_item_id"] != wantWorkItemID {
		t.Fatalf("expected internal work item metadata, got %#v", result.ToolChoices)
	}
}

func TestNextActionResultSerializesOnlyToolChoices(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusRunning,
		}}},
		ToolChoices: []agent.ToolChoice{{
			ToolCallID: "toolu_read",
			Type:       domain.ToolTypeRead,
			Intent:     "read file",
		}},
		WorkItemID: "wi-1",
		Status:     NextActionStatusTool,
	})
	if err != nil {
		t.Fatalf("marshal next action result: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"tool_choices"`) {
		t.Fatalf("expected tool_choices field, got %s", text)
	}
	if strings.Contains(text, `"tool_choice"`) {
		t.Fatalf("did not expect legacy tool_choice field, got %s", text)
	}
}

func TestNextActionAddsWorkflowRunSessionToEngineContext(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()

	var capturedSession agent.LLMSession
	activities := &Activities{
		Engine: stubEngine{
			output: agent.NextActionOutput{
				NextAction: agent.NextAction{ResponseMessage: "done"},
				Status:     NextActionStatusCompleted,
			},
			session: &capturedSession,
		},
		Project:    domain.Project{ID: "project-1", Name: "OpenCTO"},
		SkillsRoot: t.TempDir(),
	}
	env.RegisterActivityWithOptions(activities.NextAction, activity.RegisterOptions{Name: "Activities.NextAction"})

	_, err := env.ExecuteActivity("Activities.NextAction", NextActionRequest{
		ProjectID:      "project-1",
		Event:          domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"},
		ExecutionCycle: 3,
	})
	if err != nil {
		t.Fatalf("next action activity: %v", err)
	}

	if capturedSession.ProjectID != "project-1" || capturedSession.WorkflowID == "" || capturedSession.WorkflowRunID == "" || capturedSession.RequestKind != "next_action" {
		t.Fatalf("expected project/workflow/run session, got %#v", capturedSession)
	}
}

func TestNextActionSubAgentInheritsConversationContext(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	captured := agent.NextActionInput{}
	activities := Activities{
		Engine: stubEngine{
			output: agent.NextActionOutput{
				NextAction: agent.NextAction{ResponseMessage: "done"},
				Status:     NextActionStatusCompleted,
			},
			input: &captured,
		},
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{ID: "original-request", EventID: "event-original", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", ThreadID: "thread-1", Role: domain.ConversationRoleUser, Body: "Create the stargazers workflow.", CreatedAt: base},
					{ID: "clarifier", EventID: "event-clarifier", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", ThreadID: "thread-1", Role: domain.ConversationRoleAssistant, Body: "Should it restart from scratch?", CreatedAt: base.Add(time.Second)},
				},
			},
		},
		Project:                     domain.Project{ID: "project-1", Name: "OpenCTO"},
		ConversationEnabled:         true,
		ConversationLimit:           5,
		ConversationMaxContextChars: 8000,
	}

	event := domain.Event{
		ID:          "event-current",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		ThreadID:    "thread-1",
		Body:        "Yes, start fresh.",
	}
	_, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          event,
		ExecutionCycle: 1,
		SubAgent: &agent.SubAgentContext{
			Goal:   "Update scheduled workflow github-stargazers",
			Prompt: "Apply the user's latest answer.",
			RunID:  "agent-workflow-1",
		},
		ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
		RestrictTools: true,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}

	if !strings.Contains(captured.Context.Event.Body, "Update scheduled workflow github-stargazers") ||
		!strings.Contains(captured.Context.Event.Body, "Apply the user's latest answer.") {
		t.Fatalf("expected synthetic agent event, got %q", captured.Context.Event.Body)
	}
	if got := idsFromConversation(captured.Context.Conversation); strings.Join(got, ",") != "original-request,clarifier" {
		t.Fatalf("expected inherited parent conversation, got %#v", captured.Context.Conversation)
	}
	if len(captured.Context.Skills) != 0 {
		t.Fatalf("expected skills to respect agent tool allowlist, got %#v", captured.Context.Skills)
	}
}

func TestNextActionAssignsSubAgentWorkItemFromSyntheticEvent(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Engine: stubEngine{output: agent.NextActionOutput{
			ToolChoices: []agent.ToolChoice{{
				ToolCallID: "toolu_read",
				Type:       domain.ToolTypeRead,
				Intent:     "read file",
				Input:      json.RawMessage(`{"file_path":"/tmp/example.txt"}`),
				Metadata: map[string]string{
					"tool_call_id": "toolu_read",
				},
			}},
			Status: NextActionStatusTool,
		}},
		Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
	}

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "parent request"}
	result, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          event,
		ExecutionCycle: 1,
		SubAgent: &agent.SubAgentContext{
			Goal:   "Inspect wiring",
			Prompt: "Find the child workflow wiring.",
			RunID:  "agent-workflow-1",
		},
		ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
		RestrictTools: true,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	parentWorkItemID := stableActivityID("work-item", "project-1", "event-1", "1")
	subAgentEventID := stableActivityID("agent-event", "project-1", "event-1", "agent-workflow-1", "Inspect wiring", "Find the child workflow wiring.")
	wantWorkItemID := stableActivityID("work-item", "project-1", subAgentEventID, "1")
	if result.WorkItemID != wantWorkItemID {
		t.Fatalf("expected agent work item %q, got %q", wantWorkItemID, result.WorkItemID)
	}
	if result.WorkItemID == parentWorkItemID {
		t.Fatalf("agent work item should not collide with parent work item %q", parentWorkItemID)
	}
	if len(result.ToolChoices) != 1 || result.ToolChoices[0].Metadata["work_item_id"] != wantWorkItemID {
		t.Fatalf("expected agent work item metadata, got %#v", result.ToolChoices)
	}
}

func TestNextActionKeepsSubAgentWorkItemWhenEngineReplacesNextAction(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Engine: stubEngine{output: agent.NextActionOutput{
			NextAction: agent.NextAction{
				ResponseMessage: "ignored for tool status",
			},
			ToolChoices: []agent.ToolChoice{{
				ToolCallID: "toolu_read",
				Type:       domain.ToolTypeRead,
				Intent:     "read file",
				Input:      json.RawMessage(`{"file_path":"/tmp/example.txt"}`),
				Metadata: map[string]string{
					"tool_call_id": "toolu_read",
				},
			}},
			Status: NextActionStatusTool,
		}},
		Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
	}

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "parent request"}
	result, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          event,
		ExecutionCycle: 1,
		SubAgent: &agent.SubAgentContext{
			Goal:   "Inspect wiring",
			Prompt: "Find the child workflow wiring.",
			RunID:  "agent-workflow-1",
		},
		ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
		RestrictTools: true,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	parentWorkItemID := stableActivityID("work-item", "project-1", "event-1", "1")
	subAgentEventID := stableActivityID("agent-event", "project-1", "event-1", "agent-workflow-1", "Inspect wiring", "Find the child workflow wiring.")
	wantWorkItemID := stableActivityID("work-item", "project-1", subAgentEventID, "1")
	if result.WorkItemID != wantWorkItemID {
		t.Fatalf("expected agent work item %q, got %q", wantWorkItemID, result.WorkItemID)
	}
	if result.WorkItemID == parentWorkItemID {
		t.Fatalf("agent work item should not fall back to parent work item %q", parentWorkItemID)
	}
}

func TestNextActionSubAgentRunIDSeparatesSyntheticWorkItems(t *testing.T) {
	t.Parallel()

	run := func(runID string) string {
		t.Helper()
		activities := Activities{
			Engine: stubEngine{output: agent.NextActionOutput{
				ToolChoices: []agent.ToolChoice{{
					ToolCallID: "toolu_read",
					Type:       domain.ToolTypeRead,
					Intent:     "read file",
					Input:      json.RawMessage(`{"file_path":"/tmp/example.txt"}`),
					Metadata: map[string]string{
						"tool_call_id": "toolu_read",
					},
				}},
				Status: NextActionStatusTool,
			}},
			Project: domain.Project{ID: "project-1", Name: "OpenCTO"},
		}
		result, err := activities.NextAction(context.Background(), NextActionRequest{
			ProjectID:      "project-1",
			Event:          domain.Event{ID: "event-1", ProjectID: "project-1", Body: "parent request"},
			ExecutionCycle: 1,
			SubAgent: &agent.SubAgentContext{
				Goal:   "Inspect wiring",
				Prompt: "Find the child workflow wiring.",
				RunID:  runID,
			},
			ToolAllowlist: []domain.ToolType{domain.ToolTypeRead},
			RestrictTools: true,
		})
		if err != nil {
			t.Fatalf("next action: %v", err)
		}
		return result.WorkItemID
	}

	first := run("agent-workflow-1")
	second := run("agent-workflow-2")
	if first == second {
		t.Fatalf("expected different work item ids for distinct agent run ids, got %q", first)
	}
}

func TestNextActionPassesEventChannelToEngine(t *testing.T) {
	t.Parallel()

	var input agent.NextActionInput
	activities := Activities{
		Engine: stubEngine{
			output: agent.NextActionOutput{
				NextAction: agent.NextAction{ResponseMessage: "done"},
				Status:     NextActionStatusCompleted,
			},
			input: &input,
		},
	}

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeCLI,
		Body:        "inspect workspace",
	}
	_, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          event,
		ExecutionCycle: 1,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	if input.ChannelType != domain.ChannelTypeCLI {
		t.Fatalf("expected local channel, got %q", input.ChannelType)
	}
}

func TestCompressConversationSummarizesOlderMessages(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	var messages []domain.ConversationMessage
	for i := 0; i < 6; i++ {
		messages = append(messages, domain.ConversationMessage{
			ID:          fmt.Sprintf("message-%d", i),
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			Body:        strings.Repeat("context ", 30),
			CreatedAt:   base.Add(time.Duration(i) * time.Minute),
		})
	}
	upserted := []domain.ConversationSummary{}
	compressorInput := agent.ConversationCompressionInput{}
	activities := Activities{
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeChannel: messages,
			},
			upsertedSummaries: &upserted,
		},
		ConversationCompressor:      stubConversationCompressor{output: agent.ConversationCompressionOutput{Summary: "Older channel context."}, input: &compressorInput},
		ConversationEnabled:         true,
		ConversationSummaryEnabled:  true,
		ConversationSummaryTrigger:  100,
		ConversationSummaryMaxChars: 1000,
		ConversationSummaryRecent:   2,
	}

	result, err := activities.CompressConversation(context.Background(), CompressConversationRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
		},
	})
	if err != nil {
		t.Fatalf("compress conversation: %v", err)
	}
	if !result.Summarized || result.MessageCount != 4 {
		t.Fatalf("unexpected compression result: %#v", result)
	}
	if len(compressorInput.Messages) != 4 || compressorInput.Messages[3].ID != "message-3" {
		t.Fatalf("expected compressor to leave recent messages raw, got %#v", compressorInput.Messages)
	}
	if len(upserted) != 1 || upserted[0].Summary != "Older channel context." || upserted[0].ToMessageID != "message-3" {
		t.Fatalf("unexpected upserted summary: %#v", upserted)
	}
	if upserted[0].Scope != domain.ConversationSummaryScopeChannel || upserted[0].ChannelID != "channel-a" {
		t.Fatalf("unexpected summary scope: %#v", upserted[0])
	}
}

func TestCompressConversationIncludesThreadRootMessage(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	root := domain.ConversationMessage{
		ID:          "root-message",
		ProjectID:   "default",
		Role:        domain.ConversationRoleUser,
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-a",
		Body:        "original parent request",
		CreatedAt:   base,
	}
	var messages []domain.ConversationMessage
	for i := 0; i < 6; i++ {
		messages = append(messages, domain.ConversationMessage{
			ID:          fmt.Sprintf("thread-message-%d", i),
			ProjectID:   "default",
			Role:        domain.ConversationRoleUser,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			ThreadID:    "thread-a",
			Body:        strings.Repeat("thread context ", 30),
			CreatedAt:   base.Add(time.Duration(i+1) * time.Minute),
		})
	}
	upserted := []domain.ConversationSummary{}
	compressorInput := agent.ConversationCompressionInput{}
	threadQueries := []storage.ConversationThreadQuery{}
	activities := Activities{
		Store: stubProjectStore{
			threadQueries: &threadQueries,
			conversationThreads: map[string]domain.ConversationThread{
				"thread-a": {
					ProjectID:     "default",
					ChannelType:   domain.ChannelTypeDiscord,
					ChannelID:     "channel-a",
					ThreadID:      "thread-a",
					RootMessageID: "root-source-message",
					CreatedAt:     base.Add(time.Minute),
				},
			},
			rootMessages: map[string]domain.ConversationMessage{
				"root-source-message": root,
			},
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: messages,
			},
			upsertedSummaries: &upserted,
		},
		ConversationCompressor:      stubConversationCompressor{output: agent.ConversationCompressionOutput{Summary: "Older thread context."}, input: &compressorInput},
		ConversationEnabled:         true,
		ConversationSummaryEnabled:  true,
		ConversationSummaryTrigger:  100,
		ConversationSummaryMaxChars: 1000,
		ConversationSummaryRecent:   2,
	}

	result, err := activities.CompressConversation(context.Background(), CompressConversationRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "default",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-a",
			ThreadID:    "thread-a",
		},
	})
	if err != nil {
		t.Fatalf("compress conversation: %v", err)
	}
	if !result.Summarized || result.MessageCount != 4 {
		t.Fatalf("unexpected compression result: %#v", result)
	}
	if len(compressorInput.Messages) != 5 ||
		compressorInput.Messages[0].ID != "root-message" ||
		compressorInput.Messages[1].ID != "thread-message-0" ||
		compressorInput.Messages[4].ID != "thread-message-3" {
		t.Fatalf("expected compressor input to include root message before thread candidates, got %#v", compressorInput.Messages)
	}
	if len(threadQueries) != 1 || threadQueries[0].ChannelID != "channel-a" {
		t.Fatalf("expected compression thread lookup to include channel id, got %#v", threadQueries)
	}
	if len(upserted) != 1 ||
		upserted[0].FromMessageID != "thread-message-0" ||
		upserted[0].ToMessageID != "thread-message-3" {
		t.Fatalf("thread summary range should not include root message: %#v", upserted)
	}
}

func TestNextActionLoadsConversationFromLatestAdditionalThreadEvent(t *testing.T) {
	t.Parallel()

	var input agent.NextActionInput
	queries := []storage.ConversationQuery{}
	base := time.Date(2026, 5, 8, 15, 0, 0, 0, time.UTC)
	activities := Activities{
		Engine: stubEngine{
			output: agent.NextActionOutput{
				NextAction: agent.NextAction{ResponseMessage: "done"},
				Status:     NextActionStatusCompleted,
			},
			input: &input,
		},
		Store: stubProjectStore{
			conversationsByScope: map[storage.ConversationScope][]domain.ConversationMessage{
				storage.ConversationScopeThread: {
					{
						ID:          "prompt-1",
						ProjectID:   "project-1",
						Role:        domain.ConversationRoleAssistant,
						ChannelType: domain.ChannelTypeDiscord,
						ChannelID:   "bot-prompt-1",
						ThreadID:    "bot-prompt-1",
						Body:        "where should I create it?",
						CreatedAt:   base,
					},
				},
			},
			conversationQueries: &queries,
		},
		ConversationEnabled: true,
	}
	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "channel-1",
		Body:        "create react/vite app",
	}
	threadReply := domain.Event{
		ID:          "event-2",
		ProjectID:   "project-1",
		ChannelType: domain.ChannelTypeDiscord,
		ChannelID:   "bot-prompt-1",
		Body:        "1",
		Metadata:    domain.Metadata{domain.MetadataKeyControl: domain.MetadataControlTaskReply},
	}
	_, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:        "project-1",
		Event:            event,
		AdditionalEvents: []domain.Event{threadReply},
		ExecutionCycle:   2,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	if len(queries) == 0 || queries[0].Scope != storage.ConversationScopeThread ||
		queries[0].ChannelID != "bot-prompt-1" ||
		queries[0].ThreadID != "bot-prompt-1" ||
		queries[0].ExcludeEventID != "event-2" {
		t.Fatalf("expected conversation query to use latest thread event, got %#v", queries)
	}
	if len(input.Context.Conversation) != 1 || input.Context.Conversation[0].Body != "where should I create it?" {
		t.Fatalf("expected thread conversation in engine input, got %#v", input.Context.Conversation)
	}
	if len(input.Context.AdditionalEvents) != 1 || input.Context.AdditionalEvents[0].Body != "1" {
		t.Fatalf("expected routed thread reply in additional events, got %#v", input.Context.AdditionalEvents)
	}
}

func TestExecuteToolReturnsManagedProcessMetadata(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX exec fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	workingDir := t.TempDir()
	stateDir := t.TempDir()
	activities := Activities{
		WorkspaceRoot: dir,
		StateDir:      stateDir,
		ToolResultProcessors: []postprocess.Processor{
			postprocess.ProcessorFunc(func(_ context.Context, req postprocess.Request, result postprocess.Result) postprocess.Result {
				if req.Tool == domain.ToolTypeExec && req.Status == domain.ExecutionStatusSucceeded {
					result.Metadata = postprocess.EnsureMetadata(result.Metadata)
					result.Metadata["post_processed"] = "true"
					result.Observation = postprocess.AppendObservationNote(result.Observation, "post_processed: true")
				}
				return result
			}),
		},
	}
	request := ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "schedule something",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID:  "toolu_bg",
			Type:        domain.ToolTypeExec,
			Intent:      "start background fixture",
			Command:     "sh",
			Args:        []string{"-c", "printf 'ready\n'; sleep 30"},
			WorkingDir:  workingDir,
			TimeoutMs:   1000,
			RunMode:     domain.ToolRunModeStartBackground,
			Idempotency: domain.ToolIdempotencyNonIdempotent,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_bg",
				"work_item_id":    "work-item-1",
			},
		},
	}
	result, err := activities.ExecuteTool(context.Background(), request)
	if err != nil {
		t.Fatalf("start exec process: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("unexpected result: %#v", result)
	}
	processID := result.Metadata["process_id"]
	if processID == "" || result.Metadata["pid"] == "" {
		t.Fatalf("expected process metadata, got %#v", result.Metadata)
	}
	if len(result.Processes) != 1 || result.Processes[0].ID != processID || result.Processes[0].Scope != domain.ProcessScopeStopOnFinish {
		t.Fatalf("expected process reference, got %#v", result.Processes)
	}
	if result.Metadata["post_processed"] != "true" || !strings.Contains(result.Observation, "post_processed: true") {
		t.Fatalf("expected background exec result to be post-processed, got %#v", result)
	}
	manager := exectool.NewProcessManager(nil)
	defer func() {
		_, _ = manager.Stop(context.Background(), stateDir, processID)
	}()

	checked, err := manager.Check(context.Background(), stateDir, processID)
	if err != nil {
		t.Fatalf("check process: %v", err)
	}
	if checked.Status != domain.ProcessStatusRunning {
		t.Fatalf("expected running process, got %#v", checked)
	}
	if checked.WorkingDirectory != workingDir {
		t.Fatalf("expected process working directory %q, got %q", workingDir, checked.WorkingDirectory)
	}
	var stdoutTail string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, err := manager.Logs(context.Background(), stateDir, processID, 1024)
		if err != nil {
			t.Fatalf("read process logs: %v", err)
		}
		stdoutTail = result.StdoutTail
		if strings.Contains(stdoutTail, "ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stdoutTail, "ready") {
		t.Fatalf("expected stdout logs to contain ready, got %q", stdoutTail)
	}
}

func TestExecuteToolStartBackgroundFailureIncludesProcessOutput(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX exec fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	var sawFailure bool
	activities := Activities{
		WorkspaceRoot: dir,
		StateDir:      t.TempDir(),
		ToolResultProcessors: []postprocess.Processor{
			postprocess.ProcessorFunc(func(_ context.Context, req postprocess.Request, result postprocess.Result) postprocess.Result {
				if req.Tool == domain.ToolTypeExec && req.Status == domain.ExecutionStatusFailed && strings.TrimSpace(req.Error) != "" {
					sawFailure = true
					result.Metadata = postprocess.EnsureMetadata(result.Metadata)
					result.Metadata["post_processed_failure"] = "true"
				}
				return result
			}),
		},
	}
	result, err := activities.ExecuteTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID:  "toolu_bg_fail",
			Type:        domain.ToolTypeExec,
			Intent:      "start failing background fixture",
			Command:     "sh",
			Args:        []string{"-c", "printf 'startup stdout\n'; printf 'startup stderr\n' >&2; exit 7"},
			TimeoutMs:   1000,
			RunMode:     domain.ToolRunModeStartBackground,
			Idempotency: domain.ToolIdempotencyIdempotent,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_bg_fail",
				"work_item_id":    "work-item-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("start exec process: %v", err)
	}
	if result.Status != domain.ExecutionStatusFailed {
		t.Fatalf("expected failed result, got %#v", result)
	}
	if !strings.Contains(result.Observation, "stdout:\nstartup stdout") {
		t.Fatalf("expected startup stdout in observation, got %q", result.Observation)
	}
	if !strings.Contains(result.Observation, "stderr:\nstartup stderr") {
		t.Fatalf("expected startup stderr in observation, got %q", result.Observation)
	}
	if !strings.Contains(result.Observation, "error:\nbackground process exited during startup") {
		t.Fatalf("expected startup error in observation, got %q", result.Observation)
	}
	if result.Error == "" {
		t.Fatalf("expected error details, got %#v", result)
	}
	if !sawFailure || result.Metadata["post_processed_failure"] != "true" {
		t.Fatalf("expected failed background exec to be post-processed, got result=%#v sawFailure=%t", result, sawFailure)
	}
}

func TestExecuteExecUsesToolChoiceWorkingDir(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX pwd fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	workingDir := t.TempDir()
	activities := Activities{
		Exec:          exectool.NewSafeExecutor(nil),
		WorkspaceRoot: dir,
	}
	result, err := activities.ExecuteTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_pwd",
			Type:       domain.ToolTypeExec,
			Intent:     "print working directory",
			Command:    "pwd",
			WorkingDir: workingDir,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_pwd",
				"work_item_id":    "work-item-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute exec: %v", err)
	}
	if result.WorkingDirectory != workingDir {
		t.Fatalf("expected working directory %q, got %q", workingDir, result.WorkingDirectory)
	}
	if !strings.Contains(result.Observation, workingDir) {
		t.Fatalf("expected observation to contain working directory %q, got %q", workingDir, result.Observation)
	}
	if result.Metadata["stdout_log_path"] == "" || result.Metadata["stderr_log_path"] == "" {
		t.Fatalf("expected exec log metadata, got %#v", result.Metadata)
	}
}

func TestWorkflowStepAttemptLogPathsAreAttemptSpecific(t *testing.T) {
	t.Parallel()

	stateDir := t.TempDir()
	stdoutPath, stderrPath := workflowStepAttemptLogPaths(stateDir, "finance-check", "run-1", "download", 2)
	if !strings.HasSuffix(filepath.ToSlash(stdoutPath), "/workflow-logs/finance-check/run-1/download/attempt-2/stdout.log") {
		t.Fatalf("unexpected stdout path: %s", stdoutPath)
	}
	if !strings.HasSuffix(filepath.ToSlash(stderrPath), "/workflow-logs/finance-check/run-1/download/attempt-2/stderr.log") {
		t.Fatalf("unexpected stderr path: %s", stderrPath)
	}
}

func TestExecuteExecPromotesLongCommandToProcess(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX exec fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	stateDir := t.TempDir()
	activities := Activities{
		Exec:          exectool.NewSafeExecutor(nil),
		WorkspaceRoot: dir,
		StateDir:      stateDir,
		ExecGrace:     20 * time.Millisecond,
	}
	result, err := activities.ExecuteTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_promote",
			Type:       domain.ToolTypeExec,
			Intent:     "run long fixture",
			Command:    "sh",
			Args:       []string{"-c", "printf 'ready\n'; sleep 30"},
			WorkingDir: dir,
			TimeoutMs:  1000,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_promote",
				"work_item_id":    "work-item-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("execute exec: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded || len(result.Processes) != 1 {
		t.Fatalf("expected promoted process result, got %#v", result)
	}
	processID := result.Processes[0].ID
	manager := exectool.NewProcessManager(nil)
	defer func() {
		_, _ = manager.Stop(context.Background(), stateDir, processID)
	}()
	checked, err := manager.Check(context.Background(), stateDir, processID)
	if err != nil {
		t.Fatalf("check process: %v", err)
	}
	if checked.Status != domain.ProcessStatusRunning {
		t.Fatalf("expected running process, got %#v", checked)
	}
	if result.Metadata["promoted_to_managed_process"] != "true" || result.Metadata["process_id"] != processID {
		t.Fatalf("expected promotion metadata, got %#v", result.Metadata)
	}
}

func TestNextActionStopsProcessWithStopOnFinishScopeAtCompletion(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX exec fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	stateDir := t.TempDir()
	activities := Activities{
		Engine: stubEngine{output: agent.NextActionOutput{
			NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
				ID:        "work-item-1",
				ProjectID: "project-1",
				Status:    domain.WorkItemStatusRunning,
			}}, ResponseMessage: "server is available"},
			Status: NextActionStatusCompleted,
		}},
		WorkspaceRoot: dir,
		StateDir:      stateDir,
	}
	started, err := activities.ExecuteTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID:   "toolu_bg",
			Type:         domain.ToolTypeExec,
			Intent:       "start server",
			Command:      "sh",
			Args:         []string{"-c", "printf 'ready\n'; sleep 30"},
			WorkingDir:   dir,
			TimeoutMs:    1000,
			RunMode:      domain.ToolRunModeStartBackground,
			ProcessScope: domain.ProcessScopeStopOnFinish,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_bg",
				"work_item_id":    "work-item-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("start exec process: %v", err)
	}
	processID := started.Metadata["process_id"]
	manager := exectool.NewProcessManager(nil)
	defer func() {
		_, _ = manager.Stop(context.Background(), stateDir, processID)
	}()
	checked, err := manager.Check(context.Background(), stateDir, processID)
	if err != nil {
		t.Fatalf("check started process: %v", err)
	}
	if checked.Status != domain.ProcessStatusRunning {
		t.Fatalf("expected background process to be running before completion, got %#v", checked)
	}

	result, err := activities.NextAction(context.Background(), NextActionRequest{
		ProjectID:      "project-1",
		Event:          domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run server"},
		Processes:      started.Processes,
		ExecutionCycle: 2,
	})
	if err != nil {
		t.Fatalf("next action: %v", err)
	}
	if result.Status != NextActionStatusCompleted {
		t.Fatalf("expected completed status, got %#v", result)
	}
	if len(result.Processes) != 1 || result.Processes[0].Status != domain.ProcessStatusStopped {
		t.Fatalf("expected stopped process reference, got %#v", result.Processes)
	}
	checked, err = manager.Check(context.Background(), stateDir, processID)
	if err != nil {
		t.Fatalf("check stopped process: %v", err)
	}
	if checked.Status != domain.ProcessStatusStopped {
		t.Fatalf("expected background process to be stopped after completion, got %#v", checked)
	}
	if len(result.NextAction.ResponseMessage) == 0 {
		t.Fatalf("expected response message")
	}
	if !strings.Contains(result.NextAction.ResponseMessage, "server is available") || !strings.Contains(result.NextAction.ResponseMessage, "stopped stop-on-finish background process") {
		t.Fatalf("expected cleanup notice in response, got %q", result.NextAction.ResponseMessage)
	}
}

func TestPrepareWorkflowRunUsesExecutionRunID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	workflowID := "finance-check"
	workflowDir, err := workflowbundle.WorkflowDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest := workflowbundle.Manifest{
		Name:        "finance check",
		Description: "",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 9 * * *",
			OneShotAt:     "",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "10m",
		},
		NotificationPolicy: workflowbundle.NotificationPolicy{OnFailure: true},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "sh",
			Args:                []string{"src/check.sh"},
			StartToCloseTimeout: "1m",
			RetryPolicy: workflowbundle.RetryPolicy{
				NonRetryableErrorTypes: []string{},
			},
		}},
	}
	files := []workflowbundle.File{{
		Path:       "src/check.sh",
		Content:    "echo ok\n",
		Executable: true,
	}}
	if err := workflowbundle.WriteBundle(ctx, workflowDir, manifest, files); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	commitHash, err := workflowbundle.CommitBundle(ctx, workflowDir, "initial", files)
	if err != nil {
		t.Fatalf("commit bundle: %v", err)
	}

	result, err := (&Activities{WorkspaceRoot: workspaceRoot}).PrepareWorkflowRun(ctx, workflowrun.PrepareRequest{
		Input: workflowrun.Input{
			ProjectID:   "project-1",
			WorkflowID:  workflowID,
			CommitHash:  commitHash,
			ScheduledAt: time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		},
		TemporalWorkflowID: "workflow-execution-id",
		TemporalRunID:      "actual-run-id",
	})
	if err != nil {
		t.Fatalf("prepare workflow run: %v", err)
	}
	if result.RunID != "actual-run-id" {
		t.Fatalf("expected run id to use execution run id, got %q", result.RunID)
	}
	wantRunPath, err := workflowbundle.WorkflowRunDir(workspaceRoot, workflowID, "actual-run-id")
	if err != nil {
		t.Fatalf("workflow run dir: %v", err)
	}
	if result.RunPath != wantRunPath {
		t.Fatalf("expected run path %q, got %q", wantRunPath, result.RunPath)
	}
	if _, err := os.Stat(filepath.Join(result.RunPath, workflowbundle.ManifestFilename)); err != nil {
		t.Fatalf("expected archived manifest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.RunPath, "src", "check.sh")); err != nil {
		t.Fatalf("expected archived source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(result.RunPath, "artifacts")); err != nil {
		t.Fatalf("expected artifacts directory: %v", err)
	}
	dataDir, err := workflowbundle.WorkflowDataDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow data dir: %v", err)
	}
	if _, err := os.Stat(dataDir); err != nil {
		t.Fatalf("expected workflow data directory: %v", err)
	}
}

func TestPrepareWorkflowRunUsesPublishedLocalSource(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	workspaceRoot := t.TempDir()
	workflowID := "finance-check"
	workflowDir, err := workflowbundle.WorkflowDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow dir: %v", err)
	}
	manifest := workflowbundle.Manifest{
		Name: "finance check",
		Schedule: workflowbundle.Schedule{
			Cron:          "0 9 * * *",
			OverlapPolicy: workflowbundle.OverlapPolicySkip,
			CatchupWindow: "10m",
		},
		Steps: []workflowbundle.Step{{
			ID:                  "check",
			Command:             "sh",
			Args:                []string{"src/check.sh"},
			StartToCloseTimeout: "1m",
		}},
	}
	if err := workflowbundle.WriteBundle(ctx, workflowDir, manifest, []workflowbundle.File{{
		Path:       "src/check.sh",
		Content:    "echo old\n",
		Executable: true,
	}}); err != nil {
		t.Fatalf("write bundle: %v", err)
	}
	oldCommit, err := workflowbundle.CommitBundle(ctx, workflowDir, "initial", nil)
	if err != nil {
		t.Fatalf("commit old bundle: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workflowDir, "src", "check.sh"), []byte("echo new\n"), 0o755); err != nil {
		t.Fatalf("edit source: %v", err)
	}
	newCommit, err := workflowbundle.CommitBundle(ctx, workflowDir, "manual update", nil)
	if err != nil {
		t.Fatalf("commit new bundle: %v", err)
	}
	schedule := &fakeScheduleExecutor{result: scheduletool.Result{
		WorkflowID:   workflowID,
		CommitHash:   newCommit,
		WorkflowPath: workflowDir,
	}}

	result, err := (&Activities{WorkspaceRoot: workspaceRoot, Schedule: schedule}).PrepareWorkflowRun(ctx, workflowrun.PrepareRequest{
		Input: workflowrun.Input{
			ProjectID:   "project-1",
			WorkflowID:  workflowID,
			CommitHash:  oldCommit,
			ScheduledAt: time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC),
		},
		TemporalWorkflowID: "workflow-execution-id",
		TemporalRunID:      "actual-run-id",
	})
	if err != nil {
		t.Fatalf("prepare workflow run: %v", err)
	}
	if schedule.publishRequest.WorkflowID != workflowID || schedule.publishRequest.ProjectID != "project-1" {
		t.Fatalf("unexpected publish request: %#v", schedule.publishRequest)
	}
	if result.CommitHash != newCommit {
		t.Fatalf("expected prepared commit %q, got %q", newCommit, result.CommitHash)
	}
	source, err := os.ReadFile(filepath.Join(result.RunPath, "src", "check.sh"))
	if err != nil {
		t.Fatalf("read archived source: %v", err)
	}
	if string(source) != "echo new\n" {
		t.Fatalf("expected archived latest source, got %q", string(source))
	}
}

func TestCleanupWorkflowRunsKeepsLatestTenSnapshots(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	workflowID := "finance-check"
	runsDir, err := workflowbundle.WorkflowRunsDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow runs dir: %v", err)
	}
	baseTime := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		runID := fmt.Sprintf("run-%02d", i)
		runDir := filepath.Join(runsDir, runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatalf("mkdir run %s: %v", runID, err)
		}
		modTime := baseTime.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(runDir, modTime, modTime); err != nil {
			t.Fatalf("chtimes run %s: %v", runID, err)
		}
	}

	result, err := (&Activities{WorkspaceRoot: workspaceRoot}).CleanupWorkflowRuns(context.Background(), workflowrun.CleanupRunsRequest{
		WorkflowID:   workflowID,
		CurrentRunID: "run-11",
		KeepLast:     workflowrun.DefaultRunRetention,
	})
	if err != nil {
		t.Fatalf("cleanup workflow runs: %v", err)
	}
	if len(result.DeletedRunIDs) != 2 {
		t.Fatalf("expected two deleted runs, got %#v", result.DeletedRunIDs)
	}
	for _, runID := range []string{"run-00", "run-01"} {
		if _, err := os.Stat(filepath.Join(runsDir, runID)); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be deleted, stat err=%v", runID, err)
		}
	}
	for _, runID := range []string{"run-02", "run-11"} {
		if _, err := os.Stat(filepath.Join(runsDir, runID)); err != nil {
			t.Fatalf("expected %s to be kept: %v", runID, err)
		}
	}
}

func TestCleanupWorkflowRunsDoesNotDeleteActiveOlderSnapshot(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	workflowID := "finance-check"
	runsDir, err := workflowbundle.WorkflowRunsDir(workspaceRoot, workflowID)
	if err != nil {
		t.Fatalf("workflow runs dir: %v", err)
	}
	baseTime := time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 12; i++ {
		runID := fmt.Sprintf("run-%02d", i)
		runDir := filepath.Join(runsDir, runID)
		if err := os.MkdirAll(runDir, 0o755); err != nil {
			t.Fatalf("mkdir run %s: %v", runID, err)
		}
		modTime := baseTime.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(runDir, modTime, modTime); err != nil {
			t.Fatalf("chtimes run %s: %v", runID, err)
		}
	}

	store := stubProjectStore{workflowRuns: map[string]domain.ScheduledWorkflowRun{
		"project-1/run-00": {
			ID:         "run-00",
			ProjectID:  "project-1",
			WorkflowID: workflowID,
			Status:     domain.ExecutionStatusRunning,
		},
	}}
	result, err := (&Activities{WorkspaceRoot: workspaceRoot, Store: store}).CleanupWorkflowRuns(context.Background(), workflowrun.CleanupRunsRequest{
		ProjectID:    "project-1",
		WorkflowID:   workflowID,
		CurrentRunID: "run-11",
		KeepLast:     workflowrun.DefaultRunRetention,
	})
	if err != nil {
		t.Fatalf("cleanup workflow runs: %v", err)
	}
	if len(result.DeletedRunIDs) != 1 || result.DeletedRunIDs[0] != "run-01" {
		t.Fatalf("expected only oldest inactive run to be deleted, got %#v", result.DeletedRunIDs)
	}
	if _, err := os.Stat(filepath.Join(runsDir, "run-00")); err != nil {
		t.Fatalf("expected active old run to be kept: %v", err)
	}
}

func TestWorkflowStepEnvironmentSetsRunPaths(t *testing.T) {
	t.Setenv("OPENCTO_WORKSPACE", "/inherited")
	t.Setenv("OPENCTO_RUN_DIR", "/old-run")
	t.Setenv("PATH", "/usr/bin")

	runPath := filepath.Join(t.TempDir(), "run-1")
	request := workflowrun.ExecuteStepRequest{
		WorkflowID: "finance-check",
		RunID:      "run-1",
		RunPath:    runPath,
	}
	artifactsDir := filepath.Join(runPath, "artifacts")

	env, err := workflowStepEnvironment("/workspace", "/opencto", request)
	if err != nil {
		t.Fatalf("workflow step environment: %v", err)
	}
	for name, want := range map[string]string{
		"OPENCTO_WORKSPACE":                  "/workspace",
		"OPENCTO_WORKFLOWS_DIR":              filepath.Join("/workspace", "workflows"),
		"OPENCTO_WORKFLOW_RUN_DIR":           runPath,
		"OPENCTO_WORKFLOW_DATA_DIR":          filepath.Join("/workspace", "workflows", "finance-check", "data"),
		"OPENCTO_WORKFLOW_RUN_ARTIFACTS_DIR": artifactsDir,
		"OPENCTO_ROOT":                       "/opencto",
	} {
		if got := envValue(env, name); got != want {
			t.Fatalf("expected %s=%q, got %q", name, want, got)
		}
	}
	wantPath := filepath.Join("/workspace", "bin") + string(os.PathListSeparator) + "/usr/bin"
	if got := envValue(env, "PATH"); got != wantPath {
		t.Fatalf("expected PATH=%q, got %q", wantPath, got)
	}
	for _, name := range []string{"OPENCTO_RUN_DIR"} {
		if got := envValue(env, name); got != "" {
			t.Fatalf("expected %s to be stripped, got %q", name, got)
		}
	}
}

func TestExecuteWorkflowStepCreatesStepArtifactDirectory(t *testing.T) {
	t.Parallel()

	workspaceRoot := t.TempDir()
	runPath := filepath.Join(workspaceRoot, "workflow-runs", "finance-check", "run-1")
	result, err := (&Activities{WorkspaceRoot: workspaceRoot}).ExecuteWorkflowStep(context.Background(), workflowrun.ExecuteStepRequest{
		ProjectID:  "project-1",
		WorkflowID: "finance-check",
		RunID:      "run-1",
		RunPath:    runPath,
		Step: workflowbundle.Step{
			ID:      "check",
			Command: "sh",
			Args: []string{
				"-c",
				`dir="$OPENCTO_WORKFLOW_RUN_ARTIFACTS_DIR" && test -d "$dir" && printf '{"ok":true}\n' > "$dir/payload.json"`,
			},
		},
	})
	if err != nil {
		t.Fatalf("execute workflow step: %v", err)
	}
	if result.ExitCode != 0 {
		t.Fatalf("expected successful step, got %#v", result)
	}
	artifactPath := filepath.Join(runPath, "artifacts", "payload.json")
	data, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read step artifact: %v", err)
	}
	if strings.TrimSpace(string(data)) != `{"ok":true}` {
		t.Fatalf("unexpected artifact: %q", string(data))
	}
	if _, err := os.Stat(result.StdoutLogPath); err != nil {
		t.Fatalf("expected attempt stdout log: %v", err)
	}
	if _, err := os.Stat(result.StderrLogPath); err != nil {
		t.Fatalf("expected attempt stderr log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runPath, "steps")); !os.IsNotExist(err) {
		t.Fatalf("expected no step directory, stat err=%v", err)
	}
}

func envValue(env []string, name string) string {
	prefix := name + "="
	value := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			value = strings.TrimPrefix(entry, prefix)
		}
	}
	return value
}

func TestWorkflowStepFailureMessageIncludesCommandOutputAndLogs(t *testing.T) {
	t.Parallel()

	message := workflowStepFailureMessage(workflowrun.StepFailure{
		StepID:        "check_and_append",
		Command:       "go",
		Args:          []string{"finance2049"},
		ExitCode:      2,
		Error:         "exit status 2",
		OutputSummary: "stderr:\ngo finance2049: unknown command\n",
		StdoutLogPath: "/tmp/stdout.log",
		StderrLogPath: "/tmp/stderr.log",
	})
	for _, want := range []string{
		`workflow step "check_and_append" failed with exit code 2`,
		"command: go finance2049",
		"error: exit status 2",
		"go finance2049: unknown command",
		"stderr_log: /tmp/stderr.log",
		"stdout_log: /tmp/stdout.log",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected failure message to contain %q, got:\n%s", want, message)
		}
	}
}

func executeRequest(toolType domain.ToolType, callID string, input map[string]any) ExecuteToolRequest {
	encoded, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "luka",
			Body:        "schedule something",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID:   callID,
			Type:         toolType,
			Intent:       string(toolType) + " fixture",
			Input:        json.RawMessage(encoded),
			InputSummary: string(toolType) + " fixture",
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    callID,
			},
		},
	}
}

func writeActivitySkill(t *testing.T, root, id, content string) {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, skillcatalog.SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill fixture: %v", err)
	}
}

type fakeGrepExecutor struct {
	result greptool.Result
	err    error
}

func (f fakeGrepExecutor) Run(context.Context, greptool.Request) (greptool.Result, error) {
	return f.result, f.err
}

type fakeScheduleExecutor struct {
	result           scheduletool.Result
	err              error
	createRequest    scheduletool.CreateRequest
	updateRequest    scheduletool.UpdateRequest
	deleteRequest    scheduletool.DeleteRequest
	operationRequest scheduletool.OperationRequest
	publishRequest   scheduletool.UpdateRequest
}

func (f *fakeScheduleExecutor) Create(_ context.Context, req scheduletool.CreateRequest) (scheduletool.Result, error) {
	f.createRequest = req
	return f.result, f.err
}

func (f *fakeScheduleExecutor) Update(_ context.Context, req scheduletool.UpdateRequest) (scheduletool.Result, error) {
	f.updateRequest = req
	return f.result, f.err
}

func (f *fakeScheduleExecutor) Delete(_ context.Context, req scheduletool.DeleteRequest) (scheduletool.Result, error) {
	f.deleteRequest = req
	return f.result, f.err
}

func (f *fakeScheduleExecutor) Operation(_ context.Context, req scheduletool.OperationRequest) (scheduletool.Result, error) {
	f.operationRequest = req
	return f.result, f.err
}

func (f *fakeScheduleExecutor) PublishCurrentSource(_ context.Context, req scheduletool.UpdateRequest) (scheduletool.Result, error) {
	f.publishRequest = req
	return f.result, f.err
}
