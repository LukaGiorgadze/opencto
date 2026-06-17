package workflows_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime/activities"
	"github.com/opencto/opencto/internal/runtime/scheduled"
	"github.com/opencto/opencto/internal/runtime/workflows"
)

func registerTaskWorkflowActivities(env *testsuite.TestWorkflowEnvironment) {
	env.RegisterActivityWithOptions((&activities.Activities{}).NextAction, activity.RegisterOptions{Name: "Activities.NextAction"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ExecuteTool, activity.RegisterOptions{Name: "Activities.ExecuteTool"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ExecuteMemoryTool, activity.RegisterOptions{Name: "Activities.ExecuteMemoryTool"})
	env.RegisterActivityWithOptions((&activities.Activities{}).PersistEvent, activity.RegisterOptions{Name: "Activities.PersistEvent"})
	env.RegisterActivityWithOptions((&activities.Activities{}).PrepareOnboarding, activity.RegisterOptions{Name: "Activities.PrepareOnboarding"})
	env.RegisterActivityWithOptions((&activities.Activities{}).FinalizeOnboarding, activity.RegisterOptions{Name: "Activities.FinalizeOnboarding"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ResetConversationContext, activity.RegisterOptions{Name: "Activities.ResetConversationContext"})
	env.RegisterActivityWithOptions((&activities.Activities{}).CompressAgentObservations, activity.RegisterOptions{Name: "Activities.CompressAgentObservations"})
	env.RegisterActivityWithOptions((&activities.Activities{}).PersistNextAction, activity.RegisterOptions{Name: "Activities.PersistNextAction"})
	env.RegisterActivityWithOptions((&activities.Activities{}).PersistToolResult, activity.RegisterOptions{Name: "Activities.PersistToolResult"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ResponseSession, activity.RegisterOptions{Name: "Activities.ResponseSession"})
	env.RegisterActivityWithOptions((&activities.Activities{}).ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})
	env.RegisterActivityWithOptions((&activities.Activities{}).EnqueueScheduledEvent, activity.RegisterOptions{Name: scheduled.EnqueueScheduledEventName})
}

func TestTaskWorkflowAlternatesNextActionAndExecuteTool(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	nextAction := agent.NextAction{
		WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}},
	}
	toolChoice := agent.ToolChoice{
		ToolCallID: "toolu_1",
		Type:       domain.ToolTypeExec,
		Intent:     "print working directory",
		Command:    "pwd",
		Metadata: map[string]string{
			"tool_call_id": "toolu_1",
			"work_item_id": "wi-1",
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 &&
			request.LastResult == nil &&
			len(request.LastResults) == 0 &&
			len(request.ObservationHistory) == 0
	})).Return(activities.NextActionResult{
		NextAction:  nextAction,
		ToolChoices: []agent.ToolChoice{toolChoice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-1" &&
			request.ToolChoice.ToolCallID == "toolu_1" &&
			request.ToolChoice.Metadata["tool_call_id"] == "toolu_1"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_1",
		Tool:            domain.ToolTypeExec,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "print working directory",
		Command:         "pwd",
		Observation:     "/tmp/opencto",
		Metadata: map[string]string{
			"tool_call_id": "toolu_1",
		},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			len(request.LastResults) == 1 &&
			request.LastResults[0].ToolCallID == "toolu_1" &&
			request.LastResults[0].Observation == "/tmp/opencto" &&
			len(request.ObservationHistory) == 0
	})).Return(activities.NextActionResult{
		NextAction:  nextAction,
		Observation: &agent.ExecutionFeedback{WorkItemID: "wi-1", ToolCallID: "toolu_1", Status: string(domain.ExecutionStatusSucceeded), Observation: "/tmp/opencto"},
		Status:      activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected workflow to complete")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowResetsConversationContextForNewCommand(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	env.OnActivity("Activities.ResetConversationContext", mock.Anything, mock.MatchedBy(func(request activities.ResetConversationContextRequest) bool {
		return request.Event.ID == "event-reset" &&
			request.Event.Body == domain.ConversationResetSlashCommand &&
			request.Event.ChannelID == "channel-1"
	})).Return(activities.ResetConversationContextResult{Scope: string(domain.ContextResetScopeChannel)}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event: domain.Event{
			ID:          "event-reset",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Body:        domain.ConversationResetSlashCommand,
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed || result.Status != activities.NextActionStatusCompleted || result.ResponseMessage != domain.ConversationResetConfirmation || !result.Report {
		t.Fatalf("unexpected reset result: %#v", result)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowSkipsResetReportWhenCommandWasAcknowledged(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	env.OnActivity("Activities.ResetConversationContext", mock.Anything, mock.Anything).Return(activities.ResetConversationContextResult{Scope: string(domain.ContextResetScopeChannel)}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event: domain.Event{
			ID:          "event-reset",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Body:        domain.ConversationResetSlashCommand,
			Metadata: domain.Metadata{
				domain.MetadataKeyCommandResponseAcknowledged: "true",
			},
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Report {
		t.Fatalf("expected reset report to be suppressed after interaction ack: %#v", result)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowRoutesAgentToolTosubWorkflow(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, input workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		if input.ParentWorkItemID != "wi-agent" || input.ParentToolCallID != "toolu_agent" {
			return workflows.AgentWorkflowResult{}, errors.New("missing parent lineage")
		}
		return workflows.AgentWorkflowResult{
			Status:       domain.ExecutionStatusSucceeded,
			Message:      "sub completed",
			TurnCount:    2,
			ToolsUsed:    []domain.ToolType{domain.ToolTypeWrite},
			FilesTouched: []string{"/tmp/agent.txt"},
		}, nil
	}, workflow.RegisterOptions{Name: workflows.AgentWorkflowName})
	registerTaskWorkflowActivities(env)

	input, err := json.Marshal(map[string]any{
		"goal":          "Write file",
		"prompt":        "Create the requested file.",
		"allowed_tools": []domain.ToolType{domain.ToolTypeWrite},
		"max_turns":     5,
	})
	if err != nil {
		t.Fatalf("marshal agent input: %v", err)
	}
	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "delegate work"}
	nextAction := agent.NextAction{WorkItems: []domain.WorkItem{{
		ID:        "wi-agent",
		ProjectID: "project-1",
		Status:    domain.WorkItemStatusReady,
	}}}
	agentChoice := agent.ToolChoice{
		ToolCallID: "toolu_agent",
		Type:       domain.ToolTypeAgent,
		Intent:     "run sub agent",
		Input:      input,
		Metadata: map[string]string{
			"tool_call_id": "toolu_agent",
			"work_item_id": "wi-agent",
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 && len(request.LastResults) == 0
	})).Return(activities.NextActionResult{
		NextAction:  nextAction,
		ToolChoices: []agent.ToolChoice{agentChoice},
		WorkItemID:  "wi-agent",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			len(request.LastResults) == 1 &&
			request.LastResults[0].Tool == domain.ToolTypeAgent &&
			request.LastResults[0].Status == domain.ExecutionStatusSucceeded &&
			strings.Contains(request.LastResults[0].Observation, "sub completed") &&
			request.LastResults[0].Metadata["agent_turn_count"] == "2" &&
			request.LastResults[0].Metadata["agent_tools_used"] == string(domain.ToolTypeWrite)
	})).Return(activities.NextActionResult{
		NextAction: agent.NextAction{ResponseMessage: "done"},
		Status:     activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowStopsWhenAgentsubIsCanceled(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, _ workflows.AgentWorkflowInput) (workflows.AgentWorkflowResult, error) {
		var signal workflows.TaskControlSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskCancel).Receive(ctx, &signal)
		return workflows.AgentWorkflowResult{
			Status:  domain.ExecutionStatusCanceled,
			Message: "Agent canceled.",
		}, nil
	}, workflow.RegisterOptions{Name: workflows.AgentWorkflowName})
	registerTaskWorkflowActivities(env)

	input, err := json.Marshal(map[string]any{
		"goal":      "Long task",
		"prompt":    "Wait for cancellation.",
		"max_turns": 5,
	})
	if err != nil {
		t.Fatalf("marshal agent input: %v", err)
	}
	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "delegate long work"}
	agentChoice := agent.ToolChoice{
		ToolCallID: "toolu_agent",
		Type:       domain.ToolTypeAgent,
		Intent:     "run sub agent",
		Input:      input,
		Metadata: map[string]string{
			"tool_call_id": "toolu_agent",
			"work_item_id": "wi-agent",
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1
	})).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-agent",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoices: []agent.ToolChoice{agentChoice},
		WorkItemID:  "wi-agent",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalTaskCancel, workflows.TaskControlSignal{
			Event:  domain.Event{ID: "cancel-1", ProjectID: "project-1", Body: "cancel"},
			Reason: "test cancel",
		})
	}, time.Millisecond)

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Completed {
		t.Fatalf("expected parent task to stop incomplete after cancel, got %#v", result)
	}
	env.AssertExpectations(t)
}

func TestAgentWorkflowRunsDurableSubAgentLoop(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.AgentWorkflow)
	registerTaskWorkflowActivities(env)

	input, err := json.Marshal(map[string]any{
		"goal":          "Write file",
		"prompt":        "Create the requested file.",
		"allowed_tools": []domain.ToolType{domain.ToolTypeWrite},
		"max_turns":     5,
	})
	if err != nil {
		t.Fatalf("marshal agent input: %v", err)
	}
	writeInput, err := json.Marshal(map[string]any{
		"file_path": "/tmp/agent.txt",
		"content":   "hello\n",
	})
	if err != nil {
		t.Fatalf("marshal write input: %v", err)
	}

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "delegate work"}
	nextAction := agent.NextAction{WorkItems: []domain.WorkItem{{
		ID:        "wi-sub",
		ProjectID: "project-1",
		Status:    domain.WorkItemStatusReady,
	}}}
	writeChoice := agent.ToolChoice{
		ToolCallID: "toolu_write",
		Type:       domain.ToolTypeWrite,
		Intent:     "write file",
		Input:      writeInput,
		Metadata: map[string]string{
			"tool_call_id": "toolu_write",
			"work_item_id": "wi-sub",
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 &&
			request.SubAgent != nil &&
			request.SubAgent.Goal == "Write file" &&
			strings.Contains(request.SubAgent.Prompt, "Create the requested file") &&
			request.RestrictTools &&
			len(request.ToolAllowlist) == 1 &&
			request.ToolAllowlist[0] == domain.ToolTypeWrite &&
			len(request.Processes) == 0
	})).Return(activities.NextActionResult{
		NextAction:  nextAction,
		ToolChoices: []agent.ToolChoice{writeChoice},
		WorkItemID:  "wi-sub",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-sub" &&
			request.ToolChoice.ToolCallID == "toolu_write" &&
			request.ToolChoice.Metadata["parent_work_item_id"] == "wi-parent" &&
			request.ToolChoice.Metadata["parent_tool_call_id"] == "toolu_agent" &&
			request.ToolChoice.Metadata["agent_child_trace"] == "true"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-sub",
		ToolCallID:      "toolu_write",
		Tool:            domain.ToolTypeWrite,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "write file",
		Input:           writeInput,
		Observation:     "wrote file",
		ResultCode:      "0",
		Metadata: map[string]string{
			"tool_call_id": "toolu_write",
			"file_path":    "/tmp/agent.txt",
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "sub background process",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			len(request.LastResults) == 1 &&
			request.LastResults[0].ToolCallID == "toolu_write" &&
			len(request.Processes) == 1 &&
			request.Processes[0].ID == "proc-1" &&
			request.Processes[0].Status == domain.ProcessStatusRunning
	})).Return(activities.NextActionResult{
		NextAction: agent.NextAction{ResponseMessage: "wrote the requested file"},
		Status:     activities.NextActionStatusCompleted,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "sub background process",
			Status:      domain.ProcessStatusStopped,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()

	env.ExecuteWorkflow(workflows.AgentWorkflow, workflows.AgentWorkflowInput{
		ProjectID:        "project-1",
		Event:            event,
		ParentWorkItemID: "wi-parent",
		ParentToolCallID: "toolu_agent",
		AgentWorkflowID:  "project-1:agent:test",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_agent",
			Type:       domain.ToolTypeAgent,
			Intent:     "run sub agent",
			Input:      input,
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("agent workflow failed: %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded ||
		result.TurnCount != 2 ||
		len(result.ToolsUsed) != 1 ||
		result.ToolsUsed[0] != domain.ToolTypeWrite ||
		len(result.FilesTouched) != 1 ||
		result.FilesTouched[0] != "/tmp/agent.txt" ||
		len(result.Processes) != 1 ||
		result.Processes[0].ID != "proc-1" ||
		result.Processes[0].Status != domain.ProcessStatusStopped {
		t.Fatalf("unexpected agent result: %#v", result)
	}
	env.AssertExpectations(t)
}

func TestAgentWorkflowCompletionSuccessUsesPublishObservation(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.AgentWorkflow)
	registerTaskWorkflowActivities(env)

	input, err := json.Marshal(map[string]any{
		"goal":      "Create scheduled workflow github-stargazers",
		"prompt":    "Create a workflow that fetches GitHub stargazers.",
		"max_turns": 5,
	})
	if err != nil {
		t.Fatalf("marshal agent input: %v", err)
	}
	completionInput, err := json.Marshal(map[string]any{
		"workflow_id": "github-stargazers",
		"prompt":      "Create a workflow that fetches GitHub stargazers.",
	})
	if err != nil {
		t.Fatalf("marshal completion input: %v", err)
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 &&
			request.SubAgent != nil &&
			request.SubAgent.Goal == "Create scheduled workflow github-stargazers"
	})).Return(activities.NextActionResult{
		NextAction: agent.NextAction{
			ResponseMessage: "Created workflow.yml and ran syntax checks.",
			WorkItems: []domain.WorkItem{{
				ID:        "wi-sub",
				ProjectID: "project-1",
				Status:    domain.WorkItemStatusCompleted,
			}},
		},
		WorkItemID: "wi-sub",
		Status:     activities.NextActionStatusCompleted,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.WorkItemID == "wi-sub" &&
			request.ToolChoice.Type == domain.ToolTypeWorkflowCreate &&
			request.ToolChoice.ToolCallID == "toolu_workflow:completion:1" &&
			request.ToolChoice.Metadata["agent_child_trace"] == "true"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-sub",
		ToolCallID:      "toolu_workflow:completion:1",
		Tool:            domain.ToolTypeWorkflowCreate,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "create workflow",
		Input:           completionInput,
		Observation:     "workflow schedule created\nworkflow_id: github-stargazers\nschedule_id: default:workflow:github-stargazers",
		ResultCode:      "0",
		Metadata: map[string]string{
			"tool_call_id":         "toolu_workflow:completion:1",
			"workflow_commit_hash": "abc123",
			"schedule_id":          "default:workflow:github-stargazers",
		},
	}, nil).Once()

	env.ExecuteWorkflow(workflows.AgentWorkflow, workflows.AgentWorkflowInput{
		ProjectID:        "project-1",
		Event:            domain.Event{ID: "event-1", ProjectID: "project-1", Body: "delegate workflow authoring"},
		ParentWorkItemID: "wi-parent",
		ParentToolCallID: "toolu_agent",
		AgentWorkflowID:  "project-1:agent:test",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_agent",
			Type:       domain.ToolTypeAgent,
			Intent:     "run sub agent",
			Input:      input,
		},
		CompletionTool: &agent.ToolChoice{
			ToolCallID: "toolu_workflow",
			Type:       domain.ToolTypeWorkflowCreate,
			Intent:     "create workflow",
			Input:      completionInput,
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("agent workflow failed: %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("expected success, got %#v", result)
	}
	if !strings.Contains(result.Message, "workflow schedule created") ||
		!strings.Contains(result.Message, "Authoring notes:") ||
		!strings.Contains(result.Message, "Created workflow.yml") {
		t.Fatalf("expected publish message with sub notes, got %q", result.Message)
	}
	if result.Metadata["workflow_commit_hash"] != "abc123" {
		t.Fatalf("expected publish metadata, got %#v", result.Metadata)
	}
	env.AssertExpectations(t)
}

func TestAgentWorkflowCompletionSuccessSurvivesTracePersistenceFailure(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.AgentWorkflow)
	registerTaskWorkflowActivities(env)

	input, err := json.Marshal(map[string]any{
		"goal":      "Create scheduled workflow github-stargazers",
		"prompt":    "Create a workflow that fetches GitHub stargazers.",
		"max_turns": 5,
	})
	if err != nil {
		t.Fatalf("marshal agent input: %v", err)
	}
	completionInput, err := json.Marshal(map[string]any{
		"workflow_id": "github-stargazers",
		"prompt":      "Create a workflow that fetches GitHub stargazers.",
	})
	if err != nil {
		t.Fatalf("marshal completion input: %v", err)
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{
			ResponseMessage: "Created workflow.yml.",
			WorkItems: []domain.WorkItem{{
				ID:        "wi-sub",
				ProjectID: "project-1",
				Status:    domain.WorkItemStatusCompleted,
			}},
		},
		WorkItemID: "wi-sub",
		Status:     activities.NextActionStatusCompleted,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-sub",
		ToolCallID:      "toolu_workflow:completion:1",
		Tool:            domain.ToolTypeWorkflowCreate,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "create workflow",
		Input:           completionInput,
		Observation:     "workflow schedule created",
		ResultCode:      "0",
	}, nil).Once()
	env.OnActivity("Activities.PersistToolResult", mock.Anything, mock.MatchedBy(func(request activities.PersistToolResultRequest) bool {
		return request.Result.Tool == domain.ToolTypeWorkflowCreate &&
			request.Result.Status == domain.ExecutionStatusSucceeded
	})).Return(temporal.NewNonRetryableApplicationError("trace store unavailable", "TracePersistError", nil)).Once()

	env.ExecuteWorkflow(workflows.AgentWorkflow, workflows.AgentWorkflowInput{
		ProjectID:        "project-1",
		Event:            domain.Event{ID: "event-1", ProjectID: "project-1", Body: "delegate workflow authoring"},
		ParentWorkItemID: "wi-parent",
		ParentToolCallID: "toolu_agent",
		AgentWorkflowID:  "project-1:agent:test",
		ToolChoice: agent.ToolChoice{
			ToolCallID: "toolu_agent",
			Type:       domain.ToolTypeAgent,
			Intent:     "run sub agent",
			Input:      input,
		},
		CompletionTool: &agent.ToolChoice{
			ToolCallID: "toolu_workflow",
			Type:       domain.ToolTypeWorkflowCreate,
			Intent:     "create workflow",
			Input:      completionInput,
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("agent workflow failed: %v", err)
	}
	var result workflows.AgentWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Status != domain.ExecutionStatusSucceeded {
		t.Fatalf("expected publish success to win, got %#v", result)
	}
	if !strings.Contains(result.Message, "workflow schedule created") {
		t.Fatalf("expected publish message, got %q", result.Message)
	}
	if !strings.Contains(result.Metadata["agent_trace_persist_error"], "trace store unavailable") {
		t.Fatalf("expected trace warning metadata, got %#v", result.Metadata)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowExecutesMultipleToolChoicesAsSeparateActivities(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	nextAction := agent.NextAction{WorkItems: []domain.WorkItem{{
		ID:        "wi-1",
		ProjectID: "project-1",
		Status:    domain.WorkItemStatusReady,
	}}}
	choices := []agent.ToolChoice{
		{
			ToolCallID: "toolu_read",
			Type:       domain.ToolTypeRead,
			Intent:     "read file",
			Input:      []byte(`{"file_path":"/workspace/a.go"}`),
			Metadata: map[string]string{
				"tool_call_id":  "toolu_read",
				"tool_call_ids": "toolu_read,toolu_grep",
			},
		},
		{
			ToolCallID: "toolu_grep",
			Type:       domain.ToolTypeGrep,
			Intent:     "grep files",
			Input:      []byte(`{"pattern":"needle"}`),
			Metadata: map[string]string{
				"tool_call_id":  "toolu_grep",
				"tool_call_ids": "toolu_read,toolu_grep",
			},
		},
	}

	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 1 && len(request.LastResults) == 0
	})).Return(activities.NextActionResult{
		NextAction:  nextAction,
		ToolChoices: choices,
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.ToolCallID == "toolu_read"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_read",
		Tool:            domain.ToolTypeRead,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "read file",
		Observation:     "file content",
		Metadata: map[string]string{
			"tool_call_id":  "toolu_read",
			"tool_call_ids": "toolu_read,toolu_grep",
		},
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.ToolCallID == "toolu_grep"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_grep",
		Tool:            domain.ToolTypeGrep,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "grep files",
		Observation:     "matched",
		Metadata: map[string]string{
			"tool_call_id":  "toolu_grep",
			"tool_call_ids": "toolu_read,toolu_grep",
		},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			len(request.LastResults) == 2 &&
			request.LastResults[0].ToolCallID == "toolu_read" &&
			request.LastResults[1].ToolCallID == "toolu_grep" &&
			len(request.ObservationHistory) == 0
	})).Return(activities.NextActionResult{
		NextAction: nextAction,
		Observations: []agent.ExecutionFeedback{
			{WorkItemID: "wi-1", ToolCallID: "toolu_read", Tool: domain.ToolTypeRead, Status: string(domain.ExecutionStatusSucceeded), Observation: "file content", Metadata: map[string]string{"tool_call_ids": "toolu_read,toolu_grep"}},
			{WorkItemID: "wi-1", ToolCallID: "toolu_grep", Tool: domain.ToolTypeGrep, Status: string(domain.ExecutionStatusSucceeded), Observation: "matched", Metadata: map[string]string{"tool_call_ids": "toolu_read,toolu_grep"}},
		},
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowRoutesMemoryToolsToMemoryActivity(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "remember this"}
	choice := agent.ToolChoice{
		ToolCallID: "toolu_memory",
		Type:       domain.ToolTypeMemoryProposeAdd,
		Intent:     "propose memory add preference",
		Input:      []byte(`{"content":"Use SQLite for local storage.","scope":"project","kind":"decision","tags":["storage"],"confidence":1,"pinned":false,"reason":"user preference"}`),
		Metadata: map[string]string{
			"tool_call_id":    "toolu_memory",
			"execution_cycle": "1",
		},
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoices: []agent.ToolChoice{choice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteMemoryTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.Type == domain.ToolTypeMemoryProposeAdd &&
			request.ToolChoice.ToolCallID == "toolu_memory"
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_memory",
		Tool:            domain.ToolTypeMemoryProposeAdd,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "propose memory add preference",
		Observation:     "Accepted memory add proposal.\nmemory_id: memory-1",
		Metadata: map[string]string{
			"tool_call_id": "toolu_memory",
			"memory_id":    "memory-1",
		},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.ExecutionCycle == 2 &&
			len(request.LastResults) == 1 &&
			request.LastResults[0].Tool == domain.ToolTypeMemoryProposeAdd
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowRejectsNonTerminalNextActionWithoutTool(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "inspect workspace"}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusTool,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})

	err := env.GetWorkflowError()
	if err == nil || !strings.Contains(err.Error(), "without a tool choice") {
		t.Fatalf("expected missing tool choice error, got %v", err)
	}
}

func TestTaskWorkflowStartsResponseSessionForChannelEvent(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		Body:        "inspect workspace",
	}
	env.OnActivity("Activities.ResponseSession", mock.Anything, mock.MatchedBy(func(request activities.ResponseSessionRequest) bool {
		return request.ProjectID == "project-1" &&
			request.Event.ID == "event-1" &&
			request.Event.ChannelID == "channel-1"
	})).Return(nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPassesProcessesReturnedByExecuteToolToNextAction(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run dev server"}
	choice := agent.ToolChoice{
		ToolCallID:  "toolu_bg",
		Type:        domain.ToolTypeExec,
		Intent:      "start dev server",
		Command:     "pnpm",
		Args:        []string{"run", "dev"},
		RunMode:     domain.ToolRunModeStartBackground,
		Idempotency: domain.ToolIdempotencyNonIdempotent,
		Metadata: map[string]string{
			"tool_call_id":    "toolu_bg",
			"work_item_id":    "wi-1",
			"execution_cycle": "1",
		},
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoices: []agent.ToolChoice{choice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.MatchedBy(func(request activities.ExecuteToolRequest) bool {
		return request.ToolChoice.ToolCallID == "toolu_bg" && request.ToolChoice.RunMode == domain.ToolRunModeStartBackground
	})).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeExec,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start dev server",
		Command:         "pnpm",
		Args:            []string{"run", "dev"},
		Observation:     "Started background process.",
		Metadata: map[string]string{
			"tool_call_id":  "toolu_bg",
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeStopOnFinish),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start dev server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.LastResults) == 1 &&
			request.LastResults[0].Metadata["process_id"] == "proc-1" &&
			len(request.Processes) == 1 &&
			request.Processes[0].ID == "proc-1"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start dev server",
			Status:      domain.ProcessStatusStopped,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected workflow to complete")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowKeepsProjectScopedBackgroundProcessRunning(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run persistent server"}
	choice := agent.ToolChoice{
		ToolCallID:   "toolu_bg",
		Type:         domain.ToolTypeExec,
		Intent:       "start persistent server",
		Command:      "server",
		RunMode:      domain.ToolRunModeStartBackground,
		Idempotency:  domain.ToolIdempotencyNonIdempotent,
		ProcessScope: domain.ProcessScopeProject,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction: agent.NextAction{WorkItems: []domain.WorkItem{{
			ID:        "wi-1",
			ProjectID: "project-1",
			Status:    domain.WorkItemStatusReady,
		}}},
		ToolChoices: []agent.ToolChoice{choice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeExec,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start persistent server",
		Command:         "server",
		Observation:     "Started background process.",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeProject),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.Processes) == 1 &&
			request.Processes[0].ID == "proc-1" &&
			request.Processes[0].Scope == domain.ProcessScopeProject
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if !result.Completed {
		t.Fatalf("expected workflow to complete")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowMarksIncompleteWhenTaskProcessCleanupFails(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run server"}
	choice := agent.ToolChoice{
		ToolCallID:  "toolu_bg",
		Type:        domain.ToolTypeExec,
		Intent:      "start task server",
		Command:     "server",
		RunMode:     domain.ToolRunModeStartBackground,
		Idempotency: domain.ToolIdempotencyNonIdempotent,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction:  agent.NextAction{},
		ToolChoices: []agent.ToolChoice{choice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeExec,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start task server",
		Command:         "server",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeStopOnFinish),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start task server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		Status: activities.NextActionStatusFailed,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start task server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeStopOnFinish,
		}},
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Completed {
		t.Fatalf("expected incomplete result")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPreservesProjectProcessAfterNextActionError(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "run persistent server"}
	choice := agent.ToolChoice{
		ToolCallID:   "toolu_bg",
		Type:         domain.ToolTypeExec,
		Intent:       "start persistent server",
		Command:      "server",
		RunMode:      domain.ToolRunModeStartBackground,
		Idempotency:  domain.ToolIdempotencyNonIdempotent,
		ProcessScope: domain.ProcessScopeProject,
	}
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{
		NextAction:  agent.NextAction{},
		ToolChoices: []agent.ToolChoice{choice},
		WorkItemID:  "wi-1",
		Status:      activities.NextActionStatusTool,
	}, nil).Once()
	env.OnActivity("Activities.ExecuteTool", mock.Anything, mock.Anything).Return(activities.ExecuteToolResult{
		Cycle:           1,
		WorkItemID:      "wi-1",
		ToolCallID:      "toolu_bg",
		Tool:            domain.ToolTypeExec,
		Status:          domain.ExecutionStatusSucceeded,
		RequestedAction: "start persistent server",
		Command:         "server",
		Metadata: map[string]string{
			"process_id":    "proc-1",
			"process_scope": string(domain.ProcessScopeProject),
			"run_mode":      string(domain.ToolRunModeStartBackground),
		},
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
	}, nil).Once()
	env.OnActivity("Activities.NextAction", mock.Anything, mock.Anything).Return(activities.NextActionResult{}, errors.New("next action failed")).Times(5)
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return request.Completion != nil &&
			len(request.Completion.Processes) == 1 &&
			request.Completion.Processes[0].ID == "proc-1"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusFailed,
		Processes: []domain.ProcessReference{{
			ID:          "proc-1",
			Description: "start persistent server",
			Status:      domain.ProcessStatusRunning,
			Scope:       domain.ProcessScopeProject,
		}},
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Completed {
		t.Fatalf("expected incomplete result")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowPassesAdditionalContextSignalToNextAction(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}
	additional := domain.Event{ID: "event-2", ProjectID: "project-1", Body: "also check tests"}
	persistedAdditional := false
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalTaskAdditionalContext, workflows.AdditionalContextSignal{Event: additional})
	}, 0)
	env.OnActivity("Activities.PersistEvent", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		request := args.Get(1).(activities.PersistEventRequest)
		if request.Event.ID == "event-2" && request.Event.Metadata[domain.MetadataKeyControl] == "" {
			persistedAdditional = true
		}
	}).Return(nil)
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.AdditionalEvents) == 1 && request.AdditionalEvents[0].ID == "event-2"
	})).Return(activities.NextActionResult{
		Status: activities.NextActionStatusCompleted,
	}, nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	if !persistedAdditional {
		t.Fatalf("expected additional context signal to be persisted")
	}
	env.AssertExpectations(t)
}

func TestTaskWorkflowUsesLatestAdditionalContextAsReportTarget(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.TaskWorkflow)
	registerTaskWorkflowActivities(env)

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		Body:        "create app",
	}
	additional := domain.Event{
		ID:          "event-2",
		ProjectID:   "project-1",
		ChannelID:   "thread-1",
		ChannelType: domain.ChannelTypeDiscord,
		ThreadID:    "thread-1",
		Body:        "add orange colors",
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalTaskAdditionalContext, workflows.AdditionalContextSignal{Event: additional})
	}, 0)
	env.OnActivity("Activities.PersistEvent", mock.Anything, mock.Anything).Return(nil)
	env.OnActivity("Activities.NextAction", mock.Anything, mock.MatchedBy(func(request activities.NextActionRequest) bool {
		return len(request.AdditionalEvents) == 1 && request.AdditionalEvents[0].ID == "event-2"
	})).Return(activities.NextActionResult{
		NextAction: agent.NextAction{ResponseMessage: "done"},
		Status:     activities.NextActionStatusCompleted,
	}, nil).Once()
	env.OnActivity("Activities.PersistNextAction", mock.Anything, mock.MatchedBy(func(request activities.PersistNextActionRequest) bool {
		return request.Status == activities.NextActionStatusCompleted
	})).Return(nil).Once()

	env.ExecuteWorkflow(workflows.TaskWorkflow, workflows.TaskWorkflowInput{
		ProjectID: "project-1",
		Event:     event,
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("task workflow failed: %v", err)
	}
	var result workflows.TaskWorkflowResult
	if err := env.GetWorkflowResult(&result); err != nil {
		t.Fatalf("get result: %v", err)
	}
	if result.Event.ChannelID != "thread-1" || result.Event.ThreadID != "thread-1" {
		t.Fatalf("expected final report target to use thread, got %#v", result.Event)
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowStartsUntokenedMessageAsSeparateTaskWhileActive(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID == "event-1" {
			_ = workflow.Sleep(ctx, 10*365*24*time.Hour)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{ID: "event-2", ProjectID: "project-1", Body: "extra context"}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 2*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("expected untokened second message to start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowStartsStandaloneDiscordControlMessagesAsNewTasks(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID == "event-1" {
			_ = workflow.Sleep(ctx, 10*365*24*time.Hour)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	events := []domain.Event{
		{ID: "event-1", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "do work"},
		{ID: "event-ok", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "ok"},
		{ID: "event-approve", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "approve"},
		{ID: "event-go", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "go for it"},
		{ID: "event-cancel", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "cancel"},
		{ID: "event-interrupt", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "interrupt"},
		{ID: "event-extra", ProjectID: "project-1", ChannelType: domain.ChannelTypeDiscord, ChannelID: "channel-1", Body: "please continue"},
	}
	for index, event := range events {
		event := event
		env.RegisterDelayedCallback(func() {
			env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: event})
		}, time.Duration(index)*time.Millisecond)
	}
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 20*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	want := "event-1,event-ok,event-approve,event-go,event-cancel,event-interrupt,event-extra"
	if strings.Join(seen, ",") != want {
		t.Fatalf("expected standalone Discord messages to start workflows, got %#v", seen)
	}
}

func TestProjectWorkflowRoutesReplyToOwnedMessageAsAdditionalContext(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		var signal workflows.AdditionalContextSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
		received = signal.Event.ID == "event-2" &&
			signal.Event.Body == "Approved!" &&
			signal.Event.Metadata[domain.MetadataKeyControl] == domain.MetadataControlTaskReply
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Body:      "do work",
			Provenance: domain.Provenance{
				SourceID: "source-message-1",
			},
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			Body:      "Approved!",
			Metadata:  domain.Metadata{domain.MetadataKeyReplyToMessageID: "source-message-1"},
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !received {
		t.Fatalf("expected reply to owned message to route as additional context")
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("reply should not start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowRoutesOnboardingReplyAsSeparateTask(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID == "event-1" {
			_ = workflow.Sleep(ctx, 10*365*24*time.Hour)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Body:      "do work",
			Provenance: domain.Provenance{
				SourceID: "source-message-1",
			},
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			Body:      domain.OnboardingSlashCommand,
			Metadata:  domain.Metadata{domain.MetadataKeyReplyToMessageID: "source-message-1"},
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("expected onboarding reply to start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowRoutesReplyToOriginalMessageAsAdditionalContext(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		var signal workflows.AdditionalContextSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
		received = signal.Event.ID == "event-2" &&
			signal.Event.Body == "now" &&
			signal.Event.Metadata[domain.MetadataKeyControl] == domain.MetadataControlTaskReply
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Body:      "delete react example app",
			Provenance: domain.Provenance{
				SourceID: "user-message-1",
			},
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			Body:      "now",
			Metadata:  domain.Metadata{domain.MetadataKeyReplyToMessageID: "user-message-1"},
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !received {
		t.Fatalf("expected reply to original user message to route as additional context")
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("reply to original message should not start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowLearnsThreadFromReplyToOriginalMessage(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	var received []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		for len(received) < 1 {
			var signal workflows.AdditionalContextSignal
			workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
			received = append(received, signal.Event.ID)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			Body:      "create app",
			Provenance: domain.Provenance{
				SourceID: "user-message-1",
			},
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			ChannelID: "thread-1",
			ThreadID:  "thread-1",
			Body:      "",
			Metadata:  domain.Metadata{domain.MetadataKeyReplyToMessageID: "user-message-1"},
			Provenance: domain.Provenance{
				SourceID: "thread-starter-message",
			},
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-3",
			ProjectID: "project-1",
			ChannelID: "thread-1",
			ThreadID:  "thread-1",
			Body:      "add orange colors",
			Provenance: domain.Provenance{
				SourceID: "thread-message-2",
			},
		}})
	}, 2*time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(received, ",") != "event-3" {
		t.Fatalf("expected empty thread bootstrap to register ownership without context signal, got %#v", received)
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("thread follow-up should not start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowRoutesThreadMessageToOwnedTask(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		var signal workflows.AdditionalContextSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
		received = signal.Event.ID == "event-2" &&
			signal.Event.ThreadID == "thread-1" &&
			signal.Event.Metadata[domain.MetadataKeyControl] == domain.MetadataControlTaskReply
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			ThreadID:  "thread-1",
			Body:      "do work",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			ThreadID:  "thread-1",
			Body:      "extra context",
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !received {
		t.Fatalf("expected thread message to route as additional context")
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("thread message should not start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowStartsResetCommandAsNewTaskInOwnedThread(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID == "event-1" {
			_ = workflow.Sleep(ctx, 10*365*24*time.Hour)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-1",
			Body:        "do work",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-reset",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-1",
			Body:        domain.ConversationResetSlashCommand,
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1,event-reset" {
		t.Fatalf("reset command should start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowDoesNotRouteParentChannelMessageToThreadTask(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext), func(c workflow.ReceiveChannel, more bool) {
			var signal workflows.AdditionalContextSignal
			c.Receive(ctx, &signal)
			received = signal.Event.ID == "event-2"
		})
		selector.AddFuture(workflow.NewTimer(ctx, 3*time.Millisecond), func(workflow.Future) {})
		selector.Select(ctx)
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-1",
			Body:        "do work",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-2",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Body:        "new main-channel task",
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 6*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if received {
		t.Fatalf("parent channel message should not route into thread task")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("parent channel message should start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowStartsUnownedThreadMessageAsNewTask(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		selector := workflow.NewSelector(ctx)
		selector.AddReceive(workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext), func(c workflow.ReceiveChannel, more bool) {
			var signal workflows.AdditionalContextSignal
			c.Receive(ctx, &signal)
			received = signal.Event.ID == "event-2"
		})
		selector.AddFuture(workflow.NewTimer(ctx, 3*time.Millisecond), func(workflow.Future) {})
		selector.Select(ctx)
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			Body:        "do work",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-2",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ChannelID:   "channel-1",
			ThreadID:    "thread-1",
			Body:        "thread follow-up",
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if received {
		t.Fatalf("unowned thread message should not route to active parent-channel task")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("unowned thread message should start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowRoutesThreadChannelMessageToOwnedTask(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	received := false
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID != "event-1" {
			return workflows.TaskWorkflowResult{Completed: true}, nil
		}
		var signal workflows.AdditionalContextSignal
		workflow.GetSignalChannel(ctx, workflows.SignalTaskAdditionalContext).Receive(ctx, &signal)
		received = signal.Event.ID == "event-2" &&
			signal.Event.ChannelID == "thread-channel-1" &&
			signal.Event.Metadata[domain.MetadataKeyControl] == domain.MetadataControlTaskReply
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-1",
			ProjectID: "project-1",
			ChannelID: "thread-channel-1",
			ThreadID:  "thread-channel-1",
			Body:      "create app",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:        "event-2",
			ProjectID: "project-1",
			ChannelID: "thread-channel-1",
			Body:      "orange colors",
		}})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 5*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !received {
		t.Fatalf("expected thread channel message to route as additional context")
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("thread channel message should not start a separate task, got %#v", seen)
	}
}

func TestProjectWorkflowReportsAfterTaskWorkflowCompletes(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		return workflows.TaskWorkflowResult{
			Completed:       true,
			Status:          activities.NextActionStatusCompleted,
			Event:           input.Event,
			ResponseMessage: "done",
			Report:          true,
		}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.RegisterActivityWithOptions((&activities.Activities{}).ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})

	reported := false
	env.OnActivity("Activities.ReportResponse", mock.Anything, mock.MatchedBy(func(request activities.ReportResponseRequest) bool {
		reported = request.Event.ID == "event-1" && request.Message == "done"
		return reported
	})).Run(func(mock.Arguments) {
		env.CancelWorkflow()
	}).Return(activities.ReportResponseResult{}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			Body:        "do work",
		}})
	}, 0)
	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !reported {
		t.Fatalf("expected project workflow to report after sub task result")
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowReportsReplyTargetFromEventMetadata(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		return workflows.TaskWorkflowResult{
			Completed:       true,
			Status:          activities.NextActionStatusCompleted,
			Event:           input.Event,
			ResponseMessage: "done",
			Report:          true,
		}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.RegisterActivityWithOptions((&activities.Activities{}).ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})

	reported := false
	env.OnActivity("Activities.ReportResponse", mock.Anything, mock.MatchedBy(func(request activities.ReportResponseRequest) bool {
		reported = request.Event.ID == "event-1" &&
			request.Message == "done" &&
			request.ReplyTo != nil &&
			request.ReplyTo.MessageID == "channel-1:message-1" &&
			request.ReplyTo.ChannelID == "channel-1" &&
			request.ReplyTo.ContextID == "context-1"
		return reported
	})).Run(func(mock.Arguments) {
		env.CancelWorkflow()
	}).Return(activities.ReportResponseResult{}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeTelegram,
			Body:        "do work",
			Metadata: domain.Metadata{
				domain.MetadataKeyReplyToMessageID: "channel-1:message-1",
				domain.MetadataKeyReplyToChannelID: "channel-1",
				domain.MetadataKeyReplyToContextID: "context-1",
			},
		}})
	}, 0)
	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !reported {
		t.Fatalf("expected project workflow to report reply target from event metadata")
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowContinuesAfterReportResponseFails(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		return workflows.TaskWorkflowResult{
			Completed:       true,
			Status:          activities.NextActionStatusCompleted,
			Event:           input.Event,
			ResponseMessage: "done",
			Report:          true,
		}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.RegisterActivityWithOptions((&activities.Activities{}).ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})

	env.OnActivity("Activities.ReportResponse", mock.Anything, mock.MatchedBy(func(request activities.ReportResponseRequest) bool {
		return request.Event.ID == "event-1"
	})).Return(activities.ReportResponseResult{}, errors.New("discord upload failed")).Once()
	reportedSecond := false
	env.OnActivity("Activities.ReportResponse", mock.Anything, mock.MatchedBy(func(request activities.ReportResponseRequest) bool {
		reportedSecond = request.Event.ID == "event-2"
		return reportedSecond
	})).Run(func(mock.Arguments) {
		env.CancelWorkflow()
	}).Return(activities.ReportResponseResult{}, nil).Once()

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			Body:        "do work",
		}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{
			ID:          "event-2",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			Body:        "next work",
		}})
	}, time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("expected workflow to continue with second event, got %#v", seen)
	}
	if !reportedSecond {
		t.Fatalf("expected second event to be reported")
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowReportsAfterTaskWorkflowFails(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	taskErr := errors.New("activity error: ContextWindowExceededError: Input tokens exceed the configured limit")
	env.RegisterWorkflowWithOptions(func(_ workflow.Context, _ workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		return workflows.TaskWorkflowResult{}, taskErr
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})
	env.RegisterActivityWithOptions((&activities.Activities{}).ReportResponse, activity.RegisterOptions{Name: "Activities.ReportResponse"})

	reported := false
	env.OnActivity("Activities.ReportResponse", mock.Anything, mock.MatchedBy(func(request activities.ReportResponseRequest) bool {
		reported = request.Event.ID == "event-1" &&
			strings.Contains(request.Message, "model context window was exceeded")
		return reported
	})).Run(func(mock.Arguments) {
		env.CancelWorkflow()
	}).Return(activities.ReportResponseResult{}, nil).Once()

	event := domain.Event{
		ID:          "event-1",
		ProjectID:   "project-1",
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		Body:        "do work",
	}
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: event})
	}, 0)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if !reported {
		t.Fatalf("expected project workflow to report after sub task failure")
	}
	env.AssertExpectations(t)
}

func TestScheduledDispatchWorkflowEnqueuesSyntheticEvent(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ScheduledDispatchWorkflow)
	env.RegisterActivityWithOptions((&activities.Activities{}).EnqueueScheduledEvent, activity.RegisterOptions{Name: scheduled.EnqueueScheduledEventName})

	enqueued := false
	env.OnActivity(scheduled.EnqueueScheduledEventName, mock.Anything, mock.MatchedBy(func(request scheduled.EnqueueScheduledEventRequest) bool {
		event := request.Event
		enqueued = event.ProjectID == "project-1" &&
			event.Body == "send hello" &&
			event.ChannelID == "channel-1" &&
			event.ThreadID == "" &&
			event.Metadata[scheduled.EventMetadataScheduleID] == "schedule-1" &&
			event.Metadata[scheduled.EventMetadataQueuePolicy] == scheduled.QueuePolicyScheduledTask &&
			event.Provenance.Source == "schedule"
		return enqueued
	})).Return(nil).Once()

	env.ExecuteWorkflow(workflows.ScheduledDispatchWorkflow, scheduled.DispatchWorkflowInput{
		ProjectID:        "project-1",
		ScheduleID:       "schedule-1",
		ScheduleName:     "daily hello",
		Task:             "send hello",
		CreatedByEventID: "event-creator",
		SourceEvent: domain.Event{
			ID:          "event-creator",
			ProjectID:   "project-1",
			ChannelID:   "channel-1",
			ChannelType: domain.ChannelTypeDiscord,
			ThreadID:    "thread-1",
			ActorName:   "luka",
		},
	})

	if err := env.GetWorkflowError(); err != nil {
		t.Fatalf("scheduled dispatch workflow failed: %v", err)
	}
	if !enqueued {
		t.Fatalf("expected scheduled event to be enqueued")
	}
	env.AssertExpectations(t)
}

func TestProjectWorkflowQueuesScheduledEventAsNewTaskWhileActive(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		if input.Event.ID == "event-1" {
			_ = workflow.Sleep(ctx, 5*time.Millisecond)
		}
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: domain.Event{ID: "event-1", ProjectID: "project-1", Body: "do work"}})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: scheduledEvent("event-2", "schedule-1")})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 20*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1,event-2" {
		t.Fatalf("expected scheduled event to run as a queued task, got %#v", seen)
	}
}

func TestProjectWorkflowSkipsOverlappingScheduledEvent(t *testing.T) {
	t.Parallel()

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ProjectWorkflow)
	var seen []string
	env.RegisterWorkflowWithOptions(func(ctx workflow.Context, input workflows.TaskWorkflowInput) (workflows.TaskWorkflowResult, error) {
		seen = append(seen, input.Event.ID)
		_ = workflow.Sleep(ctx, 5*time.Millisecond)
		return workflows.TaskWorkflowResult{Completed: true}, nil
	}, workflow.RegisterOptions{Name: workflows.TaskWorkflowName})

	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: scheduledEvent("event-1", "schedule-1")})
	}, 0)
	env.RegisterDelayedCallback(func() {
		env.SignalWorkflow(workflows.SignalEnqueueEvent, workflows.EnqueueEventSignal{Event: scheduledEvent("event-2", "schedule-1")})
	}, time.Millisecond)
	env.RegisterDelayedCallback(func() {
		env.CancelWorkflow()
	}, 20*time.Millisecond)

	env.ExecuteWorkflow(workflows.ProjectWorkflow, workflows.ProjectWorkflowInput{ProjectID: "project-1"})
	if err := env.GetWorkflowError(); err == nil {
		t.Fatalf("expected cancellation error")
	}
	if strings.Join(seen, ",") != "event-1" {
		t.Fatalf("expected overlapping scheduled event to be skipped, got %#v", seen)
	}
}

func scheduledEvent(eventID, scheduleID string) domain.Event {
	return domain.Event{
		ID:        eventID,
		ProjectID: "project-1",
		Body:      "scheduled task",
		Metadata: domain.Metadata{
			scheduled.EventMetadataScheduleID:  scheduleID,
			scheduled.EventMetadataQueuePolicy: scheduled.QueuePolicyScheduledTask,
		},
	}
}
