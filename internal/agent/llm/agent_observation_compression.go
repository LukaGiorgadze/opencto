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
	"github.com/opencto/opencto/internal/textclean"
)

type OpenAIAgentObservationCompressor struct {
	model llms.Model
}

func NewOpenAIAgentObservationCompressor(apiKey, baseURL, modelID string) (*OpenAIAgentObservationCompressor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
		openai.WithResponseFormat(agentObservationCompressionResponseFormat()),
	)
	if err != nil {
		return nil, err
	}
	return &OpenAIAgentObservationCompressor{model: model}, nil
}

func (c *OpenAIAgentObservationCompressor) CompressAgentObservations(ctx context.Context, input agent.AgentObservationCompressionInput) (agent.AgentObservationCompressionOutput, error) {
	if c == nil || c.model == nil {
		return agent.AgentObservationCompressionOutput{}, fmt.Errorf("agent observation compressor model is not configured")
	}
	if len(input.Observations) == 0 && strings.TrimSpace(input.PreviousSummary) == "" {
		return agent.AgentObservationCompressionOutput{}, nil
	}
	maxSummaryChars := input.MaxSummaryChars
	if maxSummaryChars <= 0 {
		maxSummaryChars = 6000
	}
	maxTokens := maxSummaryChars / 3
	if maxTokens < 400 {
		maxTokens = 400
	}
	messages, err := agentObservationCompressionMessages(input)
	if err != nil {
		return agent.AgentObservationCompressionOutput{}, err
	}
	response, err := c.model.GenerateContent(ctx, messages, llms.WithOptions(llms.CallOptions{
		JSONMode:         true,
		ResponseMIMEType: "application/json",
		Temperature:      0,
		MaxTokens:        maxTokens,
	}))
	if err != nil {
		return agent.AgentObservationCompressionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.AgentObservationCompressionOutput{}, fmt.Errorf("agent observation compressor returned no choices")
	}
	return parseAgentObservationCompressionOutput(response.Choices[0].Content, maxSummaryChars)
}

func agentObservationCompressionMessages(input agent.AgentObservationCompressionInput) ([]llms.MessageContent, error) {
	systemPrompt, err := prompts.Load("agent_observation_compression.tmpl")
	if err != nil {
		return nil, err
	}
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, agentObservationCompressionUserPrompt(input)),
	}, nil
}

func agentObservationCompressionUserPrompt(input agent.AgentObservationCompressionInput) string {
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
	return prompts.MustRender("agent_observation_compression_user.tmpl", map[string]any{
		"ProjectID":       strings.TrimSpace(input.ProjectID),
		"Goal":            strings.TrimSpace(input.Goal),
		"PreviousSummary": strings.TrimSpace(input.PreviousSummary),
		"MaxSummaryChars": maxSummaryChars,
		"Source":          agentObservationCompressionSource(input.Observations, sourceBudget),
	})
}

func agentObservationCompressionSource(observations []agent.ExecutionFeedback, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 12000
	}
	var builder strings.Builder
	for _, observation := range observations {
		remaining := maxChars - builder.Len()
		if remaining <= 0 {
			break
		}
		entry := agentObservationCompressionEntry(observation, remaining)
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

func agentObservationCompressionEntry(observation agent.ExecutionFeedback, budget int) string {
	labelParts := []string{
		"turn",
		fmt.Sprintf("%d", observation.Cycle),
		string(observation.Tool),
		strings.TrimSpace(observation.Status),
	}
	label := strings.Join(nonEmptyStrings(labelParts...), " ")
	body := agentObservationCompressionBody(observation)
	if strings.TrimSpace(label) == "" || strings.TrimSpace(body) == "" {
		return ""
	}
	bodyBudget := budget - len(label) - 4
	if bodyBudget <= 0 {
		return ""
	}
	if bodyBudget > 900 {
		bodyBudget = 900
	}
	body = truncateTextPlain(body, bodyBudget)
	return prompts.MustRender("conversation_compression_item_entry.tmpl", map[string]any{
		"Label": label,
		"Body":  body,
	})
}

func agentObservationCompressionBody(observation agent.ExecutionFeedback) string {
	var lines []string
	if requested := strings.TrimSpace(observation.RequestedAction); requested != "" {
		lines = append(lines, "requested_action: "+requested)
	}
	if command := strings.TrimSpace(observation.Command); command != "" {
		args := strings.Join(observation.Args, " ")
		if args != "" {
			command += " " + args
		}
		lines = append(lines, "command: "+command)
	}
	if workingDir := strings.TrimSpace(observation.Metadata["working_directory"]); workingDir != "" {
		lines = append(lines, "working_directory: "+workingDir)
	}
	if resultCode := strings.TrimSpace(observation.Metadata["result_code"]); resultCode != "" {
		lines = append(lines, "result_code: "+resultCode)
	}
	if output := strings.TrimSpace(textclean.TerminalOutput(observation.Observation)); output != "" {
		lines = append(lines, "observation: "+output)
	}
	if errText := strings.TrimSpace(textclean.TerminalOutput(observation.Error)); errText != "" {
		lines = append(lines, "error: "+errText)
	}
	return strings.Join(lines, "\n")
}

func parseAgentObservationCompressionOutput(content string, maxChars int) (agent.AgentObservationCompressionOutput, error) {
	content = stripJSONFence(content)
	if strings.TrimSpace(content) == "" {
		return agent.AgentObservationCompressionOutput{}, nil
	}
	var output agent.AgentObservationCompressionOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return agent.AgentObservationCompressionOutput{}, fmt.Errorf("decode agent observation compression output: %w", err)
	}
	output.Summary = strings.TrimSpace(output.Summary)
	if maxChars > 0 && len(output.Summary) > maxChars {
		output.Summary = truncateTextPlain(output.Summary, maxChars)
	}
	return output, nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
