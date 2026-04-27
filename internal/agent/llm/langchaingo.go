package llm

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/domain"
)

type OpenAIEngine struct {
	reasoningModel   llms.Model
	reasoningModelID string
	fastModel        llms.Model
	fastModelID      string
	audioTranscriber audioTranscriber
}

func NewOpenAIEngine(apiKey, baseURL, reasoningModelID, fastModelID, transcriptionModel string) (*OpenAIEngine, error) {
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
		audioTranscriber: newOpenAICompatibleAudioTranscriber(apiKey, baseURL, transcriptionModel, &http.Client{Timeout: 2 * time.Minute}),
	}, nil
}

func formatProjectState(active []domain.WorkItem) string {
	if len(active) == 0 {
		return "idle"
	}
	return fmt.Sprintf("%d active work item(s)", len(active))
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
