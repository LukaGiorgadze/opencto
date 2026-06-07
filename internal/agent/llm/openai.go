package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/tmc/langchaingo/llms"

	"github.com/opencto/opencto/internal/agent/prompts"
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
	openAIAPIKeyEnv            = "OPENAI_API_KEY"
	bifrostAPIKeyEnv           = "BIFROST_API_KEY"
	eventPayloadAttachmentsKey = "attachments"
)

var defaultImageResolver = media.NewImageResolver(media.DefaultImageResolverConfig())

func ResolveOpenAIAPIKey(cfg config.LLMConfig) (string, APIKeySource, error) {
	if value := strings.TrimSpace(cfg.APIKey); value != "" {
		return value, APIKeySourceConfig, nil
	}

	envName := openAIAPIKeyEnv
	if cfg.Bifrost.Enabled {
		envName = bifrostAPIKeyEnv
	}
	value := strings.TrimSpace(os.Getenv(envName))
	if value == "" {
		return "", "", fmt.Errorf("environment variable %q is not set", envName)
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
		return append(parts, llms.TextPart(imageSkipSummary(attachment, imageLimitReason("candidate", media.DefaultMaxImageCandidatesPerEvent))))
	}
	state.candidates++
	if state.images >= media.DefaultMaxImagesPerEvent {
		return append(parts, llms.TextPart(imageSkipSummary(attachment, imageLimitReason("image", media.DefaultMaxImagesPerEvent))))
	}
	if attachmentHasDownloadError(attachment) && strings.TrimSpace(attachment.LocalPath) == "" {
		return parts
	}
	if strings.TrimSpace(attachment.LocalPath) == "" && strings.TrimSpace(attachment.URL) != "" {
		if state.urlFetches >= media.DefaultMaxImageURLFetchesPerEvent {
			return append(parts, llms.TextPart(imageSkipSummary(attachment, imageLimitReason("url_fetch", media.DefaultMaxImageURLFetchesPerEvent))))
		}
		state.urlFetches++
	}
	resolution := imageResolver.Resolve(ctx, attachment)
	switch resolution.Status {
	case media.ImageStatusOK:
		size := int64(len(resolution.Image.Data))
		if state.totalBytes+size > media.DefaultMaxTotalImageBytes {
			return append(parts, llms.TextPart(imageSkipSummary(attachment, imageLimitReason("total_bytes", media.DefaultMaxTotalImageBytes))))
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
	return prompts.MustRender("attachment_summary.tmpl", map[string]any{
		"Name":          strings.TrimSpace(attachment.Filename),
		"ContentType":   strings.TrimSpace(attachment.ContentType),
		"SizeBytes":     attachment.SizeBytes,
		"LocalPath":     strings.TrimSpace(attachment.LocalPath),
		"DownloadError": strings.TrimSpace(attachment.Metadata["download_error"]),
	})
}

func attachmentHasDownloadError(attachment domain.EventAttachment) bool {
	if len(attachment.Metadata) == 0 {
		return false
	}
	return strings.TrimSpace(attachment.Metadata["download_error"]) != ""
}

func inlineImageURLSummary(imageURL string) string {
	return prompts.MustRender("inline_image_url_summary.tmpl", map[string]any{
		"URL": strings.TrimSpace(imageURL),
	})
}

func imageValidationNote(attachment domain.EventAttachment, image media.ResolvedImage) string {
	if !image.ContentTypeMismatch {
		return ""
	}
	return prompts.MustRender("image_content_type_correction.tmpl", map[string]any{
		"Label":    attachmentDisplayLabel(attachment),
		"Declared": strings.TrimSpace(image.DeclaredContentType),
		"Detected": image.DetectedContentType,
	})
}

func imageSkipSummary(attachment domain.EventAttachment, reason string) string {
	return prompts.MustRender("image_skip_summary.tmpl", map[string]any{
		"Label":  attachmentDisplayLabel(attachment),
		"Reason": strings.TrimSpace(reason),
	})
}

func imageLimitReason(kind string, limit any) string {
	return prompts.MustRender("image_limit_reason.tmpl", map[string]any{
		"Kind":  strings.TrimSpace(kind),
		"Limit": limit,
	})
}

func attachmentDisplayLabel(attachment domain.EventAttachment) string {
	return prompts.MustRender("attachment_display_label.tmpl", map[string]any{
		"Filename":  strings.TrimSpace(attachment.Filename),
		"SourceID":  strings.TrimSpace(attachment.SourceID),
		"URL":       strings.TrimSpace(attachment.URL),
		"LocalPath": strings.TrimSpace(attachment.LocalPath),
	})
}

func dataURI(contentType string, data []byte) string {
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
