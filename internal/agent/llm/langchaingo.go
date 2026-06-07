package llm

import (
	"strings"

	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/media"
)

type OpenAIEngine struct {
	reasoningModel   llms.Model
	reasoningModelID string
	fastModel        llms.Model
	fastModelID      string
	audioTranscriber audioTranscriber
	imageResolver    *media.ImageResolver
}

func NewOpenAIEngine(apiKey, baseURL, reasoningModelID, fastModelID, transcriptionModel string, bifrostEnabled bool) (*OpenAIEngine, error) {
	httpClient := newOpenAIHTTPClient(bifrostEnabled)
	reasoningModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(reasoningModelID),
		openai.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}

	fastModel, err := openai.New(
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithModel(fastModelID),
		openai.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, err
	}

	return &OpenAIEngine{
		reasoningModel:   reasoningModel,
		reasoningModelID: reasoningModelID,
		fastModel:        fastModel,
		fastModelID:      fastModelID,
		audioTranscriber: newOpenAICompatibleAudioTranscriber(apiKey, baseURL, transcriptionModel, httpClient),
		imageResolver:    media.NewImageResolver(media.DefaultImageResolverConfig()),
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
