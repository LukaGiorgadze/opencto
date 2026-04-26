package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/domain"
)

type OpenAIEngine struct {
	reasoningModel   llms.Model
	reasoningModelID string
	fastModel        llms.Model
	fastModelID      string
}

func NewOpenAIEngine(apiKey, baseURL, reasoningModelID, fastModelID string) (*OpenAIEngine, error) {
	reasoningModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(reasoningModelID),
		openai.WithResponseFormat(openai.ResponseFormatJSON),
	)
	if err != nil {
		return nil, err
	}

	fastModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(fastModelID),
		openai.WithResponseFormat(openai.ResponseFormatJSON),
	)
	if err != nil {
		return nil, err
	}

	return &OpenAIEngine{
		reasoningModel:   reasoningModel,
		reasoningModelID: reasoningModelID,
		fastModel:        fastModel,
		fastModelID:      fastModelID,
	}, nil
}

func invokeJSONMessages[T any](ctx context.Context, model llms.Model, messages []llms.MessageContent) (T, error) {
	var zero T

	response, err := model.GenerateContent(ctx, messages)
	if err != nil {
		return zero, err
	}
	if response == nil || len(response.Choices) == 0 {
		return zero, fmt.Errorf("model returned no choices")
	}

	return decodeJSONOutput[T](response.Choices[0].Content)
}

func decodeJSONOutput[T any](raw string) (T, error) {
	var zero T

	decoder := json.NewDecoder(strings.NewReader(extractJSON(raw)))
	var first json.RawMessage
	if err := decoder.Decode(&first); err != nil {
		return zero, err
	}

	var extra json.RawMessage
	switch err := decoder.Decode(&extra); err {
	case nil:
		return zero, fmt.Errorf("model returned multiple JSON values")
	case io.EOF:
	default:
		return zero, err
	}

	var output T
	strictDecoder := json.NewDecoder(strings.NewReader(string(first)))
	if err := strictDecoder.Decode(&output); err != nil {
		return zero, err
	}
	return output, nil
}

func formatProjectState(active []domain.WorkItem) string {
	if len(active) == 0 {
		return "idle"
	}
	return fmt.Sprintf("%d active work item(s)", len(active))
}

func formatActiveWorkItems(items []domain.WorkItem) string {
	if len(items) == 0 {
		return ""
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "untitled work item"
		}
		parts = append(parts, title+" ["+string(item.Status)+"]")
	}
	return strings.Join(parts, "; ")
}

func formatAvailableSkills(skills []string) string {
	values := trimStringList(skills, 32)
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func compactWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func payloadString(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	value, ok := payload[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func trimStringList(values []string, max int) []string {
	if max <= 0 {
		return nil
	}
	trimmed := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		trimmed = append(trimmed, value)
		if len(trimmed) == max {
			break
		}
	}
	return trimmed
}

func extractJSON(raw string) string {
	value := strings.TrimSpace(raw)
	value = strings.TrimPrefix(value, "```json")
	value = strings.TrimPrefix(value, "```")
	value = strings.TrimSuffix(value, "```")
	return strings.TrimSpace(value)
}
