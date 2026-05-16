package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/media"
	"github.com/opencto/opencto/internal/skills"
	"github.com/opencto/opencto/internal/textclean"
	toolregistry "github.com/opencto/opencto/internal/tools"
	edittool "github.com/opencto/opencto/internal/tools/edit"
)

type nextActionPromptData struct {
	ProjectName        string
	ProjectID          string
	ProjectDescription string
	OS                 string
	Arch               string
	Exec               string
	Path               string
	WorkspaceRoot      string
	OpenCTORoot        string
	CurrentLocalTime   string
	CurrentUTCTime     string
	HostTimeZone       string
	HostTimeZoneError  string
	ChannelType        domain.ChannelType
	SubAgentGoal       string
	AllowedTools       []string
}

type memoryContextPromptData struct {
	Memories []memoryContextPromptItem
}

type memoryContextPromptItem struct {
	ID      string
	Scope   string
	Kind    string
	Content string
}

type toolResultEnvelope struct {
	IsError bool   `json:"is_error"`
	Content string `json:"content"`
}

var promptTruncationSuffix = prompts.MustRender("truncation_suffix.tmpl", nil)

func (e *OpenAIEngine) NextAction(ctx context.Context, input agent.NextActionInput) (agent.NextActionOutput, error) {
	if e.reasoningModel == nil {
		return agent.NextActionOutput{}, fmt.Errorf("next action model is not configured")
	}

	input, err := e.enrichInputWithAttachmentTranscripts(ctx, input)
	if err != nil {
		return agent.NextActionOutput{}, err
	}

	messages, err := buildNextActionMessagesWithContext(ctx, input, e.imageResolver)
	if err != nil {
		return agent.NextActionOutput{}, err
	}

	options := []llms.CallOption{}
	tools := toolregistry.LLMDefinitions()
	if input.RestrictTools {
		tools = toolregistry.LLMDefinitionsForTypes(input.ToolAllowlist)
	}
	if len(tools) > 0 {
		options = append(options, llms.WithTools(tools))
	}
	response, err := e.reasoningModel.GenerateContent(ctx, messages, options...)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("model returned no choices")
	}

	choice := response.Choices[0]
	if len(choice.ToolCalls) > 0 {
		return nextActionToolOutput(choice, input)
	}
	return nextActionTerminalFromContent(choice.Content, input)
}

func buildNextActionMessages(input agent.NextActionInput) ([]llms.MessageContent, error) {
	return buildNextActionMessagesWithContext(context.Background(), input, nil)
}

func buildNextActionMessagesWithContext(ctx context.Context, input agent.NextActionInput, imageResolver *media.ImageResolver) ([]llms.MessageContent, error) {
	prompt, err := renderNextActionPrompt(input)
	if err != nil {
		return nil, err
	}

	userMessage, err := openAIUserMessageFromEvent(ctx, input.Context.Event, imageResolver)
	if err != nil {
		return nil, err
	}

	messages := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, prompt),
	}
	if reminder := skills.Reminder(input.Context.Skills); reminder != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, reminder))
	}
	if memory := memoryContextMessage(input.Context.Memory); memory != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, memory))
	}
	if summary := subAgentRunSummaryMessage(input.SubAgent); summary != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, summary))
	}
	summaryBudget, historyBudget := conversationContextBudgets(input.Context.ConversationMaxContextChars, len(input.Context.ConversationSummaries) > 0)
	if summaries := conversationSummaryContextMessage(input.Context.ConversationSummaries, summaryBudget); summaries != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, summaries))
	}
	conversation := conversationWithoutCurrentEvents(input.Context.Conversation, input.Context.Event, input.Context.AdditionalEvents)
	if history := conversationContextMessage(conversation, historyBudget); history != "" {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, history))
	}
	messages = append(messages, userMessage)
	for index := 0; index < len(input.ObservationHistory); {
		toolCallIDs := strings.TrimSpace(input.ObservationHistory[index].Metadata["tool_call_ids"])
		if strings.Contains(toolCallIDs, ",") {
			end := index + 1
			for end < len(input.ObservationHistory) &&
				input.ObservationHistory[end].Cycle == input.ObservationHistory[index].Cycle &&
				strings.TrimSpace(input.ObservationHistory[end].Metadata["tool_call_ids"]) == toolCallIDs {
				end++
			}
			if end-index > 1 {
				transcript, err := nextActionTranscriptMessages(input.ObservationHistory[index:end]...)
				if err != nil {
					return nil, err
				}
				messages = append(messages, transcript...)
				index = end
				continue
			}
		}
		transcript, err := nextActionTranscriptMessages(input.ObservationHistory[index])
		if err != nil {
			return nil, err
		}
		messages = append(messages, transcript...)
		index++
	}
	additionalMessages, err := additionalUserMessages(ctx, input.Context.AdditionalEvents, imageResolver)
	if err != nil {
		return nil, err
	}
	messages = append(messages, additionalMessages...)
	if input.ForceFinal {
		messages = append(messages, llms.TextParts(llms.ChatMessageTypeHuman, prompts.MustRender("force_final.tmpl", nil)))
	}
	if err := validateToolTranscriptMessages(messages); err != nil {
		return nil, err
	}
	return messages, nil
}

func renderNextActionPrompt(input agent.NextActionInput) (string, error) {
	projectName := strings.TrimSpace(input.Context.Project.Name)
	if projectName == "" {
		projectName = input.ProjectID
	}

	data := nextActionPromptData{
		ProjectName:        projectName,
		ProjectID:          input.ProjectID,
		ProjectDescription: strings.TrimSpace(input.Context.Project.Description),
		OS:                 input.Runtime.OS,
		Arch:               input.Runtime.Arch,
		Exec:               firstNonEmpty(strings.TrimSpace(input.Runtime.Exec), "unknown"),
		Path:               strings.TrimSpace(input.Runtime.Path),
		WorkspaceRoot:      firstNonEmpty(strings.TrimSpace(input.Runtime.WorkspaceRoot), "."),
		OpenCTORoot:        firstNonEmpty(strings.TrimSpace(input.Runtime.OpenCTORoot), "."),
		CurrentLocalTime:   strings.TrimSpace(input.Runtime.CurrentLocalTime),
		CurrentUTCTime:     strings.TrimSpace(input.Runtime.CurrentUTCTime),
		HostTimeZone:       strings.TrimSpace(input.Runtime.HostTimeZone),
		HostTimeZoneError:  strings.TrimSpace(input.Runtime.HostTimeZoneError),
		ChannelType:        input.ChannelType,
	}

	if input.SubAgent != nil {
		data.SubAgentGoal = strings.TrimSpace(input.SubAgent.Goal)
		data.AllowedTools = toolTypeNames(input.ToolAllowlist)
		return prompts.Render("sub_agent.tmpl", data)
	}

	return prompts.Render("next_action.tmpl", data)
}

func subAgentRunSummaryMessage(context *agent.SubAgentContext) string {
	if context == nil {
		return ""
	}
	summary := strings.TrimSpace(context.RunSummary)
	if summary == "" {
		return ""
	}
	return prompts.MustRender("sub_agent_run_summary.tmpl", map[string]any{
		"Summary": summary,
	})
}

func toolTypeNames(values []domain.ToolType) []string {
	names := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		name := strings.TrimSpace(string(value))
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func additionalUserMessages(ctx context.Context, events []domain.Event, imageResolver *media.ImageResolver) ([]llms.MessageContent, error) {
	messages := make([]llms.MessageContent, 0, len(events))
	for _, event := range events {
		message, err := openAIUserMessageFromEvent(ctx, event, imageResolver)
		if err != nil {
			return nil, err
		}
		if messageHasContent(message) {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

func memoryContextMessage(memories []domain.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	items := make([]memoryContextPromptItem, 0, len(memories))
	for _, memory := range memories {
		content := strings.TrimSpace(memory.Content)
		if content == "" {
			continue
		}
		items = append(items, memoryContextPromptItem{
			ID:      strings.TrimSpace(memory.ID),
			Scope:   string(memory.Scope),
			Kind:    strings.TrimSpace(memory.Kind),
			Content: content,
		})
	}
	if len(items) == 0 {
		return ""
	}
	return strings.TrimSpace(prompts.MustRender("memory_context.tmpl", memoryContextPromptData{Memories: items}))
}

func conversationWithoutCurrentEvents(messages []domain.ConversationMessage, current domain.Event, additional []domain.Event) []domain.ConversationMessage {
	if len(messages) == 0 {
		return nil
	}
	excluded := map[string]bool{}
	if id := strings.TrimSpace(current.ID); id != "" {
		excluded[id] = true
	}
	for _, event := range additional {
		if id := strings.TrimSpace(event.ID); id != "" {
			excluded[id] = true
		}
	}
	if len(excluded) == 0 {
		return append([]domain.ConversationMessage(nil), messages...)
	}
	filtered := make([]domain.ConversationMessage, 0, len(messages))
	for _, message := range messages {
		if conversationMessageDuplicatesCurrentEvent(message, excluded) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func conversationMessageDuplicatesCurrentEvent(message domain.ConversationMessage, excluded map[string]bool) bool {
	if !excluded[strings.TrimSpace(message.EventID)] {
		return false
	}
	switch message.Role {
	case domain.ConversationRoleAssistant:
		return false
	default:
		return true
	}
}

func conversationContextMessage(messages []domain.ConversationMessage, maxChars int) string {
	messages = dedupeAdjacentAssistantConversationMessages(messages)
	if len(messages) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 8000
	}
	header := prompts.MustRender("conversation_history_header.tmpl", nil)
	if maxChars <= len(header) {
		return truncateText(header, maxChars)
	}
	items := compactConversationHistoryItems(messages)
	remaining := maxChars - len(header) - 1
	selected := map[int]string{}
	if rootIndex := conversationHistoryThreadRootIndex(items); rootIndex >= 0 {
		entry := conversationHistoryItemEntry(items[rootIndex], conversationPinnedRootBudget(remaining))
		if strings.TrimSpace(entry) != "" {
			selected[rootIndex] = entry
			remaining -= len(entry) + 1
		}
	}
	for i := len(items) - 1; i >= 0 && remaining > 0; i-- {
		if _, ok := selected[i]; ok {
			continue
		}
		entry := conversationHistoryItemEntry(items[i], remaining)
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if len(entry) > remaining {
			entry = truncateText(entry, remaining)
		}
		selected[i] = entry
		remaining -= len(entry) + 1
	}
	if len(selected) == 0 {
		return header
	}
	var builder strings.Builder
	builder.WriteString(header)
	for i := range items {
		if entry, ok := selected[i]; ok {
			builder.WriteString("\n")
			builder.WriteString(entry)
		}
	}
	return strings.TrimSpace(builder.String())
}

func conversationPinnedRootBudget(remaining int) int {
	if remaining > 1200 {
		return 1200
	}
	return remaining
}

func dedupeAdjacentAssistantConversationMessages(messages []domain.ConversationMessage) []domain.ConversationMessage {
	if len(messages) < 2 {
		return messages
	}
	deduped := make([]domain.ConversationMessage, 0, len(messages))
	for _, message := range messages {
		if isDuplicateAdjacentAssistantMessage(deduped, message) {
			continue
		}
		deduped = append(deduped, message)
	}
	return deduped
}

func isDuplicateAdjacentAssistantMessage(messages []domain.ConversationMessage, message domain.ConversationMessage) bool {
	if len(messages) == 0 || message.Role != domain.ConversationRoleAssistant {
		return false
	}
	previous := messages[len(messages)-1]
	if previous.Role != domain.ConversationRoleAssistant {
		return false
	}
	return normalizedConversationBody(previous.Body) == normalizedConversationBody(message.Body)
}

func normalizedConversationBody(body string) string {
	return strings.Join(strings.Fields(textclean.TerminalOutput(body)), " ")
}

func conversationContextBudgets(maxChars int, hasSummaries bool) (int, int) {
	if maxChars <= 0 {
		maxChars = 8000
	}
	if !hasSummaries {
		return 0, maxChars
	}
	summaryBudget := maxChars * 7 / 10
	if summaryBudget < 1000 {
		summaryBudget = maxChars / 2
	}
	historyBudget := maxChars - summaryBudget
	if historyBudget < 1000 {
		historyBudget = maxChars / 2
	}
	return summaryBudget, historyBudget
}

func conversationSummaryContextMessage(summaries []domain.ConversationSummary, maxChars int) string {
	if len(summaries) == 0 {
		return ""
	}
	if maxChars <= 0 {
		maxChars = 6000
	}
	header := prompts.MustRender("conversation_summary_header.tmpl", nil)
	if maxChars <= len(header) {
		return truncateText(header, maxChars)
	}
	remaining := maxChars - len(header) - 1
	summaries = conversationSummariesByPriority(summaries)
	selected := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		if remaining <= 0 {
			break
		}
		entryBudget := remaining
		entry := conversationSummaryEntry(summary, entryBudget)
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if len(entry) > remaining {
			entry = truncateText(entry, remaining)
		}
		selected = append(selected, entry)
		remaining -= len(entry) + 1
	}
	if len(selected) == 0 {
		return header
	}
	var builder strings.Builder
	builder.WriteString(header)
	for _, entry := range selected {
		builder.WriteString("\n")
		builder.WriteString(entry)
	}
	return strings.TrimSpace(builder.String())
}

func conversationSummariesByPriority(summaries []domain.ConversationSummary) []domain.ConversationSummary {
	ordered := append([]domain.ConversationSummary(nil), summaries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left := ordered[i]
		right := ordered[j]
		leftPriority := conversationSummaryScopePriority(left.Scope)
		rightPriority := conversationSummaryScopePriority(right.Scope)
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if !left.ToCreatedAt.Equal(right.ToCreatedAt) {
			return left.ToCreatedAt.After(right.ToCreatedAt)
		}
		return left.ToMessageID > right.ToMessageID
	})
	return ordered
}

func conversationSummaryScopePriority(scope domain.ConversationSummaryScope) int {
	switch scope {
	case domain.ConversationSummaryScopeThread:
		return 0
	case domain.ConversationSummaryScopeChannel:
		return 1
	case domain.ConversationSummaryScopeProject:
		return 2
	default:
		return 3
	}
}

func conversationSummaryEntry(summary domain.ConversationSummary, budget int) string {
	body := strings.TrimSpace(summary.Summary)
	if body == "" {
		return ""
	}
	label := prompts.MustRender("conversation_summary_label.tmpl", map[string]any{
		"Scope": string(summary.Scope),
	})
	bodyBudget := budget - len(label) - 4
	if bodyBudget <= 0 {
		return ""
	}
	return prompts.MustRender("conversation_context_entry.tmpl", map[string]any{
		"Label": label,
		"Body":  truncateText(body, bodyBudget),
	})
}

type conversationHistoryItem struct {
	Role     domain.ConversationRole
	Label    string
	Body     string
	ThreadID string
}

func compactConversationHistoryItems(messages []domain.ConversationMessage) []conversationHistoryItem {
	items := make([]conversationHistoryItem, 0, len(messages))
	for index := 0; index < len(messages); {
		if item, count, ok := compactEditToolHistory(messages[index:]); ok {
			items = append(items, item)
			index += count
			continue
		}
		if item, count, ok := compactExactToolHistory(messages[index:]); ok {
			items = append(items, item)
			index += count
			continue
		}
		item := conversationHistoryMessageItem(messages[index])
		if strings.TrimSpace(item.Body) != "" {
			items = append(items, item)
		}
		index++
	}
	return items
}

func conversationHistoryMessageItem(message domain.ConversationMessage) conversationHistoryItem {
	body := strings.TrimSpace(textclean.TerminalOutput(message.Body))
	if body == "" {
		return conversationHistoryItem{}
	}
	return conversationHistoryItem{
		Role:     message.Role,
		Label:    conversationHistoryLabel(message),
		Body:     body,
		ThreadID: strings.TrimSpace(message.ThreadID),
	}
}

func conversationHistoryThreadRootIndex(items []conversationHistoryItem) int {
	firstThread := -1
	for i, item := range items {
		if strings.TrimSpace(item.ThreadID) != "" {
			firstThread = i
			break
		}
	}
	if firstThread <= 0 {
		return -1
	}
	for i := firstThread - 1; i >= 0; i-- {
		if strings.TrimSpace(items[i].ThreadID) == "" {
			return i
		}
	}
	return -1
}

func conversationHistoryEntry(message domain.ConversationMessage, budget int) string {
	return conversationHistoryItemEntry(conversationHistoryMessageItem(message), budget)
}

func conversationHistoryItemEntry(item conversationHistoryItem, budget int) string {
	body := strings.TrimSpace(textclean.TerminalOutput(item.Body))
	label := strings.TrimSpace(item.Label)
	if body == "" || label == "" {
		return ""
	}
	bodyBudget := budget - len(label) - 4
	if bodyBudget <= 0 {
		return ""
	}
	if item.Role == domain.ConversationRoleTool && bodyBudget > 1200 {
		bodyBudget = 1200
	}
	body = truncateText(body, bodyBudget)
	return prompts.MustRender("conversation_context_entry.tmpl", map[string]any{
		"Label": label,
		"Body":  body,
	})
}

func conversationHistoryLabel(message domain.ConversationMessage) string {
	var parts []string
	if message.Role == domain.ConversationRoleTool {
		tool := strings.TrimSpace(message.Metadata["tool"])
		status := strings.TrimSpace(message.Metadata["status"])
		if tool != "" {
			parts = append(parts, tool)
		}
		if status != "" {
			parts = append(parts, status)
		}
	}
	return prompts.MustRender("conversation_history_label.tmpl", map[string]any{
		"Role":  string(message.Role),
		"Parts": parts,
	})
}

type compactEditToolInfo struct {
	Label           string
	FilePath        string
	Replacements    int
	HasReplacements bool
	BytesWritten    int
	HasBytesWritten bool
}

func compactEditToolHistory(messages []domain.ConversationMessage) (conversationHistoryItem, int, bool) {
	if len(messages) == 0 {
		return conversationHistoryItem{}, 0, false
	}
	first, ok := compactEditToolInfoFromMessage(messages[0])
	if !ok {
		return conversationHistoryItem{}, 0, false
	}
	infos := []compactEditToolInfo{first}
	for index := 1; index < len(messages); index++ {
		next, ok := compactEditToolInfoFromMessage(messages[index])
		if !ok || next.Label != first.Label || next.FilePath != first.FilePath {
			break
		}
		infos = append(infos, next)
	}
	if len(infos) < 2 {
		return conversationHistoryItem{}, 0, false
	}
	return conversationHistoryItem{
		Role:     domain.ConversationRoleTool,
		Label:    countLabel(first.Label, len(infos)),
		Body:     compactEditToolBody(infos),
		ThreadID: strings.TrimSpace(messages[0].ThreadID),
	}, len(infos), true
}

func compactEditToolInfoFromMessage(message domain.ConversationMessage) (compactEditToolInfo, bool) {
	if !compactableSuccessfulToolMessage(message) || strings.TrimSpace(message.Metadata["tool"]) != string(domain.ToolTypeEdit) {
		return compactEditToolInfo{}, false
	}
	body := strings.TrimSpace(message.Body)
	filePath := firstNonEmpty(
		strings.TrimSpace(message.Metadata["file_path"]),
		conversationBodyValue(body, "edited"),
	)
	if filePath == "" {
		return compactEditToolInfo{}, false
	}
	info := compactEditToolInfo{
		Label:    conversationHistoryLabel(message),
		FilePath: filePath,
	}
	if value, ok := conversationHistoryInt(firstNonEmpty(message.Metadata["replacements"], conversationBodyValue(body, "replacements"))); ok {
		info.Replacements = value
		info.HasReplacements = true
	}
	if value, ok := conversationHistoryInt(firstNonEmpty(message.Metadata["bytes_written"], conversationBodyValue(body, "bytes_written"))); ok {
		info.BytesWritten = value
		info.HasBytesWritten = true
	}
	return info, true
}

func compactEditToolBody(infos []compactEditToolInfo) string {
	replacements := make([]int, 0, len(infos))
	bytesWritten := make([]int, 0, len(infos))
	for _, info := range infos {
		if info.HasReplacements {
			replacements = append(replacements, info.Replacements)
		}
		if info.HasBytesWritten {
			bytesWritten = append(bytesWritten, info.BytesWritten)
		}
	}
	return edittool.PromptCompactHistoryBody(infos[0].FilePath, compactIntValues(replacements), compactIntValues(bytesWritten))
}

func compactExactToolHistory(messages []domain.ConversationMessage) (conversationHistoryItem, int, bool) {
	if len(messages) == 0 || !compactableSuccessfulToolMessage(messages[0]) {
		return conversationHistoryItem{}, 0, false
	}
	first := conversationHistoryMessageItem(messages[0])
	count := 1
	for count < len(messages) && compactableSuccessfulToolMessage(messages[count]) {
		next := conversationHistoryMessageItem(messages[count])
		if next.Label != first.Label || strings.TrimSpace(next.Body) != strings.TrimSpace(first.Body) {
			break
		}
		count++
	}
	if count < 2 {
		return conversationHistoryItem{}, 0, false
	}
	first.Label = countLabel(first.Label, count)
	return first, count, true
}

func countLabel(label string, count int) string {
	return prompts.MustRender("conversation_count_label.tmpl", map[string]any{
		"Label": label,
		"Count": count,
	})
}

func compactableSuccessfulToolMessage(message domain.ConversationMessage) bool {
	if message.Role != domain.ConversationRoleTool {
		return false
	}
	if strings.TrimSpace(message.Metadata["status"]) != string(domain.ExecutionStatusSucceeded) {
		return false
	}
	if code := strings.TrimSpace(message.Metadata["result_code"]); code != "" && code != "0" {
		return false
	}
	body := strings.ToLower(strings.TrimSpace(message.Body))
	return !strings.Contains("\n"+body, "\nerror:")
}

func conversationBodyValue(body string, key string) string {
	prefix := strings.TrimSpace(key) + ":"
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func conversationHistoryInt(value string) (int, bool) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func compactIntValues(values []int) string {
	if len(values) == 0 {
		return ""
	}
	minValue := values[0]
	maxValue := values[0]
	for _, value := range values[1:] {
		if value < minValue {
			minValue = value
		}
		if value > maxValue {
			maxValue = value
		}
	}
	if minValue == maxValue {
		return strconv.Itoa(minValue)
	}
	return strconv.Itoa(minValue) + "-" + strconv.Itoa(maxValue)
}

func truncateText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	suffix := promptTruncationSuffix
	if limit <= len(suffix) {
		runes := []rune(text)
		if len(runes) <= limit {
			return text
		}
		return string(runes[:limit])
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	prefixLimit := limit - len(suffix)
	if prefixLimit > len(runes) {
		prefixLimit = len(runes)
	}
	return strings.TrimSpace(string(runes[:prefixLimit])) + suffix
}

func truncateTextPlain(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || text == "" {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	if limit > len(runes) {
		limit = len(runes)
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func messageHasContent(message llms.MessageContent) bool {
	for _, part := range message.Parts {
		switch value := part.(type) {
		case llms.TextContent:
			if strings.TrimSpace(value.Text) != "" {
				return true
			}
		default:
			return true
		}
	}
	return false
}

func nextActionTranscriptMessages(feedbacks ...agent.ExecutionFeedback) ([]llms.MessageContent, error) {
	if len(feedbacks) == 0 {
		return nil, nil
	}

	parts := make([]llms.ContentPart, 0, len(feedbacks))
	results := make([]llms.MessageContent, 0, len(feedbacks))
	for _, feedback := range feedbacks {
		toolCallID := executionFeedbackToolCallID(feedback)
		if toolCallID == "" {
			return nil, fmt.Errorf("%w: execution feedback is missing tool_call_id", agent.ErrInvalidNextAction)
		}
		toolName, arguments, err := transcriptToolCall(feedback)
		if err != nil {
			return nil, err
		}
		parts = append(parts, llms.ToolCall{
			ID:   toolCallID,
			Type: "function",
			FunctionCall: &llms.FunctionCall{
				Name:      toolName,
				Arguments: arguments,
			},
		})
		results = append(results, llms.MessageContent{
			Role: llms.ChatMessageTypeTool,
			Parts: []llms.ContentPart{llms.ToolCallResponse{
				ToolCallID: toolCallID,
				Name:       toolName,
				Content:    formatToolResultContent(feedback),
			}},
		})
	}

	assistant := llms.MessageContent{
		Role:  llms.ChatMessageTypeAI,
		Parts: parts,
	}
	return append([]llms.MessageContent{assistant}, results...), nil
}

func validateToolTranscriptMessages(messages []llms.MessageContent) error {
	pendingToolCalls := map[string]bool{}
	for index, message := range messages {
		switch message.Role {
		case llms.ChatMessageTypeAI:
			if len(pendingToolCalls) > 0 {
				return fmt.Errorf("%w: assistant tool calls missing results before message %d: %s", agent.ErrInvalidNextAction, index, pendingToolCallList(pendingToolCalls))
			}
			toolCalls, err := messageToolCallIDs(message, index)
			if err != nil {
				return err
			}
			pendingToolCalls = toolCalls
		case llms.ChatMessageTypeTool:
			toolCallID, ok := messageToolResponseID(message)
			if !ok {
				return fmt.Errorf("%w: tool message %d must contain exactly one tool response", agent.ErrInvalidNextAction, index)
			}
			if toolCallID == "" || !pendingToolCalls[toolCallID] {
				return fmt.Errorf("%w: tool response %q at message %d has no preceding assistant tool call", agent.ErrInvalidNextAction, toolCallID, index)
			}
			delete(pendingToolCalls, toolCallID)
		default:
			if len(pendingToolCalls) > 0 {
				return fmt.Errorf("%w: assistant tool calls missing results before message %d: %s", agent.ErrInvalidNextAction, index, pendingToolCallList(pendingToolCalls))
			}
			pendingToolCalls = map[string]bool{}
		}
	}
	if len(pendingToolCalls) > 0 {
		return fmt.Errorf("%w: assistant tool calls missing results at end of messages: %s", agent.ErrInvalidNextAction, pendingToolCallList(pendingToolCalls))
	}
	return nil
}

func messageToolCallIDs(message llms.MessageContent, messageIndex int) (map[string]bool, error) {
	ids := map[string]bool{}
	for _, part := range message.Parts {
		call, ok := part.(llms.ToolCall)
		if !ok {
			continue
		}
		id := strings.TrimSpace(call.ID)
		if id == "" {
			return nil, fmt.Errorf("%w: assistant tool call at message %d is missing id", agent.ErrInvalidNextAction, messageIndex)
		}
		if call.FunctionCall == nil || strings.TrimSpace(call.FunctionCall.Name) == "" {
			return nil, fmt.Errorf("%w: assistant tool call %q at message %d is missing function", agent.ErrInvalidNextAction, id, messageIndex)
		}
		ids[id] = true
	}
	return ids, nil
}

func messageToolResponseID(message llms.MessageContent) (string, bool) {
	if len(message.Parts) != 1 {
		return "", false
	}
	response, ok := message.Parts[0].(llms.ToolCallResponse)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(response.ToolCallID), true
}

func pendingToolCallList(ids map[string]bool) string {
	values := make([]string, 0, len(ids))
	for id := range ids {
		values = append(values, id)
	}
	return strings.Join(values, ",")
}

func nextActionToolOutput(choice *llms.ContentChoice, input agent.NextActionInput) (agent.NextActionOutput, error) {
	selectionInput := agent.ToolSelectionInput{
		ProjectID:      input.ProjectID,
		Context:        input.Context,
		Runtime:        input.Runtime,
		ExecutionCycle: input.ExecutionCycle,
		ToolAllowlist:  input.ToolAllowlist,
		RestrictTools:  input.RestrictTools,
	}
	toolChoices, err := toolChoicesFromToolCalls(choice.ToolCalls, selectionInput)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	toolCallIDs := make([]string, 0, len(toolChoices))
	for _, toolChoice := range toolChoices {
		toolCallIDs = append(toolCallIDs, strings.TrimSpace(toolChoice.ToolCallID))
	}
	assistantText := strings.TrimSpace(choice.Content)
	if assistantText == "" {
		assistantText = strings.TrimSpace(toolChoices[0].Intent)
	}
	for index := range toolChoices {
		if toolChoices[index].Metadata == nil {
			toolChoices[index].Metadata = map[string]string{}
		}
		toolChoices[index].Metadata["tool_call_id"] = toolChoices[index].ToolCallID
		toolChoices[index].Metadata["tool_call_ids"] = strings.Join(toolCallIDs, ",")
	}
	output := agent.NextActionOutput{
		NextAction:    input.NextAction,
		Status:        "tool",
		AssistantText: assistantText,
	}
	if len(toolChoices) == 1 {
		output.ToolChoice = &toolChoices[0]
	} else {
		output.ToolChoices = toolChoices
	}
	return output, nil
}

func nextActionTerminalFromContent(content string, input agent.NextActionInput) (agent.NextActionOutput, error) {
	answer, attachments, err := parseTerminalResponse(content)
	if err != nil {
		return agent.NextActionOutput{}, err
	}
	if answer == "" && len(attachments) == 0 {
		return agent.NextActionOutput{}, fmt.Errorf("%w: terminal response is empty", agent.ErrInvalidNextAction)
	}
	nextAction := input.NextAction
	nextAction.ResponseMessage = answer
	nextAction.ResponseAttachments = attachments
	return agent.NextActionOutput{
		NextAction: nextAction,
		Status:     "completed",
	}, nil
}

type terminalResponse struct {
	ResponseMessage     string                    `json:"response_message"`
	ResponseAttachments []domain.ReportAttachment `json:"response_attachments"`
}

func parseTerminalResponse(content string) (string, []domain.ReportAttachment, error) {
	answer := strings.TrimSpace(content)
	if answer == "" {
		return "", nil, nil
	}
	structured := stripJSONFence(answer)
	if !strings.HasPrefix(structured, "{") {
		return answer, nil, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(structured), &raw); err != nil {
		return answer, nil, nil
	}
	if _, ok := raw["response_message"]; !ok {
		if _, ok := raw["response_attachments"]; !ok {
			return answer, nil, nil
		}
	}

	var response terminalResponse
	if err := json.Unmarshal([]byte(structured), &response); err != nil {
		return "", nil, fmt.Errorf("%w: terminal response JSON is invalid: %w", agent.ErrInvalidNextAction, err)
	}
	message := strings.TrimSpace(response.ResponseMessage)
	attachments := make([]domain.ReportAttachment, 0, len(response.ResponseAttachments))
	for _, attachment := range response.ResponseAttachments {
		if strings.TrimSpace(attachment.Path) == "" {
			return "", nil, fmt.Errorf("%w: response attachment path is required", agent.ErrInvalidNextAction)
		}
		attachments = append(attachments, attachment)
	}
	return message, attachments, nil
}

func stripJSONFence(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") || !strings.HasSuffix(content, "```") {
		return content
	}
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "json")
	content = strings.TrimSpace(content)
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func executionFeedbackToolCallID(feedback agent.ExecutionFeedback) string {
	if strings.TrimSpace(feedback.ToolCallID) != "" {
		return strings.TrimSpace(feedback.ToolCallID)
	}
	return strings.TrimSpace(feedback.Metadata["tool_call_id"])
}

func transcriptCommand(feedback agent.ExecutionFeedback) string {
	if command := strings.TrimSpace(feedback.Metadata["original_command"]); command != "" {
		return command
	}
	return strings.TrimSpace(feedback.Command)
}

func transcriptTimeoutMs(feedback agent.ExecutionFeedback) int {
	value := strings.TrimSpace(feedback.Metadata["timeout_ms"])
	if value == "" {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0
	}
	return parsed
}

func transcriptRunMode(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["run_mode"])
	if value == "" {
		return string(domain.ToolRunModeWaitForExit)
	}
	return value
}

func transcriptIdempotency(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["idempotency"])
	if value == "" {
		return string(domain.ToolIdempotencyUnknown)
	}
	return value
}

func transcriptProcessScope(feedback agent.ExecutionFeedback) string {
	value := strings.TrimSpace(feedback.Metadata["process_scope"])
	if value == "" {
		return string(domain.ProcessScopeStopOnFinish)
	}
	return value
}

func transcriptToolCall(feedback agent.ExecutionFeedback) (string, string, error) {
	if feedback.Tool == "" || feedback.Tool == domain.ToolTypeExec {
		args := execToolInput{
			Command:      transcriptCommand(feedback),
			Args:         feedback.Args,
			TimeoutMs:    transcriptTimeoutMs(feedback),
			RunMode:      transcriptRunMode(feedback),
			Idempotency:  transcriptIdempotency(feedback),
			ProcessScope: transcriptProcessScope(feedback),
			Description:  strings.TrimSpace(feedback.RequestedAction),
			Destructive:  feedback.Metadata["destructive"] == "true",
		}
		encoded, err := json.Marshal(args)
		if err != nil {
			return "", "", err
		}
		return toolregistry.CommandToolName, string(encoded), nil
	}

	definition, ok := toolregistry.DefinitionByType(feedback.Tool)
	if !ok {
		return "", "", fmt.Errorf("%w: unsupported previous tool type %q", agent.ErrInvalidNextAction, feedback.Tool)
	}
	input := stripHiddenToolInputFields(feedback.Input)
	if input == "" {
		input = "{}"
	}
	return definition.Name, input, nil
}

func stripHiddenToolInputFields(raw json.RawMessage) string {
	input := strings.TrimSpace(string(raw))
	if input == "" {
		return ""
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return input
	}
	delete(object, "work_item_id")
	encoded, err := json.Marshal(object)
	if err != nil {
		return input
	}
	return string(encoded)
}

func formatToolResultContent(feedback agent.ExecutionFeedback) string {
	envelope := toolResultEnvelope{
		IsError: toolResultIsError(feedback),
		Content: formatToolResultDetails(feedback),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return envelope.Content
	}
	return string(encoded)
}

func formatToolResultDetails(feedback agent.ExecutionFeedback) string {
	lines := make([]string, 0, 3)
	if code := strings.TrimSpace(feedback.Metadata["result_code"]); code != "" {
		lines = append(lines, toolregistry.PromptResultExitCode(code))
	}
	if observation := strings.TrimSpace(feedback.Observation); observation != "" {
		lines = append(lines, toolregistry.PromptResultOutput(observation))
	}
	if errMsg := strings.TrimSpace(feedback.Error); errMsg != "" {
		lines = append(lines, toolregistry.PromptResultError(errMsg))
	}
	return strings.Join(lines, "\n")
}

func toolResultIsError(feedback agent.ExecutionFeedback) bool {
	if strings.TrimSpace(feedback.Error) != "" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(feedback.Status)) {
	case string(domain.ExecutionStatusFailed), string(domain.ExecutionStatusCanceled):
		return true
	}
	code := strings.TrimSpace(feedback.Metadata["result_code"])
	return code != "" && code != "0"
}
