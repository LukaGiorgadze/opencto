package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/agent"
	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/domain"
)

type audioTranscriber interface {
	TranscribeAudio(context.Context, domain.EventAttachment) (string, error)
}

type openAICompatibleAudioTranscriber struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	convert    func(context.Context, string) (string, error)
}

func newOpenAICompatibleAudioTranscriber(apiKey, baseURL, model string, httpClient *http.Client) *openAICompatibleAudioTranscriber {
	model = strings.TrimSpace(model)
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &openAICompatibleAudioTranscriber{
		apiKey:     strings.TrimSpace(apiKey),
		baseURL:    strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		model:      model,
		httpClient: httpClient,
		convert:    convertOggAudioToWav,
	}
}

func (t *openAICompatibleAudioTranscriber) TranscribeAudio(ctx context.Context, attachment domain.EventAttachment) (string, error) {
	if t == nil {
		return "", fmt.Errorf("audio transcriber is not configured")
	}
	path := strings.TrimSpace(attachment.LocalPath)
	if path == "" {
		return "", fmt.Errorf("audio attachment has no local path")
	}
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("open audio attachment: %w", err)
	}
	defer file.Close()

	filename := firstNonEmpty(attachment.Filename, filepath.Base(path))
	if shouldConvertAudioForTranscription(attachment, path) {
		if t.convert == nil {
			return "", fmt.Errorf("audio converter is not configured")
		}
		convertedPath, err := t.convert(ctx, path)
		if err != nil {
			return "", fmt.Errorf("convert audio attachment: %w", err)
		}
		defer os.Remove(convertedPath)
		file.Close()
		file, err = os.Open(filepath.Clean(convertedPath))
		if err != nil {
			return "", fmt.Errorf("open converted audio attachment: %w", err)
		}
		defer file.Close()
		filename = convertedAudioFilename(filename)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", t.model); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.audioTranscriptionURL(), &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if t.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+t.apiKey)
	}

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("transcription failed with status %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}

	var output struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		return "", fmt.Errorf("decode transcription response: %w", err)
	}
	text := strings.TrimSpace(output.Text)
	if text == "" {
		return "", fmt.Errorf("transcription response is empty")
	}
	return text, nil
}

func (t *openAICompatibleAudioTranscriber) audioTranscriptionURL() string {
	if t.baseURL == "" {
		return "https://api.openai.com/v1/audio/transcriptions"
	}
	return t.baseURL + "/audio/transcriptions"
}

func shouldConvertAudioForTranscription(attachment domain.EventAttachment, path string) bool {
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	name := strings.ToLower(strings.TrimSpace(firstNonEmpty(attachment.Filename, filepath.Base(path))))
	ext := filepath.Ext(name)
	return contentType == "audio/ogg" || contentType == "audio/opus" || ext == ".oga" || ext == ".ogg" || ext == ".opus"
}

func convertedAudioFilename(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "audio.wav"
	}
	ext := filepath.Ext(filename)
	if ext == "" {
		return filename + ".wav"
	}
	return strings.TrimSuffix(filename, ext) + ".wav"
}

func convertOggAudioToWav(ctx context.Context, path string) (string, error) {
	input := filepath.Clean(path)
	output, err := os.CreateTemp("", "opencto-audio-*.wav")
	if err != nil {
		return "", err
	}
	outputPath := output.Name()
	if err := output.Close(); err != nil {
		_ = os.Remove(outputPath)
		return "", err
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", input, "-ac", "1", "-ar", "16000", outputPath)
	data, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.Remove(outputPath)
		detail := strings.TrimSpace(string(data))
		if detail == "" {
			detail = err.Error()
		}
		return "", fmt.Errorf("ffmpeg failed: %s", detail)
	}
	return outputPath, nil
}

func (e *OpenAIEngine) enrichInputWithAttachmentTranscripts(ctx context.Context, input agent.NextActionInput) (agent.NextActionInput, error) {
	attachments, err := eventAttachments(input.Context.Event.Payload)
	if err != nil {
		return input, err
	}
	if len(attachments) == 0 || e.audioTranscriber == nil {
		return input, nil
	}

	var transcriptParts []string
	for _, attachment := range attachments {
		if !isAudioAttachment(attachment) || strings.TrimSpace(attachment.LocalPath) == "" {
			continue
		}
		transcript, err := e.audioTranscriber.TranscribeAudio(ctx, attachment)
		if err != nil {
			transcriptParts = append(transcriptParts, prompts.MustRender("audio_transcription_failed.tmpl", map[string]any{
				"Name":  attachmentDisplayName(attachment),
				"Error": err,
			}))
			continue
		}
		transcriptParts = append(transcriptParts, prompts.MustRender("voice_message_transcript.tmpl", map[string]any{
			"Name":       attachmentDisplayName(attachment),
			"Transcript": transcript,
		}))
	}
	if len(transcriptParts) == 0 {
		return input, nil
	}

	body := strings.TrimSpace(input.Context.Event.Body)
	transcriptText := strings.Join(transcriptParts, "\n")
	if body == "" || strings.HasPrefix(body, prompts.MustRender("uploaded_attachments.tmpl", map[string]any{"Names": ""})) {
		input.Context.Event.Body = transcriptText
		return input, nil
	}
	input.Context.Event.Body = body + "\n\n" + transcriptText
	return input, nil
}

func isAudioAttachment(attachment domain.EventAttachment) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.ContentType)), "audio/")
}

func attachmentDisplayName(attachment domain.EventAttachment) string {
	return prompts.MustRender("attachment_display_name.tmpl", map[string]any{
		"Filename": strings.TrimSpace(attachment.Filename),
		"SourceID": strings.TrimSpace(attachment.SourceID),
	})
}
