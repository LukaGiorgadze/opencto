package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

func TestOpenAIMemoryExtractorParsesCandidate(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: `{"candidates":[{"scope":"user","kind":"preference","content":"The user prefers short implementation plans before code changes.","tags":["planning"],"confidence":0.8,"reason":"The user gave a durable collaboration preference."}]}`,
			}},
		},
	}
	extractor := &OpenAIMemoryExtractor{model: model}

	output, err := extractor.ExtractMemories(context.Background(), agent.MemoryExtractionInput{
		ProjectID: "project-1",
		Event: domain.Event{
			ID:          "event-1",
			ProjectID:   "project-1",
			ChannelType: domain.ChannelTypeDiscord,
			ActorName:   "Luka",
			Body:        "before coding, give me a short plan",
		},
	})
	if err != nil {
		t.Fatalf("extract memories: %v", err)
	}
	if len(output.Candidates) != 1 {
		t.Fatalf("expected one candidate, got %#v", output.Candidates)
	}
	candidate := output.Candidates[0]
	if candidate.Scope != domain.MemoryScopeUser || candidate.Kind != "preference" {
		t.Fatalf("unexpected candidate metadata: %#v", candidate)
	}
	if candidate.Content != "The user prefers short implementation plans before code changes." {
		t.Fatalf("unexpected content: %q", candidate.Content)
	}
	if !model.options.JSONMode || model.options.ResponseMIMEType != "application/json" {
		t.Fatalf("expected json mode options, got %#v", model.options)
	}
	if len(model.messages) != 2 || !strings.Contains(messageText(model.messages[0]), "casual personal facts unrelated to OpenCTO technical work") {
		t.Fatalf("expected memory policy prompt, got %#v", model.messages)
	}
	if !strings.Contains(messageText(model.messages[0]), "casual opinions, comparisons") {
		t.Fatalf("expected prompt to reject casual opinions, got %#v", model.messages)
	}
	userPrompt := messageText(model.messages[1])
	if strings.Contains(userPrompt, "Luka") {
		t.Fatalf("extractor prompt should not include actor identity metadata: %q", userPrompt)
	}
}

func TestParseMemoryExtractionOutputFiltersInvalidCandidates(t *testing.T) {
	t.Parallel()

	output, err := parseMemoryExtractionOutput(`{"candidates":[
		{"scope":"project","kind":"instruction","content":"  Always use migrations for schema changes.  ","confidence":0},
		{"scope":"bad","kind":"fact","content":"ignore me","confidence":0.9},
		{"scope":"user","kind":"preference","content":"   "}
	]}`)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.Candidates) != 1 {
		t.Fatalf("expected one valid candidate, got %#v", output.Candidates)
	}
	candidate := output.Candidates[0]
	if candidate.Content != "Always use migrations for schema changes." {
		t.Fatalf("expected trimmed content, got %q", candidate.Content)
	}
	if candidate.Confidence != 0.8 {
		t.Fatalf("expected default confidence, got %v", candidate.Confidence)
	}
}

func TestParseMemoryExtractionOutputAllowsThreadScope(t *testing.T) {
	t.Parallel()

	output, err := parseMemoryExtractionOutput(`{"candidates":[
		{"scope":"thread","kind":"decision","content":"Use orange accents in this Discord thread.","confidence":0.7}
	]}`)
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.Candidates) != 1 || output.Candidates[0].Scope != domain.MemoryScopeThread {
		t.Fatalf("expected thread candidate, got %#v", output.Candidates)
	}
}

func TestParseMemoryExtractionOutputAcceptsEmptyCandidates(t *testing.T) {
	t.Parallel()

	output, err := parseMemoryExtractionOutput("```json\n{\"candidates\":[]}\n```")
	if err != nil {
		t.Fatalf("parse output: %v", err)
	}
	if len(output.Candidates) != 0 {
		t.Fatalf("expected no candidates, got %#v", output.Candidates)
	}
}
