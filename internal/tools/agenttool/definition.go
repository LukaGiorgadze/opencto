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

	AgentToolDescription = `Launch a agent to handle a bounded, multi-step task independently.

The agent receives its own system prompt, the provided goal, dynamic context or instructions, and inherited parent conversation context.

The result is returned to the parent as a tool observation — not shown directly to the user.

Constraints:
- Prefer first-class specialized tools when they directly match the user's request. Use Agent for delegated work that genuinely needs an independent multi-step loop.
- The goal and prompt must still be explicit enough for a fresh worker to act without guessing.
- Recursive Agent calls are blocked.`

	DefaultMaxTurns = 50
	MaxTurnsLimit   = 100
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

//go:embed schema.json
var agentToolSchema json.RawMessage

func AgentToolSchema() json.RawMessage {
	return append(json.RawMessage(nil), agentToolSchema...)
}

func PromptSummary(goal string) string {
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return "Run agent"
	}
	return "Run agent: " + goal
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
