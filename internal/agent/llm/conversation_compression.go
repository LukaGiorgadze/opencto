package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/textclean"
)

type OpenAIConversationCompressor struct {
	model llms.Model
}

func NewOpenAIConversationCompressor(apiKey, baseURL, modelID string) (*OpenAIConversationCompressor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
		openai.WithResponseFormat(conversationCompressionResponseFormat()),
	)
	if err != nil {
		return nil, err
	}
	return &OpenAIConversationCompressor{model: model}, nil
}

func (c *OpenAIConversationCompressor) CompressConversation(ctx context.Context, input agent.ConversationCompressionInput) (agent.ConversationCompressionOutput, error) {
	if c == nil || c.model == nil {
		return agent.ConversationCompressionOutput{}, fmt.Errorf("conversation compressor model is not configured")
	}
	if len(input.Messages) == 0 {
		return agent.ConversationCompressionOutput{}, nil
	}
	maxSummaryChars := input.MaxSummaryChars
	if maxSummaryChars <= 0 {
		maxSummaryChars = 6000
	}
	maxTokens := maxSummaryChars / 3
	if maxTokens < 400 {
		maxTokens = 400
	}
	messages, err := conversationCompressionMessages(input)
	if err != nil {
		return agent.ConversationCompressionOutput{}, err
	}
	response, err := c.model.GenerateContent(ctx, messages, llms.WithOptions(llms.CallOptions{
		JSONMode:         true,
		ResponseMIMEType: "application/json",
		Temperature:      0,
		MaxTokens:        maxTokens,
	}))
	if err != nil {
		return agent.ConversationCompressionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.ConversationCompressionOutput{}, fmt.Errorf("conversation compressor returned no choices")
	}
	return parseConversationCompressionOutput(response.Choices[0].Content, maxSummaryChars)
}

func conversationCompressionMessages(input agent.ConversationCompressionInput) ([]llms.MessageContent, error) {
	systemPrompt, err := prompts.Load("conversation_compression.tmpl")
	if err != nil {
		return nil, err
	}
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, conversationCompressionUserPrompt(input)),
	}, nil
}

func conversationCompressionUserPrompt(input agent.ConversationCompressionInput) string {
	maxSummaryChars := input.MaxSummaryChars
	if maxSummaryChars <= 0 {
		maxSummaryChars = 6000
	}
	sourceBudget := maxSummaryChars * 6
	if sourceBudget < 12000 {
		sourceBudget = 12000
	}
	if sourceBudget > 60000 {
		sourceBudget = 60000
	}
	var builder strings.Builder
	builder.WriteString("Project ID: ")
	builder.WriteString(strings.TrimSpace(input.ProjectID))
	builder.WriteString("\nScope: ")
	builder.WriteString(string(input.Scope))
	builder.WriteString("\nMax summary chars: ")
	builder.WriteString(fmt.Sprintf("%d", maxSummaryChars))
	builder.WriteString("\n\nConversation to summarize:\n")
	builder.WriteString(conversationSummarySource(input.Messages, sourceBudget))
	return builder.String()
}

func conversationSummarySource(messages []domain.ConversationMessage, maxChars int) string {
	messages = dedupeAdjacentAssistantConversationMessages(messages)
	if maxChars <= 0 {
		maxChars = 12000
	}
	var builder strings.Builder
	for index := 0; index < len(messages); {
		remaining := maxChars - builder.Len()
		if remaining <= 0 {
			break
		}
		var entry string
		if item, count, ok := compactEditToolHistory(messages[index:]); ok {
			entry = conversationCompressionItemEntry(item, remaining)
			index += count
		} else if item, count, ok := compactExactToolHistory(messages[index:]); ok {
			entry = conversationCompressionItemEntry(item, remaining)
			index += count
		} else {
			entry = conversationCompressionMessageEntry(messages[index], remaining)
			index++
		}
		if strings.TrimSpace(entry) == "" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteString("\n")
		}
		builder.WriteString(entry)
	}
	return strings.TrimSpace(builder.String())
}

func conversationCompressionMessageEntry(message domain.ConversationMessage, budget int) string {
	item := conversationHistoryMessageItem(message)
	if message.Role == domain.ConversationRoleTool {
		item.Body = conversationCompressionToolBody(message)
	}
	return conversationCompressionItemEntry(item, budget)
}

func conversationCompressionItemEntry(item conversationHistoryItem, budget int) string {
	body := strings.TrimSpace(textclean.TerminalOutput(item.Body))
	label := strings.TrimSpace(item.Label)
	if body == "" || label == "" {
		return ""
	}
	bodyBudget := budget - len(label) - 4
	if bodyBudget <= 0 {
		return ""
	}
	if item.Role == domain.ConversationRoleTool && bodyBudget > 700 {
		bodyBudget = 700
	}
	body = truncateTextPlain(body, bodyBudget)
	return "- " + label + ": " + body
}

func conversationCompressionToolBody(message domain.ConversationMessage) string {
	body := strings.TrimSpace(message.Body)
	tool := strings.TrimSpace(message.Metadata["tool"])
	var parts []string
	if requested := conversationBodyValue(body, "requested_action"); requested != "" {
		parts = append(parts, "requested_action: "+requested)
	}
	switch tool {
	case string(domain.ToolTypeRead):
		appendCompressionValue(&parts, "file", firstNonEmpty(message.Metadata["file_path"], conversationBodyValue(body, "file")))
		appendCompressionValue(&parts, "lines", firstNonEmpty(readLinesMetadata(message.Metadata), conversationBodyValue(body, "lines")))
		appendCompressionValue(&parts, "bytes", firstNonEmpty(message.Metadata["bytes_read"], conversationBodyValue(body, "bytes")))
		appendCompressionValue(&parts, "truncated", firstNonEmpty(message.Metadata["truncated"], conversationBodyValue(body, "truncated")))
		parts = append(parts, "content: omitted from compression source")
	case string(domain.ToolTypeExec):
		appendCompressionValue(&parts, "result_code", message.Metadata["result_code"])
		appendCompressionValue(&parts, "stdout_log_path", firstNonEmpty(message.Metadata["stdout_log_path"], conversationBodyValue(body, "stdout_log_path")))
		appendCompressionValue(&parts, "stderr_log_path", firstNonEmpty(message.Metadata["stderr_log_path"], conversationBodyValue(body, "stderr_log_path")))
		appendCompressionValue(&parts, "output_truncated", firstNonEmpty(message.Metadata["stdout_truncated"], message.Metadata["stderr_truncated"], conversationBodyValue(body, "output_truncated")))
	default:
		if observed := conversationBodyValue(body, "observation"); observed != "" {
			parts = append(parts, "observation: "+truncateTextPlain(observed, 500))
		}
	}
	if len(parts) == 0 {
		return truncateTextPlain(body, 700)
	}
	return strings.Join(parts, "\n")
}

func appendCompressionValue(parts *[]string, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*parts = append(*parts, key+": "+value)
}

func readLinesMetadata(metadata domain.Metadata) string {
	linesRead := strings.TrimSpace(metadata["lines_read"])
	totalLines := strings.TrimSpace(metadata["total_lines"])
	if linesRead == "" || totalLines == "" {
		return ""
	}
	return linesRead + "/" + totalLines
}

func parseConversationCompressionOutput(content string, maxChars int) (agent.ConversationCompressionOutput, error) {
	content = stripJSONFence(content)
	if strings.TrimSpace(content) == "" {
		return agent.ConversationCompressionOutput{}, nil
	}
	var output agent.ConversationCompressionOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return agent.ConversationCompressionOutput{}, fmt.Errorf("decode conversation compression output: %w", err)
	}
	output.Summary = strings.TrimSpace(output.Summary)
	if maxChars > 0 && len(output.Summary) > maxChars {
		output.Summary = truncateTextPlain(output.Summary, maxChars)
	}
	return output, nil
}
