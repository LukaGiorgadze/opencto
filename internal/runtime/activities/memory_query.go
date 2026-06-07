package activities

import (
	"strings"

	"github.com/opencto/opencto/internal/domain"
)

func prepareMemoryEmbeddingQuery(query string, currentEvent domain.Event, additionalEvents []domain.Event, conversation []domain.ConversationMessage) string {
	query = cleanMemoryEmbeddingQueryText(query)
	current := cleanMemoryEmbeddingQueryText(currentEvent.Body)
	if query == "" {
		query = current
	}

	var anchors []string
	if query != "" {
		anchors = append(anchors, "Search memory for: "+query)
	}
	if current != "" && current != query {
		anchors = append(anchors, "Current request: "+current)
	}
	for _, event := range additionalEvents {
		if followup := cleanMemoryEmbeddingQueryText(event.Body); followup != "" {
			anchors = append(anchors, "Follow-up: "+followup)
		}
	}
	if len(anchors) == 0 {
		return ""
	}
	return buildMemoryEmbeddingQuery(anchors, memoryEmbeddingQueryContextLines(currentEvent, additionalEvents, conversation))
}

func buildMemoryEmbeddingQuery(anchors []string, context []string) string {
	var builder memoryEmbeddingQueryBuilder
	for _, line := range anchors {
		if !builder.append(line) {
			return builder.String()
		}
	}
	if len(context) == 0 || !builder.append("Recent context:") {
		return builder.String()
	}
	contextStart := len(builder.lines)
	for _, line := range context {
		if !builder.append(line) {
			break
		}
	}
	if len(builder.lines) == contextStart {
		builder.lines = builder.lines[:contextStart-1]
	}
	return builder.String()
}

func memoryEmbeddingQueryContextLines(currentEvent domain.Event, additionalEvents []domain.Event, conversation []domain.ConversationMessage) []string {
	if len(conversation) == 0 {
		return nil
	}
	excludedEventIDs := map[string]bool{}
	excludedUserBodies := map[string]bool{}
	addExcludedMemoryQueryEvent(excludedEventIDs, excludedUserBodies, currentEvent)
	for _, event := range additionalEvents {
		addExcludedMemoryQueryEvent(excludedEventIDs, excludedUserBodies, event)
	}

	selected := make([]domain.ConversationMessage, 0, memoryEmbeddingQueryMaxMessages)
	for i := len(conversation) - 1; i >= 0 && len(selected) < memoryEmbeddingQueryMaxMessages; i-- {
		message := conversation[i]
		label := memoryEmbeddingQueryRoleLabel(message.Role)
		if label == "" {
			continue
		}
		body := cleanMemoryEmbeddingQueryText(message.Body)
		if body == "" {
			continue
		}
		if message.Role == domain.ConversationRoleUser &&
			(excludedEventIDs[strings.TrimSpace(message.EventID)] || excludedUserBodies[body]) {
			continue
		}
		selected = append(selected, message)
	}

	lines := make([]string, 0, len(selected))
	for _, message := range selected {
		label := memoryEmbeddingQueryRoleLabel(message.Role)
		body := cleanMemoryEmbeddingQueryText(message.Body)
		if label == "" || body == "" {
			continue
		}
		lines = append(lines, label+": "+body)
	}
	return lines
}

func addExcludedMemoryQueryEvent(ids map[string]bool, bodies map[string]bool, event domain.Event) {
	if id := strings.TrimSpace(event.ID); id != "" {
		ids[id] = true
	}
	if body := cleanMemoryEmbeddingQueryText(event.Body); body != "" {
		bodies[body] = true
	}
}

func memoryEmbeddingQueryRoleLabel(role domain.ConversationRole) string {
	switch role {
	case domain.ConversationRoleUser:
		return "user"
	case domain.ConversationRoleAssistant:
		return "assistant"
	default:
		return ""
	}
}

func cleanMemoryEmbeddingQueryText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

type memoryEmbeddingQueryBuilder struct {
	lines []string
	chars int
}

func (b *memoryEmbeddingQueryBuilder) append(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	remaining := memoryEmbeddingQueryMaxChars - b.chars
	if len(b.lines) > 0 {
		remaining--
	}
	if remaining <= 0 {
		return false
	}
	if memoryEmbeddingQueryTextLen(line) > remaining {
		line = truncateMemoryEmbeddingQueryText(line, remaining)
		if line == "" {
			return false
		}
	}
	if len(b.lines) > 0 {
		b.chars++
	}
	b.lines = append(b.lines, line)
	b.chars += memoryEmbeddingQueryTextLen(line)
	return true
}

func (b memoryEmbeddingQueryBuilder) String() string {
	return strings.TrimSpace(strings.Join(b.lines, "\n"))
}

func memoryEmbeddingQueryTextLen(value string) int {
	return len([]rune(value))
}

func truncateMemoryEmbeddingQueryText(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= max {
		return string(runes)
	}
	return strings.TrimSpace(string(runes[:max]))
}
