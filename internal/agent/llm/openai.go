package llm

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms"
	openai "github.com/tmc/langchaingo/llms/openai"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
)

type APIKeySource string

const (
	APIKeySourceConfig       APIKeySource = "config"
	APIKeySourceEnvironment  APIKeySource = "environment"
	APIKeySourceLegacyConfig APIKeySource = "legacy_api_key_env"
)

const eventPayloadAttachmentsKey = "attachments"

func ResolveOpenAIAPIKey(cfg config.LLMConfig) (string, APIKeySource, error) {
	if value := strings.TrimSpace(cfg.APIKey); value != "" {
		return value, APIKeySourceConfig, nil
	}

	name := strings.TrimSpace(cfg.APIKeyEnv)
	if name == "" {
		return "", "", fmt.Errorf("llm.api_key_env is empty")
	}

	if strings.HasPrefix(name, "sk-") {
		return name, APIKeySourceLegacyConfig, nil
	}

	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", name)
	}
	return value, APIKeySourceEnvironment, nil
}

func NewOpenAIEmbedder(apiKey, baseURL, embeddingModel string, dimensions int) (*embeddings.EmbedderImpl, error) {
	options := []openai.Option{
		openai.WithToken(apiKey),
		openai.WithBaseURL(baseURL),
		openai.WithEmbeddingModel(embeddingModel),
	}
	if dimensions > 0 {
		options = append(options, openai.WithEmbeddingDimensions(dimensions))
	}

	model, err := openai.New(options...)
	if err != nil {
		return nil, err
	}

	return embeddings.NewEmbedder(model)
}

func openAIUserMessageFromEvent(event domain.Event) (llms.MessageContent, error) {
	attachments, err := eventAttachments(event.Payload)
	if err != nil {
		return llms.MessageContent{}, err
	}

	body := strings.TrimSpace(event.Body)
	parts := make([]llms.ContentPart, 0, 1+len(attachments)*2)
	if body != "" || len(attachments) == 0 {
		parts = append(parts, llms.TextPart(body))
	}
	for _, attachment := range attachments {
		summary := attachmentSummary(attachment)
		if summary != "" {
			parts = append(parts, llms.TextPart(summary))
		}
		part, err := openAIContentPartForAttachment(attachment)
		if err != nil {
			return llms.MessageContent{}, err
		}
		if part != nil {
			parts = append(parts, part)
		}
	}
	return llms.MessageContent{
		Role:  llms.ChatMessageTypeHuman,
		Parts: parts,
	}, nil
}

func eventAttachments(payload map[string]any) ([]domain.EventAttachment, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	raw, ok := payload[eventPayloadAttachmentsKey]
	if !ok || raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var attachments []domain.EventAttachment
	if err := json.Unmarshal(data, &attachments); err != nil {
		return nil, err
	}
	return attachments, nil
}

func openAIContentPartForAttachment(attachment domain.EventAttachment) (llms.ContentPart, error) {
	localPath := strings.TrimSpace(attachment.LocalPath)
	if localPath == "" {
		if strings.HasPrefix(strings.ToLower(attachment.ContentType), "image/") && strings.TrimSpace(attachment.URL) != "" {
			return llms.ImageURLPart(strings.TrimSpace(attachment.URL)), nil
		}
		return nil, nil
	}
	cleanPath := filepath.Clean(localPath)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("read attachment %q: %w", attachment.Filename, err)
	}
	contentType := attachmentContentType(attachment.ContentType, data)
	if strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return llms.ImageURLPart(dataURI(contentType, data)), nil
	}
	return nil, nil
}

func attachmentContentType(contentType string, data []byte) string {
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	if contentType != "" {
		return contentType
	}
	if len(data) == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(data)
}

func attachmentSummary(attachment domain.EventAttachment) string {
	name := strings.TrimSpace(attachment.Filename)
	if name == "" {
		name = "attachment"
	}
	contentType := strings.TrimSpace(attachment.ContentType)
	if contentType == "" {
		contentType = "unknown type"
	}
	if attachment.LocalPath == "" {
		if errText := attachment.Metadata["download_error"]; strings.TrimSpace(errText) != "" {
			return fmt.Sprintf("\nAttachment unavailable: %s (%s): %s", name, contentType, errText)
		}
		return fmt.Sprintf("\nAttachment metadata: %s (%s, %d bytes)", name, contentType, attachment.SizeBytes)
	}
	return fmt.Sprintf("\nAttachment available locally: %s (%s, %d bytes) at %s", name, contentType, attachment.SizeBytes, attachment.LocalPath)
}

func dataURI(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
