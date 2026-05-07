package activities

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	skillcatalog "github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/storage"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	scheduletool "github.com/opencto/opencto/internal/tools/schedule"
)

type stubProjectStore struct {
	pending              []domain.WorkItem
	memories             []domain.Memory
	forgetResult         domain.MemoryForgetResult
	forgetRequests       *[]domain.MemoryForgetRequest
	conversationsByScope map[storage.ConversationScope][]domain.ConversationMessage
	conversationQueries  *[]storage.ConversationQuery
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

type captureReporter struct {
	reports      []domain.ReportMessage
	messages     []string
	typingEvents []domain.Event
	typingErr    error
	onTyping     func()
}

func (r *captureReporter) Report(_ context.Context, _ domain.Event, report domain.ReportMessage) error {
	r.reports = append(r.reports, report)
	r.messages = append(r.messages, report.Text)
	return nil
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
	limit := storage.DefaultConversationHistoryLimit(query.Limit)
	if limit > len(source) {
		limit = len(source)
	}
	return append([]domain.ConversationMessage(nil), source[:limit]...), nil
}

func (s stubProjectStore) RememberMemory(context.Context, domain.Memory) (domain.Memory, error) {
	return domain.Memory{}, nil
}

func (s stubProjectStore) SearchMemories(context.Context, domain.MemorySearchRequest) ([]domain.Memory, error) {
	return append([]domain.Memory(nil), s.memories...), nil
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

func TestReportResponseIncludesAttachments(t *testing.T) {
	t.Parallel()

	reporter := &captureReporter{}
	activities := Activities{Reporter: reporter}
	err := activities.ReportResponse(context.Background(), ReportResponseRequest{
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
	if len(reporter.reports) != 1 {
		t.Fatalf("expected one report, got %d", len(reporter.reports))
	}
	report := reporter.reports[0]
	if report.Text != "see attached" || len(report.Attachments) != 1 || report.Attachments[0].Path != "/workspace/screenshot.png" {
		t.Fatalf("unexpected report: %#v", report)
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

func TestLoadContextIncludesBoundedMemoryWhenEnabled(t *testing.T) {
	t.Parallel()

	memory := domain.Memory{
		ID:        "memory-1",
		ProjectID: "default",
		Scope:     domain.MemoryScopeProject,
		Kind:      "preference",
		Content:   "Use SQLite for local storage.",
	}
	activities := Activities{
		Store:         stubProjectStore{memories: []domain.Memory{memory}},
		Project:       domain.Project{ID: "default", Name: "OpenCTO"},
		MemoryEnabled: true,
		MemoryLimit:   5,
	}
	loaded, err := activities.LoadContext(context.Background(), domain.Event{
		ID:        "event-1",
		ProjectID: "default",
		Body:      "what storage should we use?",
	})
	if err != nil {
		t.Fatalf("load context: %v", err)
	}
	if len(loaded.Memory) != 1 || loaded.Memory[0].ID != "memory-1" {
		t.Fatalf("expected memory to be loaded, got %#v", loaded.Memory)
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
			Type:       domain.ToolTypeMemoryForget,
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
	if len(request.Scopes) != 0 {
		t.Fatalf("unexpected forget scopes: %#v", request.Scopes)
	}
	if len(request.Tags) != 0 {
		t.Fatalf("unexpected forget tags: %#v", request.Tags)
	}
	if result.Metadata["deleted_count"] != "2" || !strings.Contains(result.Observation, "deleted_count: 2") {
		t.Fatalf("unexpected forget result: %#v", result)
	}
}

func TestExecuteMemoryToolRejectsMixedForgetSelectors(t *testing.T) {
	t.Parallel()

	activities := Activities{
		Store:         stubProjectStore{},
		MemoryEnabled: true,
	}
	_, err := activities.ExecuteMemoryTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "default",
		WorkItemID: "work-1",
		Event: domain.Event{
			ID:        "event-1",
			ProjectID: "default",
		},
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_memory",
			Type:       domain.ToolTypeMemoryForget,
			Intent:     "forget cleanup memories",
			Input:      []byte(`{"memory_ids":["memory-1"],"tags":["cleanup"],"scope":"all"}`),
			Metadata: map[string]string{
				"execution_cycle": "1",
			},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "exactly one selector") {
		t.Fatalf("expected mixed selector error, got %v", err)
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
	if len(queries) != 2 || queries[0].Scope != storage.ConversationScopeThread || queries[1].Scope != storage.ConversationScopeChannel {
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
