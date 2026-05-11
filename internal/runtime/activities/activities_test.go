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

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/embedding"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/storage"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	scheduletool "github.com/opencto/opencto/internal/tools/schedule"
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
	summariesByScope     map[domain.ConversationSummaryScope][]domain.ConversationSummary
	summaryQueries       *[]storage.ConversationSummaryQuery
	upsertedSummaries    *[]domain.ConversationSummary
	upsertedThreads      *[]domain.ConversationThread
	upsertedConversation *[]domain.ConversationMessage
}

type stubEngine struct {
	output agent.NextActionOutput
	err    error
	input  *agent.NextActionInput
}

func (e stubEngine) NextAction(_ context.Context, input agent.NextActionInput) (agent.NextActionOutput, error) {
	if e.input != nil {
		*e.input = input
	}
	return e.output, e.err
}

type stubMemoryExtractor struct {
	output agent.MemoryExtractionOutput
	err    error
	input  *agent.MemoryExtractionInput
}

func (e stubMemoryExtractor) ExtractMemories(_ context.Context, input agent.MemoryExtractionInput) (agent.MemoryExtractionOutput, error) {
	if e.input != nil {
		*e.input = input
	}
	return e.output, e.err
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

func TestExtractMemoryStoresAutoCandidate(t *testing.T) {
	t.Parallel()

	remembered := []domain.Memory{}
	searchRequests := []domain.MemorySearchRequest{}
	var extractorInput agent.MemoryExtractionInput
	activities := Activities{
		Store: stubProjectStore{
			memories: []domain.Memory{{
				ID:      "memory-existing",
				Scope:   domain.MemoryScopeProject,
				Kind:    "instruction",
				Content: "Existing project instruction.",
			}},
			remembered:     &remembered,
			searchRequests: &searchRequests,
		},
		MemoryEnabled:            true,
		MemoryAutoExtractEnabled: true,
		MemoryExtractor: stubMemoryExtractor{
			input: &extractorInput,
			output: agent.MemoryExtractionOutput{Candidates: []agent.MemoryCandidate{{
				Scope:      domain.MemoryScopeUser,
				Kind:       "preference",
				Content:    "The user prefers short implementation plans before code changes.",
				Tags:       []string{"planning"},
				Confidence: 0.8,
				Reason:     "The user gave a durable collaboration preference.",
			}}},
		},
	}

	result, err := activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			Kind:        domain.EventKindMessage,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ActorID:     "actor-1",
			ActorName:   "Luka",
			Body:        "before coding, give me a short plan",
		},
	})
	if err != nil {
		t.Fatalf("extract memory: %v", err)
	}
	if result.Candidates != 1 || result.Remembered != 1 || result.Rejected != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(searchRequests) != 1 {
		t.Fatalf("expected one memory search request, got %#v", searchRequests)
	}
	if searchRequests[0].UserID != "discord:actor-1" {
		t.Fatalf("expected discord user id in search, got %#v", searchRequests[0])
	}
	if len(extractorInput.ExistingMemories) != 1 {
		t.Fatalf("expected existing memories to be passed to extractor, got %#v", extractorInput.ExistingMemories)
	}
	if len(remembered) != 1 {
		t.Fatalf("expected remembered memory, got %#v", remembered)
	}
	memory := remembered[0]
	if memory.Scope != domain.MemoryScopeUser || memory.UserID != "discord:actor-1" || memory.Source != "auto_memory" || memory.SourceID != "event-1" {
		t.Fatalf("unexpected remembered memory: %#v", memory)
	}
	if memory.Metadata["reason"] != "The user gave a durable collaboration preference." || memory.Metadata["actor_name"] != "Luka" {
		t.Fatalf("unexpected memory metadata: %#v", memory.Metadata)
	}
}

func TestExtractMemoryStoresThreadScopedCandidate(t *testing.T) {
	t.Parallel()

	remembered := []domain.Memory{}
	searchRequests := []domain.MemorySearchRequest{}
	activities := Activities{
		Store: stubProjectStore{
			remembered:     &remembered,
			searchRequests: &searchRequests,
		},
		MemoryEnabled:            true,
		MemoryAutoExtractEnabled: true,
		MemoryExtractor: stubMemoryExtractor{
			output: agent.MemoryExtractionOutput{Candidates: []agent.MemoryCandidate{{
				Scope:      domain.MemoryScopeThread,
				Kind:       "decision",
				Content:    "Use a compact orange theme in this thread.",
				Tags:       []string{"theme"},
				Confidence: 0.8,
			}}},
		},
	}

	result, err := activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			Kind:        domain.EventKindMessage,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "thread-1",
			ThreadID:    "thread-1",
			ActorID:     "actor-1",
			Body:        "use orange here",
		},
	})
	if err != nil {
		t.Fatalf("extract memory: %v", err)
	}
	if result.Remembered != 1 || len(remembered) != 1 {
		t.Fatalf("expected remembered thread memory, got result=%#v memories=%#v", result, remembered)
	}
	if remembered[0].Scope != domain.MemoryScopeThread || remembered[0].ChannelID != "thread-1" || remembered[0].ThreadID != "thread-1" {
		t.Fatalf("unexpected remembered thread memory: %#v", remembered[0])
	}
	firstMemoryID := remembered[0].ID
	_, err = activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:          "event-2",
			ProjectID:   "project-1",
			Kind:        domain.EventKindMessage,
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "thread-2",
			ThreadID:    "thread-2",
			ActorID:     "actor-1",
			Body:        "use orange here too",
		},
	})
	if err != nil {
		t.Fatalf("extract second thread memory: %v", err)
	}
	if len(remembered) != 2 || remembered[1].ID == firstMemoryID {
		t.Fatalf("thread-scoped memories should include thread id in stable identity: %#v", remembered)
	}
	if len(searchRequests) != 2 ||
		searchRequests[0].ChannelID != "thread-1" ||
		searchRequests[0].ThreadID != "thread-1" ||
		searchRequests[1].ChannelID != "thread-2" ||
		searchRequests[1].ThreadID != "thread-2" ||
		len(searchRequests[0].Scopes) != 5 ||
		searchRequests[0].Scopes[0] != domain.MemoryScopeThread ||
		searchRequests[0].Scopes[1] != domain.MemoryScopeChannel ||
		searchRequests[0].Scopes[2] != domain.MemoryScopeProject ||
		searchRequests[0].Scopes[3] != domain.MemoryScopeUser ||
		searchRequests[0].Scopes[4] != domain.MemoryScopeGlobal {
		t.Fatalf("expected thread context memory search, got %#v", searchRequests)
	}
}

func TestExtractMemorySkipsControlMessages(t *testing.T) {
	t.Parallel()

	var extractorInput agent.MemoryExtractionInput
	activities := Activities{
		Store:                    stubProjectStore{},
		MemoryEnabled:            true,
		MemoryAutoExtractEnabled: true,
		MemoryExtractor:          stubMemoryExtractor{input: &extractorInput},
	}
	result, err := activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Kind:      domain.EventKindMessage,
			Body:      "cancel",
			Metadata:  domain.Metadata{domain.MetadataKeyControl: "cancel"},
		},
	})
	if err != nil {
		t.Fatalf("extract memory: %v", err)
	}
	if result != (ExtractMemoryResult{}) {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if extractorInput.Event.ID != "" {
		t.Fatalf("expected extractor to be skipped, got %#v", extractorInput)
	}
}

func TestExtractMemorySkipsAttachmentOnlyFallback(t *testing.T) {
	t.Parallel()

	var extractorInput agent.MemoryExtractionInput
	activities := Activities{
		Store:                    stubProjectStore{},
		MemoryEnabled:            true,
		MemoryAutoExtractEnabled: true,
		MemoryExtractor:          stubMemoryExtractor{input: &extractorInput},
	}
	result, err := activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Kind:      domain.EventKindMessage,
			Body:      "Uploaded attachment(s): screenshot.png (image/png)",
		},
	})
	if err != nil {
		t.Fatalf("extract memory: %v", err)
	}
	if result != (ExtractMemoryResult{}) {
		t.Fatalf("expected empty result, got %#v", result)
	}
	if extractorInput.Event.ID != "" {
		t.Fatalf("expected extractor to be skipped, got %#v", extractorInput)
	}
}

func TestExtractMemoryTreatsPolicyRejectionAsNonFatal(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Store: stubProjectStore{
			rememberErr: fmt.Errorf("%w: content appears to describe temporary task state", storage.ErrMemoryPolicyRejected),
		},
		MemoryEnabled:            true,
		MemoryAutoExtractEnabled: true,
		MemoryExtractor: stubMemoryExtractor{
			output: agent.MemoryExtractionOutput{Candidates: []agent.MemoryCandidate{{
				Scope:   domain.MemoryScopeProject,
				Kind:    "fact",
				Content: "Use this temporary migration approach today.",
			}}},
		},
	}
	result, err := activities.ExtractMemory(context.Background(), ExtractMemoryRequest{
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Kind:      domain.EventKindMessage,
			Body:      "use this temporary migration approach today",
		},
	})
	if err != nil {
		t.Fatalf("extract memory: %v", err)
	}
	if result.Candidates != 1 || result.Remembered != 0 || result.Rejected != 1 {
		t.Fatalf("unexpected result: %#v", result)
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
		ScheduleID: "opencto:project-1:schedule:daily-hello",
		Name:       "daily hello",
		Kind:       "recurring",
		TimeZone:   "Asia/Tbilisi",
		Cron:       "0 9 * * *",
		Message:    "schedule created",
	}}
	activities.Schedule = scheduleExecutor
	scheduleResult, err := activities.ExecuteTool(ctx, executeRequest(domain.ToolTypeSchedule, "schedule-1", map[string]any{
		"operation":         "create",
		"schedule_id":       "",
		"name":              "daily hello",
		"description":       "",
		"task":              "send hello",
		"one_shot_at":       "",
		"cron":              "0 9 * * *",
		"paused":            false,
		"note":              "",
		"limit":             0,
		"include_completed": false,
	}))
	if err != nil {
		t.Fatalf("schedule tool: %v", err)
	}
	if scheduleResult.Status != domain.ExecutionStatusSucceeded ||
		scheduleResult.Metadata["schedule_id"] != "opencto:project-1:schedule:daily-hello" ||
		scheduleExecutor.request.SourceEvent.ChannelID != "channel-1" {
		t.Fatalf("unexpected schedule result: %#v request=%#v", scheduleResult, scheduleExecutor.request)
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
			ToolChoice: &agent.ToolChoice{
				ToolCallID: "toolu_next",
				Type:       domain.ToolTypeExec,
				Intent:     "inspect workspace",
				Command:    "pwd",
				Metadata: map[string]string{
					"tool_call_id": "toolu_next",
					"work_item_id": "model-supplied-work-item",
				},
			},
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
	if result.ToolChoice == nil || result.ToolChoice.Metadata["work_item_id"] != wantWorkItemID {
		t.Fatalf("expected internal work item metadata, got %#v", result.ToolChoice)
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
		ChannelType: domain.ChannelTypeLocal,
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
	if input.ChannelType != domain.ChannelTypeLocal {
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
	activities := Activities{
		WorkspaceRoot: dir,
		StateDir:      t.TempDir(),
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
	result  scheduletool.Result
	err     error
	request scheduletool.Request
}

func (f *fakeScheduleExecutor) Run(_ context.Context, req scheduletool.Request) (scheduletool.Result, error) {
	f.request = req
	return f.result, f.err
}
