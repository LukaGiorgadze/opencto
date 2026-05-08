package llm

import (
	"context"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/domain"
)

func TestOpenAIConversationCompressorUsesCompactedConversation(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: `{"summary":"User chose SQLite; tool verification passed."}`,
			}},
		},
	}
	compressor := &OpenAIConversationCompressor{model: model}

	output, err := compressor.CompressConversation(context.Background(), agent.ConversationCompressionInput{
		ProjectID:       "project-1",
		Scope:           domain.ConversationSummaryScopeProject,
		MaxSummaryChars: 2000,
		Messages: []domain.ConversationMessage{
			{ID: "user-1", Role: domain.ConversationRoleUser, Body: "use sqlite"},
			{ID: "tool-1", Role: domain.ConversationRoleTool, Body: "ok", Metadata: domain.Metadata{"tool": "exec", "status": "succeeded"}},
			{ID: "tool-2", Role: domain.ConversationRoleTool, Body: "ok", Metadata: domain.Metadata{"tool": "exec", "status": "succeeded"}},
		},
	})
	if err != nil {
		t.Fatalf("compress conversation: %v", err)
	}
	if output.Summary != "User chose SQLite; tool verification passed." {
		t.Fatalf("unexpected summary: %q", output.Summary)
	}
	if !model.options.JSONMode || model.options.ResponseMIMEType != "application/json" {
		t.Fatalf("expected json mode options, got %#v", model.options)
	}
	source := messageText(model.messages[1])
	if !strings.Contains(source, "tool[exec succeeded] x2") {
		t.Fatalf("expected compacted tool output in source, got:\n%s", source)
	}
}

func TestConversationCompressionOmitsRawReadContent(t *testing.T) {
	t.Parallel()

	model := &recordingToolModel{
		response: &llms.ContentResponse{
			Choices: []*llms.ContentChoice{{
				Content: `{"summary":"Read a large Temporal event file."}`,
			}},
		},
	}
	compressor := &OpenAIConversationCompressor{model: model}

	_, err := compressor.CompressConversation(context.Background(), agent.ConversationCompressionInput{
		ProjectID:       "project-1",
		Scope:           domain.ConversationSummaryScopeThread,
		MaxSummaryChars: 2000,
		Messages: []domain.ConversationMessage{{
			ID:   "read-1",
			Role: domain.ConversationRoleTool,
			Body: "requested_action: read event json\n\nobservation:\nfile: /tmp/events.json\nlines: 2000/46866\nbytes: 3183838\ntruncated: true\ncontent:\n     1\t{\n     2\t  \"events\": [\n     3\t    \"raw payload\" [truncated]",
			Metadata: domain.Metadata{
				"tool":        string(domain.ToolTypeRead),
				"status":      string(domain.ExecutionStatusSucceeded),
				"file_path":   "/tmp/events.json",
				"lines_read":  "2000",
				"total_lines": "46866",
				"bytes_read":  "3183838",
				"truncated":   "true",
			},
		}},
	})
	if err != nil {
		t.Fatalf("compress conversation: %v", err)
	}
	source := messageText(model.messages[1])
	if !strings.Contains(source, "content: omitted from compression source") || !strings.Contains(source, "lines: 2000/46866") {
		t.Fatalf("expected compact read metadata, got:\n%s", source)
	}
	if strings.Contains(source, "raw payload") || strings.Contains(source, "[truncated]") {
		t.Fatalf("expected raw read content to be omitted, got:\n%s", source)
	}
}
