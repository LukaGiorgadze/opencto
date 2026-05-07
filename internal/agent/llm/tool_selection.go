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
	edittool "github.com/opencto/opencto/internal/tools/edit"
	globtool "github.com/opencto/opencto/internal/tools/glob"
	greptool "github.com/opencto/opencto/internal/tools/grep"
	readtool "github.com/opencto/opencto/internal/tools/read"
	scheduletool "github.com/opencto/opencto/internal/tools/schedule"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
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

	raw := json.RawMessage(strings.TrimSpace(call.FunctionCall.Arguments))
	switch definition.Type {
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
		return structuredToolChoiceFromInput(definition, call, raw, input, "read "+strings.TrimSpace(args.FilePath)), nil
	case domain.ToolTypeEdit:
		var args edittool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, "edit "+strings.TrimSpace(args.FilePath)), nil
	case domain.ToolTypeWrite:
		var args writetool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, "write "+strings.TrimSpace(args.FilePath)), nil
	case domain.ToolTypeGlob:
		var args globtool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		summary := "glob " + strings.TrimSpace(args.Pattern)
		if path := strings.TrimSpace(args.Path); path != "" {
			summary += " in " + path
		}
		return structuredToolChoiceFromInputWithWorkingDir(definition, call, raw, input, summary, toolWorkingDir(args.Cwd, input.Runtime.WorkspaceRoot)), nil
	case domain.ToolTypeGrep:
		var args greptool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		summary := "grep " + strings.TrimSpace(args.Pattern)
		if path := strings.TrimSpace(args.Path); path != "" {
			summary += " in " + path
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, summary), nil
	case domain.ToolTypeSchedule:
		var args scheduletool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return scheduleToolChoiceFromInput(definition, call, raw, input, args), nil
	case domain.ToolTypeSkill:
		var args skilltool.Request
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return structuredToolChoiceFromInput(definition, call, raw, input, "load skill "+strings.TrimSpace(args.SkillID)), nil
	default:
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool type %q for call %q", definition.Type, call.FunctionCall.Name)
	}
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

func scheduleToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, args scheduletool.Request) agent.ToolChoice {
	operation := strings.TrimSpace(args.Operation)
	summary := "schedule " + operation
	if name := strings.TrimSpace(args.Name); name != "" {
		summary += " " + name
	} else if id := strings.TrimSpace(args.ScheduleID); id != "" {
		summary += " " + id
	}
	if task := strings.TrimSpace(args.Task); task != "" {
		summary += ": " + task
	} else if description := strings.TrimSpace(args.Description); description != "" {
		summary += ": " + description
	}
	idempotency := domain.ToolIdempotencyNonIdempotent
	if operation == scheduletool.OperationList || operation == scheduletool.OperationDescribe {
		idempotency = domain.ToolIdempotencyReadOnly
	}
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
		Destructive:  operation == scheduletool.OperationDelete || operation == scheduletool.OperationTrigger,
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
	summary = firstNonEmpty(summary, definition.Name+" tool call", strings.TrimSpace(input.Context.Event.Body))
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
