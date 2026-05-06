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

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/channels"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
)

const discordOutboundMessageMaxChars = 2000
const discordAttachmentMaxBytes int64 = 20 << 20
const discordOutboundAttachmentMaxFiles = 10
const discordDefaultOutboundMaxFileBytes int64 = 10 << 20
const discordDefaultOutboundMaxTotalBytes int64 = 25 << 20

const discordAttachmentPayloadKey = "attachments"

type AttachmentLimits = channels.AttachmentLimits
type MessageLimits = channels.MessageLimits

type Options struct {
	WorkspaceRoot    string
	MessageLimits    MessageLimits
	AttachmentLimits AttachmentLimits
}

type Adapter struct {
	projectID        string
	dispatcher       *runtime.Dispatcher
	session          *discordgo.Session
	logger           *slog.Logger
	attachmentDir    string
	httpClient       *http.Client
	workspaceRoot    string
	messageLimits    MessageLimits
	attachmentLimits AttachmentLimits
}

func New(projectID, token, _ string, dispatcher *runtime.Dispatcher, logger *slog.Logger, opts ...Options) (*Adapter, error) {
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
	options := defaultOptions()
	if len(opts) > 0 {
		options = opts[0]
		options.MessageLimits = normalizeMessageLimits(options.MessageLimits)
		options.AttachmentLimits = normalizeAttachmentLimits(options.AttachmentLimits)
	}
	return &Adapter{
		projectID:        projectID,
		dispatcher:       dispatcher,
		session:          session,
		logger:           logger,
		attachmentDir:    attachmentDir,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		workspaceRoot:    options.WorkspaceRoot,
		messageLimits:    options.MessageLimits,
		attachmentLimits: options.AttachmentLimits,
	}, nil
}

func defaultOptions() Options {
	return Options{
		MessageLimits: MessageLimits{
			MaxChars: discordOutboundMessageMaxChars,
		},
		AttachmentLimits: AttachmentLimits{
			MaxFiles:      discordOutboundAttachmentMaxFiles,
			MaxFileBytes:  discordDefaultOutboundMaxFileBytes,
			MaxTotalBytes: discordDefaultOutboundMaxTotalBytes,
		},
	}
}

func normalizeMessageLimits(limits MessageLimits) MessageLimits {
	if limits.MaxChars == 0 {
		limits.MaxChars = discordOutboundMessageMaxChars
	}
	return limits
}

func normalizeAttachmentLimits(limits AttachmentLimits) AttachmentLimits {
	if limits.MaxFiles == 0 {
		limits.MaxFiles = discordOutboundAttachmentMaxFiles
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = discordDefaultOutboundMaxFileBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = discordDefaultOutboundMaxTotalBytes
	}
	return limits
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (a *Adapter) Report(ctx context.Context, event domain.Event, report domain.ReportMessage) error {
	if a.session == nil || event.ChannelID == "" {
		return nil
	}
	report, err := channels.ResolveReport(report, channels.ResolveOptions{
		WorkspaceRoot: a.workspaceRoot,
		Limits:        a.attachmentLimits,
	})
	if err != nil {
		return err
	}
	if report.Empty() {
		return nil
	}

	attachments := report.Attachments
	chunks := channels.SplitText(report.Text, a.messageLimits.MaxChars)
	for i, chunk := range chunks {
		var files []*discordgo.File
		var closers []io.Closer
		if i == 0 && len(attachments) > 0 {
			files, closers, err = discordFiles(attachments)
			if err != nil {
				return err
			}
		}
		if err := sendDiscordMessage(ctx, a.session, event.ChannelID, chunk, files); err != nil {
			closeDiscordFiles(closers)
			return err
		}
		closeDiscordFiles(closers)
	}
	return nil
}

func discordFiles(attachments []domain.ReportAttachment) ([]*discordgo.File, []io.Closer, error) {
	if len(attachments) > discordOutboundAttachmentMaxFiles {
		return nil, nil, fmt.Errorf("discord supports at most %d attachments per message", discordOutboundAttachmentMaxFiles)
	}
	files := make([]*discordgo.File, 0, len(attachments))
	closers := make([]io.Closer, 0, len(attachments))
	for _, attachment := range attachments {
		file, err := os.Open(attachment.Path)
		if err != nil {
			closeDiscordFiles(closers)
			return nil, nil, err
		}
		files = append(files, &discordgo.File{
			Name:        safeAttachmentFilename("", firstNonEmpty(attachment.Filename, filepath.Base(attachment.Path))),
			ContentType: attachment.ContentType,
			Reader:      file,
		})
		closers = append(closers, file)
	}
	return files, closers, nil
}

func sendDiscordMessage(ctx context.Context, session *discordgo.Session, channelID, content string, files []*discordgo.File) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if strings.TrimSpace(content) == "" && len(files) == 0 {
		return nil
	}
	_, err := session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		Files:           files,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	return err
}

func closeDiscordFiles(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
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
