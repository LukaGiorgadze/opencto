package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/media"
)

type APIKeySource string

const (
	APIKeySourceConfig      APIKeySource = "config"
	APIKeySourceEnvironment APIKeySource = "environment"
)

const (
	bifrostAPIKeyEnv           = "BIFROST_API_KEY"
	eventPayloadAttachmentsKey = "attachments"
)

var defaultImageResolver = media.NewImageResolver(media.DefaultImageResolverConfig())

func ResolveOpenAIAPIKey(cfg config.LLMConfig) (string, APIKeySource, error) {
	if value := strings.TrimSpace(cfg.APIKey); value != "" {
		return value, APIKeySourceConfig, nil
	}

	value := strings.TrimSpace(os.Getenv(bifrostAPIKeyEnv))
	if value == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", bifrostAPIKeyEnv)
	}
	return value, APIKeySourceEnvironment, nil
}

func openAIUserMessageFromEvent(ctx context.Context, event domain.Event, imageResolver *media.ImageResolver) (llms.MessageContent, error) {
	attachments, err := eventAttachments(event.Payload)
	if err != nil {
		return llms.MessageContent{}, err
	}
	if imageResolver == nil {
		imageResolver = defaultImageResolver
	}
	if ctx == nil {
		ctx = context.Background()
	}
	imageCtx, cancel := context.WithTimeout(ctx, media.DefaultFetchTimeout)
	defer cancel()

	body := strings.TrimSpace(event.Body)
	parts := make([]llms.ContentPart, 0, 1+len(attachments)*3)
	if body != "" || len(attachments) == 0 {
		parts = append(parts, llms.TextPart(body))
	}
	state := imageMessageState{seenURLs: map[string]bool{}}
	for _, attachment := range attachments {
		summary := attachmentSummary(attachment)
		if summary != "" {
			parts = append(parts, llms.TextPart(summary))
		}
		if url := strings.TrimSpace(attachment.URL); url != "" {
			state.seenURLs[url] = true
		}
		parts = appendImageParts(imageCtx, imageResolver, parts, attachment, &state)
	}
	for _, imageURL := range media.ImageURLsFromText(body, media.DefaultMaxImageCandidatesPerEvent) {
		if state.seenURLs[imageURL] {
			continue
		}
		state.seenURLs[imageURL] = true
		attachment := domain.EventAttachment{
			Filename: imageURL,
			URL:      imageURL,
		}
		parts = append(parts, llms.TextPart(inlineImageURLSummary(imageURL)))
		parts = appendImageParts(imageCtx, imageResolver, parts, attachment, &state)
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

type imageMessageState struct {
	images     int
	candidates int
	urlFetches int
	totalBytes int64
	seenURLs   map[string]bool
}

func appendImageParts(ctx context.Context, imageResolver *media.ImageResolver, parts []llms.ContentPart, attachment domain.EventAttachment, state *imageMessageState) []llms.ContentPart {
	if state == nil || !media.MaybeImageAttachment(attachment) {
		return parts
	}
	if imageResolver == nil {
		imageResolver = defaultImageResolver
	}
	if state.candidates >= media.DefaultMaxImageCandidatesPerEvent {
		return append(parts, llms.TextPart(imageSkipSummary(attachment, fmt.Sprintf("image candidate limit reached; only the first %d possible image(s) are checked", media.DefaultMaxImageCandidatesPerEvent))))
	}
	state.candidates++
	if state.images >= media.DefaultMaxImagesPerEvent {
		return append(parts, llms.TextPart(imageSkipSummary(attachment, fmt.Sprintf("image limit reached; only the first %d image(s) are sent", media.DefaultMaxImagesPerEvent))))
	}
	if attachmentHasDownloadError(attachment) && strings.TrimSpace(attachment.LocalPath) == "" {
		return parts
	}
	if strings.TrimSpace(attachment.LocalPath) == "" && strings.TrimSpace(attachment.URL) != "" {
		if state.urlFetches >= media.DefaultMaxImageURLFetchesPerEvent {
			return append(parts, llms.TextPart(imageSkipSummary(attachment, fmt.Sprintf("image URL fetch limit reached; only the first %d image URL(s) are fetched", media.DefaultMaxImageURLFetchesPerEvent))))
		}
		state.urlFetches++
	}
	resolution := imageResolver.Resolve(ctx, attachment)
	switch resolution.Status {
	case media.ImageStatusOK:
		size := int64(len(resolution.Image.Data))
		if state.totalBytes+size > media.DefaultMaxTotalImageBytes {
			return append(parts, llms.TextPart(imageSkipSummary(attachment, fmt.Sprintf("total image byte limit reached; at most %d byte(s) are sent", media.DefaultMaxTotalImageBytes))))
		}
		state.totalBytes += size
		state.images++
		if note := imageValidationNote(attachment, resolution.Image); note != "" {
			parts = append(parts, llms.TextPart(note))
		}
		return append(parts, llms.ImageURLPart(dataURI(resolution.Image.ContentType, resolution.Image.Data)))
	case media.ImageStatusSkipped:
		return append(parts, llms.TextPart(imageSkipSummary(attachment, resolution.Reason)))
	default:
		return parts
	}
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

func attachmentHasDownloadError(attachment domain.EventAttachment) bool {
	if len(attachment.Metadata) == 0 {
		return false
	}
	return strings.TrimSpace(attachment.Metadata["download_error"]) != ""
}

func inlineImageURLSummary(imageURL string) string {
	return "\nInline image URL candidate: " + strings.TrimSpace(imageURL)
}

func imageValidationNote(attachment domain.EventAttachment, image media.ResolvedImage) string {
	if !image.ContentTypeMismatch {
		return ""
	}
	declared := strings.TrimSpace(image.DeclaredContentType)
	if declared == "" {
		declared = "unknown type"
	}
	return fmt.Sprintf("\nAttachment content type corrected for %s: declared %s, detected %s", attachmentDisplayLabel(attachment), declared, image.DetectedContentType)
}

func imageSkipSummary(attachment domain.EventAttachment, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "image could not be validated"
	}
	return fmt.Sprintf("\nImage attachment skipped: %s: %s", attachmentDisplayLabel(attachment), reason)
}

func attachmentDisplayLabel(attachment domain.EventAttachment) string {
	return firstNonEmpty(attachment.Filename, attachment.SourceID, attachment.URL, attachment.LocalPath, "attachment")
}

func dataURI(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
