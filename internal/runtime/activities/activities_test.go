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
	greptool "github.com/opencto/opencto/internal/tools/grep"
)

type stubProjectStore struct {
	pending []domain.WorkItem
}

func (s stubProjectStore) Append(context.Context, domain.Event) error {
	return nil
}

func (s stubProjectStore) ListByProject(context.Context, string, int) ([]domain.Event, error) {
	return nil, nil
}

func (s stubProjectStore) ListPending(context.Context, string) ([]domain.WorkItem, error) {
	return append([]domain.WorkItem(nil), s.pending...), nil
}

func (s stubProjectStore) UpsertWorkItem(context.Context, domain.WorkItem) error {
	return nil
}

func (s stubProjectStore) GetWorkItem(context.Context, string, string) (domain.WorkItem, error) {
	return domain.WorkItem{}, nil
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

func TestLoadContextReturnsProjectAndActiveWorkItems(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 4, 23, 17, 31, 0, 0, time.UTC)
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
		Store:   stubProjectStore{pending: []domain.WorkItem{workItem}},
		Project: domain.Project{ID: "default", Name: "OpenCTO"},
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
}

func TestExecuteToolRunsDedicatedFileTools(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	filePath := filepath.Join(dir, "example.txt")
	if err := os.WriteFile(filePath, []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	activities := Activities{
		WorkspaceRoot: dir,
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
}

func TestStartShellProcessReturnsManagedProcessMetadata(t *testing.T) {
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
	result, err := activities.StartShellProcess(context.Background(), request)
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
	defer func() {
		_, _ = activities.StopShellProcess(context.Background(), ProcessRequest{ProjectID: "project-1", ProcessID: processID})
	}()

	checked, err := activities.CheckShellProcess(context.Background(), ProcessRequest{ProjectID: "project-1", ProcessID: processID})
	if err != nil {
		t.Fatalf("check process: %v", err)
	}
	if checked.Status != domain.ProcessStatusRunning {
		t.Fatalf("expected running process, got %#v", checked)
	}
	var stdoutTail string
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		result, err := activities.ReadShellProcessLogs(context.Background(), ProcessRequest{ProjectID: "project-1", ProcessID: processID, LimitBytes: 1024})
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
