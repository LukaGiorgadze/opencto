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
)

type OpenAIMemoryExtractor struct {
	model llms.Model
}

func NewOpenAIMemoryExtractor(apiKey, baseURL, modelID string) (*OpenAIMemoryExtractor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
		openai.WithResponseFormat(memoryExtractionResponseFormat()),
	)
	if err != nil {
		return nil, err
	}
	return &OpenAIMemoryExtractor{model: model}, nil
}

func (e *OpenAIMemoryExtractor) ExtractMemories(ctx context.Context, input agent.MemoryExtractionInput) (agent.MemoryExtractionOutput, error) {
	if e == nil || e.model == nil {
		return agent.MemoryExtractionOutput{}, fmt.Errorf("memory extractor model is not configured")
	}
	body := strings.TrimSpace(input.Event.Body)
	if body == "" {
		return agent.MemoryExtractionOutput{}, nil
	}
	messages, err := memoryExtractionMessages(input)
	if err != nil {
		return agent.MemoryExtractionOutput{}, err
	}
	response, err := e.model.GenerateContent(ctx, messages, llms.WithOptions(llms.CallOptions{
		JSONMode:         true,
		ResponseMIMEType: "application/json",
		Temperature:      0,
		MaxTokens:        800,
	}))
	if err != nil {
		return agent.MemoryExtractionOutput{}, err
	}
	if response == nil || len(response.Choices) == 0 {
		return agent.MemoryExtractionOutput{}, fmt.Errorf("memory extractor returned no choices")
	}
	return parseMemoryExtractionOutput(response.Choices[0].Content)
}

func memoryExtractionMessages(input agent.MemoryExtractionInput) ([]llms.MessageContent, error) {
	systemPrompt, err := prompts.Load("memory_extraction.tmpl")
	if err != nil {
		return nil, err
	}
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, memoryExtractionUserPrompt(input)),
	}, nil
}

func memoryExtractionUserPrompt(input agent.MemoryExtractionInput) string {
	var builder strings.Builder
	builder.WriteString("Project ID: ")
	builder.WriteString(strings.TrimSpace(input.ProjectID))
	builder.WriteString("\nChannel: ")
	builder.WriteString(string(input.Event.ChannelType))
	if threadID := strings.TrimSpace(input.Event.ThreadID); threadID != "" {
		builder.WriteString("\nThread ID: ")
		builder.WriteString(threadID)
	}
	builder.WriteString("\n\nUser message:\n")
	builder.WriteString(strings.TrimSpace(input.Event.Body))
	if len(input.ExistingMemories) > 0 {
		builder.WriteString("\n\nExisting relevant memories:")
		for _, memory := range input.ExistingMemories {
			content := strings.TrimSpace(memory.Content)
			if content == "" {
				continue
			}
			builder.WriteString("\n- id: ")
			builder.WriteString(strings.TrimSpace(memory.ID))
			builder.WriteString("; scope: ")
			builder.WriteString(string(memory.Scope))
			builder.WriteString("; kind: ")
			builder.WriteString(strings.TrimSpace(memory.Kind))
			builder.WriteString("; content: ")
			builder.WriteString(content)
		}
	}
	return builder.String()
}

func parseMemoryExtractionOutput(content string) (agent.MemoryExtractionOutput, error) {
	content = stripJSONFence(content)
	if strings.TrimSpace(content) == "" {
		return agent.MemoryExtractionOutput{}, nil
	}
	var output agent.MemoryExtractionOutput
	if err := json.Unmarshal([]byte(content), &output); err != nil {
		return agent.MemoryExtractionOutput{}, fmt.Errorf("decode memory extraction output: %w", err)
	}
	if len(output.Candidates) > 3 {
		output.Candidates = output.Candidates[:3]
	}
	candidates := make([]agent.MemoryCandidate, 0, len(output.Candidates))
	for _, candidate := range output.Candidates {
		candidate.Content = strings.TrimSpace(candidate.Content)
		if candidate.Content == "" {
			continue
		}
		candidate.Kind = strings.TrimSpace(candidate.Kind)
		candidate.Reason = strings.TrimSpace(candidate.Reason)
		candidate.Tags = trimStringList(candidate.Tags, 8)
		if candidate.Confidence <= 0 {
			candidate.Confidence = 0.8
		}
		if candidate.Confidence > 1 {
			candidate.Confidence = 1
		}
		switch candidate.Scope {
		case domain.MemoryScopeThread, domain.MemoryScopeChannel, domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal:
		default:
			continue
		}
		candidates = append(candidates, candidate)
	}
	output.Candidates = candidates
	return output, nil
}
