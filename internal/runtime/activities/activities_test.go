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
	greptool "github.com/opencto/opencto/internal/tools/grep"
	shelltool "github.com/opencto/opencto/internal/tools/shell"
)

type stubProjectStore struct {
	pending []domain.WorkItem
}

type stubEngine struct {
	output agent.NextActionOutput
	err    error
}

func (e stubEngine) NextAction(context.Context, agent.NextActionInput) (agent.NextActionOutput, error) {
	return e.output, e.err
}

type captureReporter struct {
	messages     []string
	typingEvents []domain.Event
	typingErr    error
	onTyping     func()
}

func (r *captureReporter) Report(_ context.Context, _ domain.Event, message string) error {
	r.messages = append(r.messages, message)
	return nil
}

func (r *captureReporter) NotifyTyping(_ context.Context, event domain.Event) error {
	r.typingEvents = append(r.typingEvents, event)
	if r.onTyping != nil {
		r.onTyping()
	}
	return r.typingErr
}

func (s stubProjectStore) Append(context.Context, domain.Event) error {
	return nil
}

func (s stubProjectStore) ListPending(context.Context, string) ([]domain.WorkItem, error) {
	return append([]domain.WorkItem(nil), s.pending...), nil
}

func (s stubProjectStore) UpsertWorkItem(context.Context, domain.WorkItem) error {
	return nil
}

func (s stubProjectStore) UpsertExecutionAttempt(context.Context, domain.ExecutionAttempt) error {
	return nil
}

func (s stubProjectStore) UpsertToolInvocation(context.Context, domain.ToolInvocation) error {
	return nil
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

func TestRuntimeStateDirDefaultsToOpenCTOState(t *testing.T) {
	t.Parallel()

	got := (&Activities{}).runtimeStateDir("project-1")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolve user home: %v", err)
	}
	want := filepath.Join(home, ".opencto", ".state")
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
}

func TestExecuteToolReturnsManagedProcessMetadata(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX shell fixture")
	}
	t.Parallel()

	dir := t.TempDir()
	stateDir := t.TempDir()
	activities := Activities{
		WorkspaceRoot: dir,
		StateDir:      stateDir,
	}
	request := ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID:  "toolu_bg",
			Type:        domain.ToolTypeShell,
			Intent:      "start background fixture",
			Command:     "sh",
			Args:        []string{"-c", "printf 'ready\n'; sleep 30"},
			WorkingDir:  dir,
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
		t.Fatalf("start shell process: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("unexpected result: %#v", result)
	}
	processID := result.Metadata["process_id"]
	if processID == "" || result.Metadata["pid"] == "" {
		t.Fatalf("expected process metadata, got %#v", result.Metadata)
	}
	if len(result.Processes) != 1 || result.Processes[0].ID != processID || result.Processes[0].Scope != domain.ProcessScopeTask {
		t.Fatalf("expected process reference, got %#v", result.Processes)
	}
	manager := shelltool.NewProcessManager(nil)
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

func TestNextActionReturnsResponseAfterTaskProcessCleanup(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("uses POSIX shell fixture")
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
			}}},
			FinalAnswer: "server is available",
			Status:      NextActionStatusCompleted,
		}},
		WorkspaceRoot: dir,
		StateDir:      stateDir,
	}
	started, err := activities.ExecuteTool(context.Background(), ExecuteToolRequest{
		ProjectID:  "project-1",
		WorkItemID: "work-item-1",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_bg",
			Type:       domain.ToolTypeShell,
			Intent:     "start server",
			Command:    "sh",
			Args:       []string{"-c", "printf 'ready\n'; sleep 30"},
			WorkingDir: dir,
			TimeoutMs:  1000,
			RunMode:    domain.ToolRunModeStartBackground,
			Metadata: map[string]string{
				"execution_cycle": "1",
				"tool_call_id":    "toolu_bg",
				"work_item_id":    "work-item-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("start shell process: %v", err)
	}
	processID := started.Metadata["process_id"]
	defer func() {
		_, _ = shelltool.NewProcessManager(nil).Stop(context.Background(), stateDir, processID)
	}()

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
	if len(result.NextAction.ResponseMessage) == 0 {
		t.Fatalf("expected response message")
	}
	if !strings.Contains(result.NextAction.ResponseMessage, "server is available") || !strings.Contains(result.NextAction.ResponseMessage, "stopped task-scoped background process") {
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
