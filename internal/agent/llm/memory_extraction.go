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

type OpenAIMemoryExtractor struct {
	model llms.Model
}

func NewOpenAIMemoryExtractor(apiKey, baseURL, modelID string) (*OpenAIMemoryExtractor, error) {
	model, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(modelID),
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
	response, err := e.model.GenerateContent(ctx, memoryExtractionMessages(input), llms.WithOptions(llms.CallOptions{
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

func memoryExtractionMessages(input agent.MemoryExtractionInput) []llms.MessageContent {
	return []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, memoryExtractionSystemPrompt()),
		llms.TextParts(llms.ChatMessageTypeHuman, memoryExtractionUserPrompt(input)),
	}
}

func memoryExtractionSystemPrompt() string {
	return strings.TrimSpace(`
You extract durable OpenCTO memory candidates from a single user message.

Return only JSON:
{"candidates":[{"scope":"project|user|global","kind":"fact|preference|instruction|decision|constraint|identity|workflow|reference|feedback|project|user","content":"...","tags":["..."],"confidence":0.8,"pinned":false,"reason":"..."}]}

Use an empty candidates array when nothing should be saved.

Save only durable information that is likely useful for future OpenCTO technical work:
- explicit requests to remember durable information that affects future OpenCTO collaboration
- stable identity or communication facts that affect collaboration, such as preferred name, role, or communication style
- stable project decisions, constraints, product goals, external references, deployment/process details
- user working preferences that affect future technical collaboration
- standing instructions such as always/never/by default/from now on

Do not save:
- casual personal facts unrelated to OpenCTO technical work, such as where someone lives, unless it directly affects scheduling, operations, compliance, or technical workflow
- temporary choices: for now, today, this task, this migration, just this once
- logs, diffs, command output, stack traces, secrets, tokens, passwords, API keys
- repo facts that should be discovered from files
- one-off conversation details or random trivia

Scopes:
- project: facts/preferences/decisions shared by the current project
- user: preferences/facts belonging only to the current user
- global: rare shared rules or facts that should apply across users

Confidence:
- 1.0 for explicit remember/always/never/by default/from now on
- 0.8 for clear inferred durable technical preferences/facts
- 0.6 for useful but less explicit durable technical context

Examples:
- "Remember my name is Luka." -> user identity memory.
- "Always ask before broad refactors." -> user preference memory.
- "Production incidents live in Linear project INFRA." -> project reference memory.
- "I live in Tbilisi." -> no memory unless it affects scheduling, operations, compliance, or technical workflow.

Keep content concise. Do not duplicate existing memories.
`)
}

func memoryExtractionUserPrompt(input agent.MemoryExtractionInput) string {
	var builder strings.Builder
	builder.WriteString("Project ID: ")
	builder.WriteString(strings.TrimSpace(input.ProjectID))
	builder.WriteString("\nChannel: ")
	builder.WriteString(string(input.Event.ChannelType))
	builder.WriteString("\nActor ID: ")
	builder.WriteString(strings.TrimSpace(input.Event.ActorID))
	builder.WriteString("\nActor name: ")
	builder.WriteString(strings.TrimSpace(input.Event.ActorName))
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
		case domain.MemoryScopeProject, domain.MemoryScopeUser, domain.MemoryScopeGlobal:
		default:
			continue
		}
		candidates = append(candidates, candidate)
	}
	output.Candidates = candidates
	return output, nil
}
