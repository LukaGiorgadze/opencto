package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

type OpenAIConversationCompressor struct {
	model llms.Model
}

func NewOpenAIConversationCompressor(apiKey, baseURL, modelID string) (*OpenAIConversationCompressor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
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
	response, err := c.model.GenerateContent(ctx, conversationCompressionMessages(input), llms.WithOptions(llms.CallOptions{
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

func conversationCompressionMessages(input agent.ConversationCompressionInput) []llms.MessageContent {
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, conversationCompressionSystemPrompt()),
		llms.TextParts(llms.ChatMessageTypeHuman, conversationCompressionUserPrompt(input)),
	}
}

func conversationCompressionSystemPrompt() string {
	return strings.TrimSpace(`
You compress older OpenCTO conversation history into bounded context for a future agent turn.

Return only JSON:
{"summary":"..."}

Write a concise chronological summary with durable conversation context:
- user goals, decisions, constraints, preferences, approvals, and corrections
- unresolved questions or pending work
- important tool outcomes, file names, commands, errors, and verification results
- facts needed to understand later messages in this scope

Do not include raw logs, full command output, long diffs, stack traces, or every turn.
Do not invent facts. If the source is ambiguous, phrase it as such.
Keep wording compact and neutral.
`)
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
	if maxChars <= 0 {
		maxChars = 12000
	}
	items := compactConversationHistoryItems(messages)
	var builder strings.Builder
	for _, item := range items {
		remaining := maxChars - builder.Len()
		if remaining <= 0 {
			break
		}
		entry := conversationHistoryItemEntry(item, remaining)
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
		output.Summary = truncateText(output.Summary, maxChars)
	}
	return output, nil
}
