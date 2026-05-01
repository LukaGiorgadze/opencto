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
	shelltool "github.com/opencto/opencto/internal/tools/shell"
	skilltool "github.com/opencto/opencto/internal/tools/skill"
	writetool "github.com/opencto/opencto/internal/tools/write"
)

type shellToolInput struct {
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	WorkingDir   string   `json:"working_dir,omitempty"`
	TimeoutMs    int      `json:"timeout_ms,omitempty"`
	RunMode      string   `json:"run_mode,omitempty"`
	Idempotency  string   `json:"idempotency,omitempty"`
	ProcessScope string   `json:"process_scope,omitempty"`
	Description  string   `json:"description,omitempty"`
	Destructive  bool     `json:"destructive,omitempty"`
	WorkItemID   string   `json:"work_item_id,omitempty"`
}

func toolChoiceFromToolCalls(calls []llms.ToolCall, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if len(calls) == 0 {
		return agent.ToolChoice{}, fmt.Errorf("tool call is required")
	}
	if len(calls) == 1 {
		return toolChoiceFromToolCall(calls[0], input)
	}

	choices := make([]agent.ToolChoice, 0, len(calls))
	for _, call := range calls {
		choice, err := toolChoiceFromToolCall(call, input)
		if err != nil {
			return agent.ToolChoice{}, err
		}
		choices = append(choices, choice)
	}
	return multiToolChoiceFromChoices(choices)
}

func multiToolChoiceFromChoices(choices []agent.ToolChoice) (agent.ToolChoice, error) {
	for _, choice := range choices {
		if choice.Type != domain.ToolTypeShell {
			return agent.ToolChoice{}, fmt.Errorf("tool %q does not support multiple actions", choice.Type)
		}
		if choice.RunMode == domain.ToolRunModeStartBackground {
			return agent.ToolChoice{}, fmt.Errorf("shell does not support multiple background actions")
		}
	}
	return multiShellToolChoice(choices)
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
	case domain.ToolTypeShell:
		var args shellToolInput
		if err := decodeToolArguments(definition.Name, raw, &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return shellToolChoiceFromInput(definition, call, raw, args, input)
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
		return structuredToolChoiceFromInput(definition, call, raw, input, summary), nil
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

func multiShellToolChoice(choices []agent.ToolChoice) (agent.ToolChoice, error) {
	actions := make([]shelltool.Action, 0, len(choices))
	ids := make([]string, 0, len(choices))
	intents := make([]string, 0, len(choices))
	timeoutMs := 0
	destructive := false
	idempotency := domain.ToolIdempotencyReadOnly
	workItemID := ""

	for _, choice := range choices {
		actions = append(actions, shelltool.Action{
			Intent:     choice.Intent,
			Command:    choice.Command,
			Args:       append([]string(nil), choice.Args...),
			WorkingDir: choice.WorkingDir,
			TimeoutMs:  choice.TimeoutMs,
		})
		ids = append(ids, strings.TrimSpace(choice.ToolCallID))
		intents = append(intents, strings.TrimSpace(firstNonEmpty(choice.Intent, choice.Command)))
		timeoutMs += clampToolTimeoutMs(choice.TimeoutMs)
		destructive = destructive || choice.Destructive
		idempotency = combineToolIdempotency(idempotency, choice.Idempotency)
		if workItemID == "" && choice.Metadata != nil {
			workItemID = strings.TrimSpace(choice.Metadata["work_item_id"])
		}
	}

	raw, err := json.Marshal(shelltool.BatchInput{Actions: actions})
	if err != nil {
		return agent.ToolChoice{}, err
	}

	metadata := map[string]string{
		"model_tool":        toolregistry.CommandToolName,
		"tool_call_id":      firstNonEmpty(ids...),
		"tool_call_ids":     strings.Join(ids, ","),
		"multi_action":      "true",
		"multi_action_size": fmt.Sprintf("%d", len(actions)),
		"run_mode":          string(domain.ToolRunModeWaitForExit),
		"idempotency":       string(idempotency),
		"process_scope":     string(domain.ProcessScopeTask),
	}
	if workItemID != "" {
		metadata["work_item_id"] = workItemID
	}

	return agent.ToolChoice{
		ToolCallID:   firstNonEmpty(ids...),
		Type:         domain.ToolTypeShell,
		Intent:       fmt.Sprintf("run %d shell commands", len(actions)),
		Command:      "shell-batch",
		Input:        json.RawMessage(raw),
		WorkingDir:   choices[0].WorkingDir,
		TimeoutMs:    clampToolTimeoutMs(timeoutMs),
		RunMode:      domain.ToolRunModeWaitForExit,
		Idempotency:  idempotency,
		ProcessScope: domain.ProcessScopeTask,
		InputSummary: strings.Join(trimStringList(intents, 10), "; "),
		Destructive:  destructive,
		Metadata:     metadata,
	}, nil
}

func combineToolIdempotency(current, next domain.ToolIdempotency) domain.ToolIdempotency {
	switch {
	case current == domain.ToolIdempotencyNonIdempotent || next == domain.ToolIdempotencyNonIdempotent:
		return domain.ToolIdempotencyNonIdempotent
	case current == domain.ToolIdempotencyUnknown || next == domain.ToolIdempotencyUnknown:
		return domain.ToolIdempotencyUnknown
	case current == domain.ToolIdempotencyIdempotent || next == domain.ToolIdempotencyIdempotent:
		return domain.ToolIdempotencyIdempotent
	default:
		return domain.ToolIdempotencyReadOnly
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

func shellToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, args shellToolInput, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	commandText := strings.TrimSpace(args.Command)
	if commandText == "" {
		return agent.ToolChoice{}, fmt.Errorf("%s tool requires a command", definition.Name)
	}
	command := commandText
	commandArgs := trimStringList(args.Args, 100)
	wrapped := false
	if len(commandArgs) == 0 {
		if shouldUseShell(commandText) {
			command, commandArgs = shellCommandForRuntime(input.Runtime, commandText)
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
	if workItemID := strings.TrimSpace(args.WorkItemID); workItemID != "" {
		metadata["work_item_id"] = workItemID
	}
	if wrapped {
		metadata["wrapped_shell_command"] = "true"
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
		WorkingDir:   firstNonEmpty(strings.TrimSpace(args.WorkingDir), strings.TrimSpace(input.Runtime.WorkspaceRoot)),
		TimeoutMs:    clampToolTimeoutMs(args.TimeoutMs),
		RunMode:      runMode,
		Idempotency:  idempotency,
		ProcessScope: processScope,
		InputSummary: firstNonEmpty(strings.TrimSpace(args.Description), commandText, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  args.Destructive,
		Metadata:     metadata,
	}, nil
}

func structuredToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, raw json.RawMessage, input agent.ToolSelectionInput, summary string) agent.ToolChoice {
	summary = firstNonEmpty(summary, definition.Name+" tool call", strings.TrimSpace(input.Context.Event.Body))
	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       summary,
		Input:        cloneRawMessage(raw),
		WorkingDir:   strings.TrimSpace(input.Runtime.WorkspaceRoot),
		TimeoutMs:    clampToolTimeoutMs(0),
		InputSummary: firstNonEmpty(summary, strings.TrimSpace(input.Context.Event.Body)),
		Metadata: map[string]string{
			"model_tool":   definition.Name,
			"tool_call_id": call.ID,
		},
	}
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
		return domain.ProcessScopeTask
	}
}

func shouldUseShell(command string) bool {
	return strings.ContainsAny(command, "\r\n;&|<>*$`()\"'")
}

func shellCommandForRuntime(runtime agent.RuntimeContext, command string) (string, []string) {
	shell := firstNonEmpty(strings.TrimSpace(runtime.Shell), defaultShellForOS(runtime.OS))
	name := strings.ToLower(filepathBase(shell))
	switch name {
	case "cmd", "cmd.exe":
		return shell, []string{"/C", command}
	case "powershell", "powershell.exe", "pwsh", "pwsh.exe":
		return shell, []string{"-Command", command}
	default:
		return shell, []string{"-c", command}
	}
}

func defaultShellForOS(osName string) string {
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
