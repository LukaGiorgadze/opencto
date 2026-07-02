package agentemail

import (
	"fmt"
	"strings"
)

func PromptSummary(req Request) string {
	action := normalizeAction(req.Action)
	switch action {
	case ActionSetupCreate:
		return "Create or reuse AgentEmail inbox"
	case ActionListMessages:
		if inboxID := strings.TrimSpace(req.InboxID); inboxID != "" {
			return fmt.Sprintf("List AgentEmail messages in %s", inboxID)
		}
		return "List AgentEmail messages"
	case ActionSearchMessages:
		query := strings.TrimSpace(req.Query)
		if query == "" {
			return "Search AgentEmail messages"
		}
		return fmt.Sprintf("Search AgentEmail messages for %q", query)
	case ActionReadMessage:
		messageID := strings.TrimSpace(req.MessageID)
		if messageID == "" {
			return "Read AgentEmail message"
		}
		return fmt.Sprintf("Read AgentEmail message %s", messageID)
	case ActionWaitForMessage:
		query := strings.TrimSpace(req.Query)
		if query == "" {
			return "Wait for AgentEmail message"
		}
		return fmt.Sprintf("Wait for AgentEmail message matching %q", query)
	case ActionSendMessage:
		if subject := strings.TrimSpace(req.Subject); subject != "" {
			return fmt.Sprintf("Send AgentEmail message %q", subject)
		}
		return "Send AgentEmail message"
	default:
		return "Use AgentEmail"
	}
}
