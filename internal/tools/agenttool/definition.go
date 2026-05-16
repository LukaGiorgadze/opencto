package agenttool

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

const (
	AgentToolName = "Agent"

	AgentToolDescription = `Launch a focused sub-agent to handle a complex, multi-step task.

The sub-agent receives a fresh sub-agent system prompt, the provided goal, the provided dynamic context/instructions, and only the allowed tools. It does not receive the parent conversation by default.

Use this for bounded delegation where another agent can inspect, edit, or validate independently. The result is returned to the parent as a tool observation; it is not shown directly to the user.

Constraints:
- allowed_tools must contain domain.ToolType values such as Read, Edit, Write, Glob, Grep, Exec, Skill, WorkflowOperation.
- Omit allowed_tools to allow every normal tool except Agent.
- Recursive Agent calls are blocked.
- The prompt must be self-contained.`

	DefaultMaxTurns = 20
	MaxTurnsLimit   = 50
)

type Request struct {
	Goal         string             `json:"goal"`
	Prompt       string             `json:"prompt"`
	AllowedTools *[]domain.ToolType `json:"allowed_tools,omitempty"`
	MaxTurns     int                `json:"max_turns,omitempty"`
}

type Result struct {
	Message      string            `json:"message"`
	TurnCount    int               `json:"turn_count"`
	FilesTouched []string          `json:"files_touched,omitempty"`
	ToolsUsed    []domain.ToolType `json:"tools_used,omitempty"`
}

//go:embed agent_schema.json
var agentToolSchema json.RawMessage

func AgentToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), agentToolSchema...)
}

func PromptSummary(goal string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "Run sub-agent"
	}
	return "Run sub-agent: " + goal
}

func NormalizeMaxTurns(value int) int {
	if value <= 0 {
		return DefaultMaxTurns
	}
	if value > MaxTurnsLimit {
		return MaxTurnsLimit
	}
	return value
}

func ValidateAllowedTools(allowed []domain.ToolType, supported func(domain.ToolType) bool) ([]domain.ToolType, error) {
	seen := map[domain.ToolType]bool{}
	cleaned := make([]domain.ToolType, 0, len(allowed))
	for _, toolType := range allowed {
		toolType = domain.ToolType(strings.TrimSpace(string(toolType)))
		if toolType == "" {
			return nil, fmt.Errorf("allowed_tools contains an empty tool type")
		}
		if toolType == domain.ToolTypeAgent {
			return nil, fmt.Errorf("Agent tool recursion is not allowed")
		}
		if supported != nil && !supported(toolType) {
			return nil, fmt.Errorf("unsupported allowed tool type %q", toolType)
		}
		if seen[toolType] {
			continue
		}
		seen[toolType] = true
		cleaned = append(cleaned, toolType)
	}
	return cleaned, nil
}
