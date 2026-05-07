package llm

import (
	"net/http"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"
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
	)
	if err != nil {
		return nil, err
	}

	fastModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(fastModelID),
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
