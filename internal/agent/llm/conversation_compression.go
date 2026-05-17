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
	toolregistry "github.com/opencto/opencto/internal/tools"
	exectool "github.com/opencto/opencto/internal/tools/exec"
	readtool "github.com/opencto/opencto/internal/tools/read"
)

type OpenAIConversationCompressor struct {
	model llms.Model
}

func NewOpenAIConversationCompressor(apiKey, baseURL, modelID string, bifrostEnabled bool) (*OpenAIConversationCompressor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
		openai.WithResponseFormat(conversationCompressionResponseFormat()),
		openai.WithHTTPClient(newOpenAIHTTPClient(bifrostEnabled)),
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
	return prompts.MustRender("conversation_compression_user.tmpl", map[string]any{
		"ProjectID":       strings.TrimSpace(input.ProjectID),
		"Scope":           string(input.Scope),
		"MaxSummaryChars": maxSummaryChars,
		"Source":          conversationSummarySource(input.Messages, sourceBudget),
	})
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
	return prompts.MustRender("conversation_compression_item_entry.tmpl", map[string]any{
		"Label": label,
		"Body":  body,
	})
}

func conversationCompressionToolBody(message domain.ConversationMessage) string {
	body := strings.TrimSpace(message.Body)
	tool := strings.TrimSpace(message.Metadata["tool"])
	var parts []string
	if requested := conversationBodyValue(body, "requested_action"); requested != "" {
		appendCompressionValue(&parts, toolregistry.PromptCompressionRequestedAction, requested)
	}
	switch tool {
	case string(domain.ToolTypeRead):
		appendCompressionValue(&parts, readtool.PromptCompressionFile, firstNonEmpty(message.Metadata["file_path"], conversationBodyValue(body, "file")))
		appendCompressionValue(&parts, readtool.PromptCompressionLines, firstNonEmpty(readLinesMetadata(message.Metadata), conversationBodyValue(body, "lines")))
		appendCompressionValue(&parts, readtool.PromptCompressionBytes, firstNonEmpty(message.Metadata["bytes_read"], conversationBodyValue(body, "bytes")))
		appendCompressionValue(&parts, readtool.PromptCompressionTruncated, firstNonEmpty(message.Metadata["truncated"], conversationBodyValue(body, "truncated")))
		parts = append(parts, readtool.PromptCompressionContentOmitted())
	case string(domain.ToolTypeExec):
		appendCompressionValue(&parts, exectool.PromptCompressionResultCode, message.Metadata["result_code"])
		appendCompressionValue(&parts, exectool.PromptCompressionStdoutLogPath, firstNonEmpty(message.Metadata["stdout_log_path"], conversationBodyValue(body, "stdout_log_path")))
		appendCompressionValue(&parts, exectool.PromptCompressionStderrLogPath, firstNonEmpty(message.Metadata["stderr_log_path"], conversationBodyValue(body, "stderr_log_path")))
		appendCompressionValue(&parts, exectool.PromptCompressionOutputTruncated, firstNonEmpty(message.Metadata["stdout_truncated"], message.Metadata["stderr_truncated"], conversationBodyValue(body, "output_truncated")))
	default:
		if observed := conversationBodyValue(body, "observation"); observed != "" {
			appendCompressionValue(&parts, toolregistry.PromptCompressionObservation, truncateTextPlain(observed, 500))
		}
	}
	if len(parts) == 0 {
		return truncateTextPlain(body, 700)
	}
	return strings.Join(parts, "\n")
}

func appendCompressionValue(parts *[]string, render func(string) string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	*parts = append(*parts, render(value))
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
