package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
	agenttool "github.com/opencto/opencto/internal/tools/agenttool"
	edittool "github.com/opencto/opencto/internal/tools/edit"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	memorytool "github.com/opencto/opencto/internal/tools/memory"
	readtool "github.com/opencto/opencto/internal/tools/read"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	scheduletool "github.com/opencto/opencto/internal/tools/workflowschedule"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

type execToolInput struct {
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	Cwd          string   `json:"cwd,omitempty"`
	TimeoutMs    int      `json:"timeout_ms,omitempty"`
	RunMode      string   `json:"run_mode,omitempty"`
	Idempotency  string   `json:"idempotency,omitempty"`
	ProcessScope string   `json:"process_scope,omitempty"`
	Description  string   `json:"description,omitempty"`
	Destructive  bool     `json:"destructive,omitempty"`
}

func toolChoicesFromToolCalls(calls []llms.ToolCall, input agent.ToolSelectionInput) ([]agent.ToolChoice, error) {
	if len(calls) == 0 {
		return nil, fmt.Errorf("tool call is required")
	}
	choices := make([]agent.ToolChoice, 0, len(calls))
	for _, call := range calls {
		choice, err := toolChoiceFromToolCall(call, input)
		if err != nil {
			return nil, err
		}
		choices = append(choices, choice)
	}
	return choices, nil
}

func toolChoiceFromToolCall(call llms.ToolCall, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if call.FunctionCall == nil {
		return agent.ToolChoice{}, fmt.Errorf("tool call %q missing function payload", call.ID)
	}

	definition, ok := toolregistry.DefinitionByName(call.FunctionCall.Name)
	if !ok {
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool call %q", call.FunctionCall.Name)
	}
	if input.RestrictTools && !toolTypeAllowed(definition.Type, input.ToolAllowlist) {
		return agent.ToolChoice{}, fmt.Errorf("tool %q is not allowed in this agent context", definition.Type)
	}

	raw := json.RawMessage(strings.TrimSpace(call.FunctionCall.Arguments))
	switch definition.Type {
	case domain.ToolTypeAgent:
		var args agenttool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return agentToolChoiceFromInput(definition, call, raw, input, args), nil
	case domain.ToolTypeExec:
		var args execToolInput
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return execToolChoiceFromInput(definition, call, raw, args, input)
	case domain.ToolTypeRead:
		var args readtool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, readtool.PromptSummary(args.FilePath)), nil
	case domain.ToolTypeEdit:
		var args edittool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, edittool.PromptSummary(args.FilePath)), nil
	case domain.ToolTypeWrite:
		var args writetool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, writetool.PromptSummary(args.FilePath)), nil
	case domain.ToolTypeGlob:
		var args globtool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		summary := globtool.PromptSummary(args.Pattern, args.Path)
		return structuredToolChoiceFromInputWithWorkingDir(definition, call, raw, input, summary, toolWorkingDir(args.Cwd, input.Runtime.WorkspaceRoot)), nil
	case domain.ToolTypeGrep:
		var args greptool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		summary := greptool.PromptSummary(args.Pattern, args.Path)
		return structuredToolChoiceFromInput(definition, call, raw, input, summary), nil
	case domain.ToolTypeMemoryProposeAdd:
		var args memorytool.ProposeAddRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return memoryToolChoiceFromInput(definition, call, raw, input, memorytool.PromptAddSummary(args.Content), domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeMemorySearch:
		var args memorytool.SearchRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return memoryToolChoiceFromInput(definition, call, raw, input, memorytool.PromptSearchSummary(args.Query), domain.ToolIdempotencyReadOnly), nil
	case domain.ToolTypeMemoryList:
		var args memorytool.ListRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return memoryToolChoiceFromInput(definition, call, raw, input, listMemorySummary(args), domain.ToolIdempotencyReadOnly), nil
	case domain.ToolTypeMemoryProposeUpdate:
		var args memorytool.ProposeUpdateRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return memoryToolChoiceFromInput(definition, call, raw, input, memorytool.PromptUpdateSummary(args.MemoryID), domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeMemoryProposeForget:
		var args memorytool.ProposeForgetRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return memoryToolChoiceFromInput(definition, call, raw, input, forgetMemorySummary(args), domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeWorkflowCreate:
		var args scheduletool.CreateRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return workflowToolChoiceFromInput(definition, call, raw, input, scheduletool.OperationCreate, args.WorkflowID, "", args.Prompt, domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeWorkflowUpdate:
		var args scheduletool.UpdateRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return workflowToolChoiceFromInput(definition, call, raw, input, scheduletool.OperationUpdate, args.WorkflowID, "", args.Prompt, domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeWorkflowDelete:
		var args scheduletool.DeleteRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return workflowToolChoiceFromInput(definition, call, raw, input, scheduletool.OperationDelete, args.WorkflowID, "", "", domain.ToolIdempotencyNonIdempotent), nil
	case domain.ToolTypeWorkflowOperation:
		var args scheduletool.OperationRequest
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		idempotency := domain.ToolIdempotencyNonIdempotent
		operation := strings.ToLower(strings.TrimSpace(args.Operation))
		if operation == scheduletool.OperationList || operation == scheduletool.OperationDescribe {
			idempotency = domain.ToolIdempotencyReadOnly
		}
		return workflowToolChoiceFromInput(definition, call, raw, input, operation, args.WorkflowID, "", "", idempotency), nil
	case domain.ToolTypeSkill:
		var args skilltool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, skilltool.PromptSummary(args.SkillID)), nil
	default:
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool type %q for call %q", definition.Type, call.FunctionCall.Name)
	}
}

func toolTypeAllowed(toolType domain.ToolType, allowed []domain.ToolType) bool {
	for _, item := range allowed {
		if item == toolType {
			return true
		}
	}
	return false
}

func agentToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, args agenttool.Request) agent.ToolChoice {
	summary := agenttool.PromptSummary(args.Goal)
	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Input:        cloneRawMessage(raw),
		WorkingDir:   strings.TrimSpace(input.Runtime.WorkspaceRoot),
		TimeoutMs:    clampToolTimeoutMs(0),
		RunMode:      domain.ToolRunModeWaitForExit,
		Idempotency:  domain.ToolIdempotencyNonIdempotent,
		ProcessScope: domain.ProcessScopeStopOnFinish,
		InputSummary: firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  false,
		Metadata: map[string]string{
			"model_tool":   definition.Name,
			"tool_call_id": call.ID,
		},
	}
}

func memoryToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, summary string, idempotency domain.ToolIdempotency) agent.ToolChoice {
	choice := structuredToolChoiceFromInput(definition, call, raw, input, summary)
	choice.RunMode = domain.ToolRunModeWaitForExit
	choice.Idempotency = idempotency
	choice.ProcessScope = domain.ProcessScopeStopOnFinish
	if input.Onboarding.Active {
		if choice.Metadata == nil {
			choice.Metadata = map[string]string{}
		}
		choice.Metadata["onboarding"] = "true"
		choice.Metadata["onboarding_source"] = strings.TrimSpace(input.Onboarding.Source)
	}
	return choice
}

func forgetMemorySummary(args memorytool.ProposeForgetRequest) string {
	ids := trimStringList(args.MemoryIDs, 20)
	if len(ids) > 0 {
		return memoryForgetSummary(strings.Join(ids, ", "), "", "")
	}
	tags := trimStringList(args.Tags, 20)
	if len(tags) > 0 {
		return memoryForgetSummary("", strings.Join(tags, ", "), "")
	}
	if scope := strings.TrimSpace(args.Scope); scope != "" {
		return memoryForgetSummary("", "", scope)
	}
	return memoryForgetSummary("", "", "")
}

func memoryForgetSummary(memoryIDs, tags, scope string) string {
	return memorytool.PromptForgetSummary(memoryIDs, tags, scope)
}

func listMemorySummary(args memorytool.ListRequest) string {
	scope := strings.TrimSpace(args.Scope)
	if scope == "" {
		scope = memorytool.ScopeAll
	}
	kind := strings.TrimSpace(args.Kind)
	return memorytool.PromptListSummary(scope, kind)
}

func decodeToolArguments(toolName string, raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return fmt.Errorf("%s tool arguments are required", toolName)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return fmt.Errorf("multiple JSON values")
	case io.EOF:
		return nil
	default:
		return err
	}
}

func execToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, args execToolInput, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	commandText := strings.TrimSpace(args.Command)
	if commandText == "" {
		return agent.ToolChoice{}, fmt.Errorf("%s tool requires a command", definition.Name)
	}
	command := commandText
	commandArgs := trimStringList(args.Args, 100)
	wrapped := false
	if len(commandArgs) == 0 {
		if shouldWrapExecCommand(commandText) {
			command, commandArgs = execCommandForRuntime(input.Runtime, commandText)
			wrapped = true
		} else {
			fields := strings.Fields(commandText)
			if len(fields) > 1 {
				command = fields[0]
				commandArgs = trimStringList(fields[1:], 100)
			}
		}
	}

	metadata := map[string]string{
		"model_tool":   definition.Name,
		"tool_call_id": call.ID,
	}
	if wrapped {
		metadata["wrapped_exec_command"] = "true"
		metadata["original_command"] = commandText
	}
	runMode := normalizeToolRunMode(args.RunMode)
	idempotency := normalizeToolIdempotency(args.Idempotency)
	processScope := normalizeProcessScope(args.ProcessScope)
	metadata["run_mode"] = string(runMode)
	metadata["idempotency"] = string(idempotency)
	metadata["process_scope"] = string(processScope)

	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       firstNonEmpty(strings.TrimSpace(args.Description), commandText),
		Command:      command,
		Args:         commandArgs,
		Input:        cloneRawMessage(raw),
		WorkingDir:   toolWorkingDir(args.Cwd, input.Runtime.WorkspaceRoot),
		TimeoutMs:    clampToolTimeoutMs(args.TimeoutMs),
		RunMode:      runMode,
		Idempotency:  idempotency,
		ProcessScope: processScope,
		InputSummary: firstNonEmpty(strings.TrimSpace(args.Description), commandText, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  args.Destructive,
		Metadata:     metadata,
	}, nil
}

func workflowToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, operation, workflowID, name, description string, idempotency domain.ToolIdempotency) agent.ToolChoice {
	operation = strings.TrimSpace(operation)
	summary := scheduletool.PromptSummary(operation, workflowID, name, description)
	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Input:        cloneRawMessage(raw),
		WorkingDir:   strings.TrimSpace(input.Runtime.WorkspaceRoot),
		TimeoutMs:    clampToolTimeoutMs(0),
		RunMode:      domain.ToolRunModeWaitForExit,
		Idempotency:  idempotency,
		ProcessScope: domain.ProcessScopeStopOnFinish,
		InputSummary: firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  false,
		Metadata: map[string]string{
			"model_tool":   definition.Name,
			"tool_call_id": call.ID,
		},
	}
}

func structuredToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, summary string) agent.ToolChoice {
	return structuredToolChoiceFromInputWithWorkingDir(definition, call, raw, input, summary, strings.TrimSpace(input.Runtime.WorkspaceRoot))
}

func structuredToolChoiceFromInputWithWorkingDir(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, summary, workingDir string) agent.ToolChoice {
	summary = firstNonEmpty(summary, toolregistry.PromptDefaultSummary(definition.Name), strings.TrimSpace(input.Context.Event.Body))
	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       summary,
		Input:        cloneRawMessage(raw),
		WorkingDir:   workingDir,
		TimeoutMs:    clampToolTimeoutMs(0),
		InputSummary: firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Metadata: map[string]string{
			"model_tool":   definition.Name,
			"tool_call_id": call.ID,
		},
	}
}

func toolWorkingDir(cwd, workspaceRoot string) string {
	return firstNonEmpty(cwd, workspaceRoot)
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), raw...)
}

func normalizeToolRunMode(value string) domain.ToolRunMode {
	switch domain.ToolRunMode(strings.ToLower(strings.TrimSpace(value))) {
	case domain.ToolRunModeStartBackground:
		return domain.ToolRunModeStartBackground
	default:
		return domain.ToolRunModeWaitForExit
	}
}

func normalizeToolIdempotency(value string) domain.ToolIdempotency {
	switch domain.ToolIdempotency(strings.ToLower(strings.TrimSpace(value))) {
	case domain.ToolIdempotencyReadOnly:
		return domain.ToolIdempotencyReadOnly
	case domain.ToolIdempotencyIdempotent:
		return domain.ToolIdempotencyIdempotent
	case domain.ToolIdempotencyNonIdempotent:
		return domain.ToolIdempotencyNonIdempotent
	default:
		return domain.ToolIdempotencyUnknown
	}
}

func normalizeProcessScope(value string) domain.ProcessScope {
	switch domain.ProcessScope(strings.ToLower(strings.TrimSpace(value))) {
	case domain.ProcessScopeProject:
		return domain.ProcessScopeProject
	default:
		return domain.ProcessScopeStopOnFinish
	}
}

func shouldWrapExecCommand(command string) bool {
	return strings.ContainsAny(command, "\r\n;&|<>*$`()\"'")
}

func execCommandForRuntime(runtime agent.RuntimeContext, command string) (string, []string) {
	exec := firstNonEmpty(strings.TrimSpace(runtime.Exec), defaultExecForOS(runtime.OS))
	name := strings.ToLower(filepathBase(exec))
	switch name {
	case "cmd", "cmd.exe":
		return exec, []string{"/C", command}
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return exec, []string{"-Command", command}
	default:
		return exec, []string{"-c", command}
	}
}

func defaultExecForOS(osName string) string {
	if strings.EqualFold(osName, "windows") {
		return "cmd"
	}
	return "sh"
}

func filepathBase(path string) string {
	normalized := strings.ReplaceAll(path, "\\", "/")
	index := strings.LastIndex(normalized, "/")
	if index == -1 {
		return normalized
	}
	return normalized[index+1:]
}

func clampToolTimeoutMs(value int) int {
	switch {
	case value <= 0:
		return 60000
	case value > 600000:
		return 600000
	default:
		return value
	}
}
