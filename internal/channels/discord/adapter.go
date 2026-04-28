package discord

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
)

const discordMessageMaxLength = 4000
const discordAttachmentMaxBytes int64 = 20 << 20

const discordAttachmentPayloadKey = "attachments"

type Adapter struct {
	projectID     string
	dispatcher    *runtime.Dispatcher
	session       *discordgo.Session
	logger        *slog.Logger
	attachmentDir string
	httpClient    *http.Client
}

func New(projectID, token, _ string, dispatcher *runtime.Dispatcher, logger *slog.Logger) (*Adapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("discord token is required")
	}
	session, err := discordgo.New("Bot " + token)
	if err != nil {
		return nil, err
	}
	session.Identify.Intents = discordgo.IntentGuildMessages | discordgo.IntentMessageContent | discordgo.IntentDirectMessages
	session.StateEnabled = false
	attachmentDir, err := filepath.Abs(filepath.Join("data", "attachments", safePathSegment(projectID), "discord"))
	if err != nil {
		return nil, err
	}
	return &Adapter{
		projectID:     projectID,
		dispatcher:    dispatcher,
		session:       session,
		logger:        logger,
		attachmentDir: attachmentDir,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	a.session.AddHandler(func(session *discordgo.Session, message *discordgo.MessageCreate) {
		if message.Author == nil || message.Author.Bot {
			return
		}
		event, err := a.NormalizeMessage(ctx, message)
		if err != nil {
			a.logger.Error("normalize discord message", slog.String("error", err.Error()))
			return
		}
		if err := a.NotifyTyping(ctx, event); err != nil {
			a.logger.Warn("notify discord typing", slog.String("error", err.Error()), slog.String("event_id", event.ID))
		}
		if err := a.dispatcher.EnqueueEvent(ctx, event); err != nil {
			a.logger.Error("enqueue discord event", slog.String("error", err.Error()), slog.String("event_id", event.ID))
		}
	})
	return a.session.Open()
}

func (a *Adapter) Close() error {
	if a.session == nil {
		return nil
	}
	return a.session.Close()
}

func (a *Adapter) NormalizeMessage(ctx context.Context, message *discordgo.MessageCreate) (domain.Event, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.Event{}, err
	}

	now := time.Now().UTC()
	body := normalizeDiscordBody(message.Content)
	attachments, err := a.normalizeAttachments(ctx, id, message.Attachments, now)
	if err != nil {
		return domain.Event{}, err
	}
	body = bodyWithAttachmentFallback(body, attachments)
	payload := map[string]any{
		"message_id": message.ID,
		"guild_id":   message.GuildID,
	}
	if len(attachments) > 0 {
		payload[discordAttachmentPayloadKey] = attachments
	}
	return domain.Event{
		ID:          id,
		ProjectID:   a.projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   message.ChannelID,
		ChannelType: domain.ChannelTypeDiscord,
		ActorID:     message.Author.ID,
		ActorName:   message.Author.Username,
		Body:        body,
		Payload:     payload,
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeDiscord),
			SourceID:   message.ID,
			Actor:      message.Author.Username,
			ObservedAt: now,
		},
		CreatedAt: now,
	}, nil
}

func (a *Adapter) normalizeAttachments(ctx context.Context, eventID string, attachments []*discordgo.MessageAttachment, observedAt time.Time) ([]domain.EventAttachment, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	out := make([]domain.EventAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		if attachment == nil {
			continue
		}
		id, err := domain.NewID()
		if err != nil {
			return nil, err
		}
		item := domain.EventAttachment{
			ID:          id,
			ProjectID:   a.projectID,
			EventID:     eventID,
			Source:      string(domain.ChannelTypeDiscord),
			SourceID:    attachment.ID,
			Filename:    attachment.Filename,
			ContentType: strings.TrimSpace(attachment.ContentType),
			SizeBytes:   int64(attachment.Size),
			URL:         strings.TrimSpace(attachment.URL),
			Metadata: map[string]string{
				"width":         fmt.Sprintf("%d", attachment.Width),
				"height":        fmt.Sprintf("%d", attachment.Height),
				"duration_secs": fmt.Sprintf("%g", attachment.DurationSecs),
			},
			CreatedAt: observedAt,
		}
		localPath, size, contentType, err := a.downloadAttachment(ctx, eventID, item)
		if err != nil {
			item.Metadata["download_error"] = err.Error()
			out = append(out, item)
			continue
		}
		item.LocalPath = localPath
		item.SizeBytes = size
		if contentType != "" {
			item.ContentType = contentType
		}
		out = append(out, item)
	}
	return out, nil
}

func (a *Adapter) downloadAttachment(ctx context.Context, eventID string, attachment domain.EventAttachment) (string, int64, string, error) {
	if attachment.URL == "" {
		return "", 0, "", fmt.Errorf("attachment URL is empty")
	}
	if attachment.SizeBytes > discordAttachmentMaxBytes {
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", discordAttachmentMaxBytes)
	}
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
	if err != nil {
		return "", 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", 0, "", fmt.Errorf("download failed with status %s", resp.Status)
	}
	if resp.ContentLength > discordAttachmentMaxBytes {
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", discordAttachmentMaxBytes)
	}

	dir := filepath.Join(a.attachmentDir, safePathSegment(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, "", err
	}
	name := safeAttachmentFilename(attachment.SourceID, attachment.Filename)
	finalPath := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", 0, "", err
	}
	tmpPath := tmp.Name()
	copied, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, discordAttachmentMaxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", closeErr
	}
	if copied > discordAttachmentMaxBytes {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", discordAttachmentMaxBytes)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", err
	}

	contentType := strings.TrimSpace(attachment.ContentType)
	if contentType == "" {
		contentType = strings.TrimSpace(resp.Header.Get("Content-Type"))
	}
	return finalPath, copied, contentType, nil
}

func normalizeDiscordBody(content string) string {
	fields := strings.Fields(content)
	filtered := make([]string, 0, len(fields))
	for _, field := range fields {
		if strings.HasPrefix(field, "<@") && strings.HasSuffix(field, ">") {
			continue
		}
		filtered = append(filtered, field)
	}
	return strings.TrimSpace(strings.Join(filtered, " "))
}

func bodyWithAttachmentFallback(body string, attachments []domain.EventAttachment) string {
	body = strings.TrimSpace(body)
	if body != "" || len(attachments) == 0 {
		return body
	}
	names := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Filename)
		if name == "" {
			name = "attachment"
		}
		if attachment.ContentType != "" {
			name += " (" + attachment.ContentType + ")"
		}
		names = append(names, name)
	}
	return "Uploaded attachment(s): " + strings.Join(names, ", ")
}

func safeAttachmentFilename(sourceID, filename string) string {
	filename = safePathSegment(filename)
	if filename == "" {
		filename = "attachment"
	}
	sourceID = safePathSegment(sourceID)
	if sourceID == "" {
		return filename
	}
	return sourceID + "-" + filename
}

func safePathSegment(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	value = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, value)
	return strings.Trim(value, ".-")
}

func (a *Adapter) Report(ctx context.Context, event domain.Event, message string) error {
	if a.session == nil || event.ChannelID == "" {
		return nil
	}
	if err := a.NotifyTyping(ctx, event); err != nil && a.logger != nil {
		a.logger.Warn("notify discord typing before report", slog.String("error", err.Error()), slog.String("event_id", event.ID))
	}
	for _, chunk := range splitDiscordMessage(message, discordMessageMaxLength) {
		if _, err := a.session.ChannelMessageSend(event.ChannelID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) NotifyTyping(ctx context.Context, event domain.Event) error {
	if a.session == nil || strings.TrimSpace(event.ChannelID) == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return a.session.ChannelTyping(event.ChannelID)
}

func splitDiscordMessage(message string, limit int) []string {
	message = strings.TrimSpace(message)
	if message == "" {
		return []string{""}
	}
	if limit <= 0 {
		limit = discordMessageMaxLength
	}

	var chunks []string
	remaining := message
	for len(remaining) > 0 {
		if utf8.RuneCountInString(remaining) <= limit {
			chunks = append(chunks, remaining)
			break
		}

		cut := longestPrefixByRunes(remaining, limit)
		prefix := remaining[:cut]
		splitAt := bestDiscordSplit(prefix)
		if splitAt <= 0 {
			splitAt = cut
		}

		chunk := strings.TrimSpace(remaining[:splitAt])
		if chunk == "" {
			chunk = strings.TrimSpace(prefix)
			splitAt = len(prefix)
		}
		chunks = append(chunks, chunk)
		remaining = strings.TrimSpace(remaining[splitAt:])
	}

	return chunks
}

func longestPrefixByRunes(text string, limit int) int {
	if limit <= 0 {
		return 0
	}
	runes := 0
	for idx := range text {
		if runes == limit {
			return idx
		}
		runes++
	}
	return len(text)
}

func bestDiscordSplit(text string) int {
	for i := len(text) - 1; i >= 0; i-- {
		switch text[i] {
		case '\n':
			return i + 1
		case ' ', '\t':
			if i > 0 {
				return i + 1
			}
		}
	}
	return 0
}
