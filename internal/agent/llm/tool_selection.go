package llm

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
	toolregistry "github.com/opencto/opencto/internal/tools"
)

type shellToolInput struct {
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	WorkingDir  string   `json:"working_dir,omitempty"`
	TimeoutMs   int      `json:"timeout_ms,omitempty"`
	Description string   `json:"description,omitempty"`
	Destructive bool     `json:"destructive,omitempty"`
	WorkItemID  string   `json:"work_item_id,omitempty"`
}

func toolChoiceFromToolCall(call llms.ToolCall, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
	if call.FunctionCall == nil {
		return agent.ToolChoice{}, fmt.Errorf("tool call %q missing function payload", call.ID)
	}

	definition, ok := toolregistry.DefinitionByName(call.FunctionCall.Name)
	if !ok {
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool call %q", call.FunctionCall.Name)
	}

	switch definition.Type {
	case domain.ToolTypeShell:
		var args shellToolInput
		if err := json.Unmarshal([]byte(call.FunctionCall.Arguments), &args); err != nil {
			return agent.ToolChoice{}, fmt.Errorf("decode %s tool arguments: %w", definition.Name, err)
		}
		return shellToolChoiceFromInput(definition, call, args, input)
	default:
		return agent.ToolChoice{}, fmt.Errorf("unsupported tool type %q for call %q", definition.Type, call.FunctionCall.Name)
	}
}

func shellToolChoiceFromInput(definition toolregistry.Definition, call llms.ToolCall, args shellToolInput, input agent.ToolSelectionInput) (agent.ToolChoice, error) {
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

	return agent.ToolChoice{
		ToolCallID:   call.ID,
		Type:         definition.Type,
		Intent:       firstNonEmpty(strings.TrimSpace(args.Description), commandText),
		Command:      command,
		Args:         commandArgs,
		WorkingDir:   firstNonEmpty(strings.TrimSpace(args.WorkingDir), strings.TrimSpace(input.Runtime.WorkspaceRoot)),
		TimeoutMs:    clampToolTimeoutMs(args.TimeoutMs),
		InputSummary: firstNonEmpty(strings.TrimSpace(args.Description), commandText, strings.TrimSpace(input.Context.Event.Body)),
		Destructive:  args.Destructive,
		Metadata:     metadata,
	}, nil
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
		return 120000
	case value > 600000:
		return 600000
	default:
		return value
	}
}
