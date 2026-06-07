package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/opencto/opencto/internal/agent/prompts"
	"github.com/opencto/opencto/internal/channels"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
)

const telegramOutboundMessageMaxChars = 4096
const telegramAttachmentMaxBytes int64 = 20 << 20
const telegramOutboundAttachmentMaxFiles = 10
const telegramDefaultOutboundMaxFileBytes int64 = 50 << 20
const telegramDefaultOutboundMaxTotalBytes int64 = 50 << 20

const telegramAttachmentPayloadKey = "attachments"
const telegramTypingTimeout = 3 * time.Second
const telegramWebhookReadHeaderTimeout = 5 * time.Second
const telegramWebhookShutdownTimeout = 5 * time.Second
const telegramWebhookMaxBodyBytes int64 = 2 << 20
const telegramDefaultWebhookPath = "/telegram/webhook"
const telegramReferencedContentMaxChars = 2000

type AttachmentLimits = channels.AttachmentLimits
type MessageLimits = channels.MessageLimits

type WebhookOptions struct {
	URL                string
	ListenAddr         string
	Path               string
	SecretToken        string
	MaxConnections     int
	DropPendingUpdates bool
}

type Options struct {
	WorkspaceRoot    string
	Webhook          WebhookOptions
	MessageLimits    MessageLimits
	AttachmentLimits AttachmentLimits
}

type telegramBot interface {
	SetWebhookWithContext(ctx context.Context, url string, opts *gotgbot.SetWebhookOpts) (bool, error)
	GetFileWithContext(ctx context.Context, fileID string, opts *gotgbot.GetFileOpts) (*gotgbot.File, error)
	SendMessageWithContext(ctx context.Context, chatID int64, text string, opts *gotgbot.SendMessageOpts) (*gotgbot.Message, error)
	SendDocumentWithContext(ctx context.Context, chatID int64, document gotgbot.InputFileOrString, opts *gotgbot.SendDocumentOpts) (*gotgbot.Message, error)
	SendChatActionWithContext(ctx context.Context, chatID int64, action string, opts *gotgbot.SendChatActionOpts) (bool, error)
	FileURL(token string, filePath string, opts *gotgbot.RequestOpts) string
}

type Adapter struct {
	projectID        string
	dispatcher       *runtime.Dispatcher
	bot              telegramBot
	token            string
	logger           *slog.Logger
	attachmentDir    string
	httpClient       *http.Client
	workspaceRoot    string
	webhook          WebhookOptions
	messageLimits    MessageLimits
	attachmentLimits AttachmentLimits
	serverMu         sync.Mutex
	server           *http.Server
}

func New(projectID, token string, dispatcher *runtime.Dispatcher, logger *slog.Logger, opts ...Options) (*Adapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("telegram token is required")
	}
	bot, err := gotgbot.NewBot(token, &gotgbot.BotOpts{DisableTokenCheck: true})
	if err != nil {
		return nil, err
	}
	return newAdapter(projectID, token, bot, dispatcher, logger, opts...)
}

func newAdapter(projectID, token string, bot telegramBot, dispatcher *runtime.Dispatcher, logger *slog.Logger, opts ...Options) (*Adapter, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if bot == nil {
		return nil, fmt.Errorf("telegram bot is required")
	}
	options := defaultOptions()
	if len(opts) > 0 {
		options = opts[0]
		options.Webhook = normalizeWebhookOptions(options.Webhook)
		options.MessageLimits = normalizeMessageLimits(options.MessageLimits)
		options.AttachmentLimits = normalizeAttachmentLimits(options.AttachmentLimits)
	}
	attachmentDir, err := telegramAttachmentDir(projectID, options.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		projectID:        projectID,
		dispatcher:       dispatcher,
		bot:              bot,
		token:            strings.TrimSpace(token),
		logger:           logger,
		attachmentDir:    attachmentDir,
		httpClient:       &http.Client{Timeout: 30 * time.Second},
		workspaceRoot:    options.WorkspaceRoot,
		webhook:          options.Webhook,
		messageLimits:    options.MessageLimits,
		attachmentLimits: options.AttachmentLimits,
	}, nil
}

func telegramAttachmentDir(projectID, workspaceRoot string) (string, error) {
	parts := []string{"data", "attachments", safePathSegment(projectID), "telegram"}
	if root := strings.TrimSpace(workspaceRoot); root != "" {
		parts = append([]string{root}, parts...)
	}
	return filepath.Abs(filepath.Join(parts...))
}

func defaultOptions() Options {
	return Options{
		Webhook: normalizeWebhookOptions(WebhookOptions{}),
		MessageLimits: MessageLimits{
			MaxChars: telegramOutboundMessageMaxChars,
		},
		AttachmentLimits: AttachmentLimits{
			MaxFiles:      telegramOutboundAttachmentMaxFiles,
			MaxFileBytes:  telegramDefaultOutboundMaxFileBytes,
			MaxTotalBytes: telegramDefaultOutboundMaxTotalBytes,
		},
	}
}

func normalizeWebhookOptions(options WebhookOptions) WebhookOptions {
	options.URL = strings.TrimSpace(options.URL)
	options.ListenAddr = strings.TrimSpace(options.ListenAddr)
	options.Path = strings.TrimSpace(options.Path)
	if options.Path == "" {
		options.Path = telegramDefaultWebhookPath
	}
	if !strings.HasPrefix(options.Path, "/") {
		options.Path = "/" + options.Path
	}
	options.SecretToken = strings.TrimSpace(options.SecretToken)
	if options.MaxConnections == 0 {
		options.MaxConnections = 40
	}
	return options
}

func normalizeMessageLimits(limits MessageLimits) MessageLimits {
	if limits.MaxChars == 0 {
		limits.MaxChars = telegramOutboundMessageMaxChars
	}
	return limits
}

func normalizeAttachmentLimits(limits AttachmentLimits) AttachmentLimits {
	if limits.MaxFiles == 0 {
		limits.MaxFiles = telegramOutboundAttachmentMaxFiles
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = telegramDefaultOutboundMaxFileBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = telegramDefaultOutboundMaxTotalBytes
	}
	return limits
}

func (a *Adapter) Start(ctx context.Context) error {
	if a.dispatcher == nil {
		return nil
	}
	if strings.TrimSpace(a.webhook.URL) == "" {
		return fmt.Errorf("telegram webhook url is required")
	}
	if strings.TrimSpace(a.webhook.ListenAddr) == "" {
		return fmt.Errorf("telegram webhook listen address is required")
	}
	webhookURL, err := telegramWebhookURL(a.webhook.URL, a.webhook.Path)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc(a.webhook.Path, a.handleWebhook)
	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: telegramWebhookReadHeaderTimeout,
	}
	listener, err := net.Listen("tcp", a.webhook.ListenAddr)
	if err != nil {
		return err
	}

	a.serverMu.Lock()
	a.server = server
	a.serverMu.Unlock()

	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			a.log().Error("telegram webhook server failed", slog.String("error", serveErr.Error()))
		}
	}()

	if _, err := a.bot.SetWebhookWithContext(ctx, webhookURL, &gotgbot.SetWebhookOpts{
		AllowedUpdates:     []string{"message", "channel_post"},
		DropPendingUpdates: a.webhook.DropPendingUpdates,
		MaxConnections:     int64(a.webhook.MaxConnections),
		SecretToken:        a.webhook.SecretToken,
	}); err != nil {
		_ = server.Close()
		return fmt.Errorf("set telegram webhook: %w", err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), telegramWebhookShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	a.log().Info("telegram webhook started",
		slog.String("listen_addr", a.webhook.ListenAddr),
		slog.String("path", a.webhook.Path),
		slog.String("url", webhookURL),
	)
	return nil
}

func (a *Adapter) Close() error {
	a.serverMu.Lock()
	server := a.server
	a.server = nil
	a.serverMu.Unlock()
	if server == nil {
		return nil
	}
	if err := server.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func telegramWebhookURL(rawURL, path string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("telegram webhook url is required")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("telegram webhook url must be absolute")
	}
	if parsed.Scheme != "https" {
		return "", fmt.Errorf("telegram webhook url must use https")
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		parsed.Path = path
	}
	return parsed.String(), nil
}

func (a *Adapter) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if secret := strings.TrimSpace(a.webhook.SecretToken); secret != "" {
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != secret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
	}
	var update gotgbot.Update
	body := http.MaxBytesReader(w, r.Body, telegramWebhookMaxBodyBytes)
	defer body.Close()
	if err := json.NewDecoder(body).Decode(&update); err != nil {
		a.log().Warn("decode telegram webhook update", slog.String("error", err.Error()))
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if err := a.handleUpdate(r.Context(), update); err != nil {
		a.log().Error("handle telegram update", slog.String("error", err.Error()), slog.Int64("update_id", update.UpdateId))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (a *Adapter) handleUpdate(ctx context.Context, update gotgbot.Update) error {
	message := telegramUpdateMessage(update)
	if message == nil {
		return nil
	}
	if message.From != nil && message.From.IsBot {
		return nil
	}
	event, err := a.NormalizeMessage(ctx, update.UpdateId, message)
	if err != nil {
		return err
	}
	if telegramEventEmptyUserMessage(event) {
		a.log().Warn("ignore empty telegram message",
			slog.String("event_id", event.ID),
			slog.String("source_message_id", event.Provenance.SourceID),
			slog.String("channel_id", event.ChannelID),
			slog.String("thread_id", event.ThreadID),
		)
		return nil
	}
	if err := a.NotifyTyping(ctx, event); err != nil {
		a.log().Warn("notify telegram typing", slog.String("error", err.Error()), slog.String("event_id", event.ID))
	}
	if a.dispatcher == nil {
		return nil
	}
	return a.dispatcher.EnqueueEvent(ctx, event)
}

func telegramUpdateMessage(update gotgbot.Update) *gotgbot.Message {
	switch {
	case update.Message != nil:
		return update.Message
	case update.ChannelPost != nil:
		return update.ChannelPost
	default:
		return nil
	}
}

func (a *Adapter) NormalizeMessage(ctx context.Context, updateID int64, message *gotgbot.Message) (domain.Event, error) {
	if message == nil {
		return domain.Event{}, fmt.Errorf("telegram message is required")
	}

	now := time.Now().UTC()
	channelID := strconv.FormatInt(message.Chat.Id, 10)
	threadID := telegramThreadID(message)
	id := stableTelegramEventID(a.projectID, updateID, message.Chat.Id, message.MessageId)
	body := normalizeTelegramBody(firstNonEmpty(message.Text, message.Caption))
	attachments, err := a.normalizeAttachments(ctx, id, message, now)
	if err != nil {
		return domain.Event{}, err
	}
	body = bodyWithAttachmentFallback(body, attachments)

	payload := map[string]any{
		"update_id":  updateID,
		"message_id": message.MessageId,
		"chat_id":    channelID,
		"chat_type":  strings.TrimSpace(message.Chat.Type),
	}
	setTelegramPayloadString(payload, "chat_title", message.Chat.Title)
	setTelegramPayloadString(payload, "chat_username", message.Chat.Username)
	if threadID != "" {
		payload["message_thread_id"] = threadID
	}
	metadata := domain.Metadata{}
	addTelegramReplyMetadata(message, payload, metadata)
	if len(attachments) > 0 {
		payload[telegramAttachmentPayloadKey] = attachments
	}

	actorID, actorName := telegramActor(message)
	event := domain.Event{
		ID:          id,
		ProjectID:   a.projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   channelID,
		ChannelType: domain.ChannelTypeTelegram,
		ThreadID:    threadID,
		ActorID:     actorID,
		ActorName:   actorName,
		Body:        body,
		Payload:     payload,
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeTelegram),
			SourceID:   telegramMessageSourceID(message.Chat.Id, message.MessageId),
			Actor:      actorName,
			ObservedAt: now,
		},
		CreatedAt: now,
	}
	if len(metadata) > 0 {
		event.Metadata = metadata
		event.Provenance.Metadata = copyTelegramMetadata(metadata)
	}
	a.log().Info("telegram message normalized",
		slog.String("event_id", event.ID),
		slog.String("source_message_id", event.Provenance.SourceID),
		slog.String("channel_id", channelID),
		slog.String("thread_id", threadID),
		slog.String("reply_to_message_id", metadata[domain.MetadataKeyReplyToMessageID]),
		slog.String("body", truncateTelegramLogText(body, 180)),
	)
	return event, nil
}

func stableTelegramEventID(projectID string, updateID, chatID, messageID int64) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"telegram-event",
		strings.TrimSpace(projectID),
		strconv.FormatInt(updateID, 10),
		strconv.FormatInt(chatID, 10),
		strconv.FormatInt(messageID, 10),
	}, "\x00")))
	return fmt.Sprintf("%x", sum)
}

func telegramThreadID(message *gotgbot.Message) string {
	if message == nil || message.MessageThreadId == 0 {
		return ""
	}
	return strconv.FormatInt(message.MessageThreadId, 10)
}

func telegramActor(message *gotgbot.Message) (string, string) {
	if message == nil {
		return "", ""
	}
	if message.From != nil {
		id := strconv.FormatInt(message.From.Id, 10)
		return id, telegramUserName(*message.From)
	}
	if message.SenderChat != nil {
		id := strconv.FormatInt(message.SenderChat.Id, 10)
		return id, telegramChatName(*message.SenderChat)
	}
	return "", telegramChatName(message.Chat)
}

func telegramUserName(user gotgbot.User) string {
	name := strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	if name != "" {
		return name
	}
	if strings.TrimSpace(user.Username) != "" {
		return "@" + strings.TrimSpace(user.Username)
	}
	return strconv.FormatInt(user.Id, 10)
}

func telegramChatName(chat gotgbot.Chat) string {
	if strings.TrimSpace(chat.Title) != "" {
		return strings.TrimSpace(chat.Title)
	}
	name := strings.TrimSpace(strings.Join([]string{chat.FirstName, chat.LastName}, " "))
	if name != "" {
		return name
	}
	if strings.TrimSpace(chat.Username) != "" {
		return "@" + strings.TrimSpace(chat.Username)
	}
	if chat.Id != 0 {
		return strconv.FormatInt(chat.Id, 10)
	}
	return ""
}

func addTelegramReplyMetadata(message *gotgbot.Message, payload map[string]any, metadata domain.Metadata) {
	if message == nil || message.ReplyToMessage == nil {
		return
	}
	referenced := message.ReplyToMessage
	replyChatID := referenced.Chat.Id
	if replyChatID == 0 {
		replyChatID = message.Chat.Id
	}
	replyChannelID := strconv.FormatInt(replyChatID, 10)
	replyMessageID := telegramMessageSourceID(replyChatID, referenced.MessageId)
	setTelegramPayloadString(payload, domain.MetadataKeyReplyToMessageID, replyMessageID)
	setTelegramPayloadString(payload, domain.MetadataKeyReplyToChannelID, replyChannelID)
	setTelegramPayloadString(payload, domain.MetadataKeyReplyToContextID, replyChannelID)
	setTelegramMetadataString(metadata, domain.MetadataKeyReplyToMessageID, replyMessageID)
	setTelegramMetadataString(metadata, domain.MetadataKeyReplyToChannelID, replyChannelID)
	setTelegramMetadataString(metadata, domain.MetadataKeyReplyToContextID, replyChannelID)
	if referenced.From != nil {
		replyActorID := strconv.FormatInt(referenced.From.Id, 10)
		setTelegramPayloadString(payload, domain.MetadataKeyReplyToActorID, replyActorID)
		setTelegramPayloadString(payload, "reply_to_author_name", telegramUserName(*referenced.From))
		setTelegramMetadataString(metadata, domain.MetadataKeyReplyToActorID, replyActorID)
	}
	if content := truncateTelegramMetadataText(normalizeTelegramBody(firstNonEmpty(referenced.Text, referenced.Caption)), telegramReferencedContentMaxChars); content != "" {
		payload["reply_to_content"] = content
	}
}

func telegramEventEmptyUserMessage(event domain.Event) bool {
	if strings.TrimSpace(event.Body) != "" {
		return false
	}
	attachments, ok := event.Payload[telegramAttachmentPayloadKey]
	if !ok || attachments == nil {
		return true
	}
	switch value := attachments.(type) {
	case []domain.EventAttachment:
		return len(value) == 0
	default:
		return false
	}
}

type telegramAttachmentCandidate struct {
	Kind        string
	FileID      string
	UniqueID    string
	Filename    string
	ContentType string
	SizeBytes   int64
	Metadata    domain.Metadata
}

func (a *Adapter) normalizeAttachments(ctx context.Context, eventID string, message *gotgbot.Message, observedAt time.Time) ([]domain.EventAttachment, error) {
	candidates := telegramAttachmentCandidates(message)
	if len(candidates) == 0 {
		return nil, nil
	}
	out := make([]domain.EventAttachment, 0, len(candidates))
	for _, candidate := range candidates {
		id, err := domain.NewID()
		if err != nil {
			return nil, err
		}
		metadata := domain.Metadata{
			"kind":           candidate.Kind,
			"file_unique_id": candidate.UniqueID,
		}
		for key, value := range candidate.Metadata {
			setTelegramMetadataString(metadata, key, value)
		}
		item := domain.EventAttachment{
			ID:          id,
			ProjectID:   a.projectID,
			EventID:     eventID,
			Source:      string(domain.ChannelTypeTelegram),
			SourceID:    candidate.FileID,
			Filename:    candidate.Filename,
			ContentType: candidate.ContentType,
			SizeBytes:   candidate.SizeBytes,
			Metadata:    metadata,
			CreatedAt:   observedAt,
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

func telegramAttachmentCandidates(message *gotgbot.Message) []telegramAttachmentCandidate {
	if message == nil {
		return nil
	}
	var out []telegramAttachmentCandidate
	if photo := largestPhoto(message.Photo); photo != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "photo",
			FileID:      photo.FileId,
			UniqueID:    photo.FileUniqueId,
			Filename:    "photo.jpg",
			ContentType: "image/jpeg",
			SizeBytes:   photo.FileSize,
			Metadata: domain.Metadata{
				"width":  strconv.FormatInt(photo.Width, 10),
				"height": strconv.FormatInt(photo.Height, 10),
			},
		})
	}
	if document := message.Document; document != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "document",
			FileID:      document.FileId,
			UniqueID:    document.FileUniqueId,
			Filename:    firstNonEmpty(document.FileName, "document"),
			ContentType: document.MimeType,
			SizeBytes:   document.FileSize,
		})
	}
	if audio := message.Audio; audio != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "audio",
			FileID:      audio.FileId,
			UniqueID:    audio.FileUniqueId,
			Filename:    firstNonEmpty(audio.FileName, "audio"),
			ContentType: audio.MimeType,
			SizeBytes:   audio.FileSize,
		})
	}
	if voice := message.Voice; voice != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "voice",
			FileID:      voice.FileId,
			UniqueID:    voice.FileUniqueId,
			Filename:    "voice.oga",
			ContentType: firstNonEmpty(voice.MimeType, "audio/ogg"),
			SizeBytes:   voice.FileSize,
		})
	}
	if video := message.Video; video != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "video",
			FileID:      video.FileId,
			UniqueID:    video.FileUniqueId,
			Filename:    firstNonEmpty(video.FileName, "video.mp4"),
			ContentType: video.MimeType,
			SizeBytes:   video.FileSize,
			Metadata: domain.Metadata{
				"width":         strconv.FormatInt(video.Width, 10),
				"height":        strconv.FormatInt(video.Height, 10),
				"duration_secs": strconv.FormatInt(video.Duration, 10),
			},
		})
	}
	if videoNote := message.VideoNote; videoNote != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "video_note",
			FileID:      videoNote.FileId,
			UniqueID:    videoNote.FileUniqueId,
			Filename:    "video-note.mp4",
			ContentType: "video/mp4",
			SizeBytes:   videoNote.FileSize,
			Metadata: domain.Metadata{
				"length":        strconv.FormatInt(videoNote.Length, 10),
				"duration_secs": strconv.FormatInt(videoNote.Duration, 10),
			},
		})
	}
	if animation := message.Animation; animation != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "animation",
			FileID:      animation.FileId,
			UniqueID:    animation.FileUniqueId,
			Filename:    firstNonEmpty(animation.FileName, "animation"),
			ContentType: animation.MimeType,
			SizeBytes:   animation.FileSize,
			Metadata: domain.Metadata{
				"width":         strconv.FormatInt(animation.Width, 10),
				"height":        strconv.FormatInt(animation.Height, 10),
				"duration_secs": strconv.FormatInt(animation.Duration, 10),
			},
		})
	}
	if sticker := message.Sticker; sticker != nil {
		out = append(out, telegramAttachmentCandidate{
			Kind:        "sticker",
			FileID:      sticker.FileId,
			UniqueID:    sticker.FileUniqueId,
			Filename:    telegramStickerFilename(*sticker),
			ContentType: telegramStickerContentType(*sticker),
			SizeBytes:   sticker.FileSize,
			Metadata: domain.Metadata{
				"width":       strconv.FormatInt(sticker.Width, 10),
				"height":      strconv.FormatInt(sticker.Height, 10),
				"emoji":       sticker.Emoji,
				"sticker_set": sticker.SetName,
			},
		})
	}
	return out
}

func largestPhoto(photos []gotgbot.PhotoSize) *gotgbot.PhotoSize {
	if len(photos) == 0 {
		return nil
	}
	best := photos[0]
	bestScore := photoScore(best)
	for _, photo := range photos[1:] {
		if score := photoScore(photo); score > bestScore {
			best = photo
			bestScore = score
		}
	}
	return &best
}

func photoScore(photo gotgbot.PhotoSize) int64 {
	if photo.FileSize > 0 {
		return photo.FileSize
	}
	return photo.Width * photo.Height
}

func telegramStickerFilename(sticker gotgbot.Sticker) string {
	switch {
	case sticker.IsVideo:
		return "sticker.webm"
	case sticker.IsAnimated:
		return "sticker.tgs"
	default:
		return "sticker.webp"
	}
}

func telegramStickerContentType(sticker gotgbot.Sticker) string {
	switch {
	case sticker.IsVideo:
		return "video/webm"
	case sticker.IsAnimated:
		return "application/x-tgsticker"
	default:
		return "image/webp"
	}
}

func (a *Adapter) downloadAttachment(ctx context.Context, eventID string, attachment domain.EventAttachment) (string, int64, string, error) {
	if attachment.SourceID == "" {
		return "", 0, "", fmt.Errorf("telegram file id is empty")
	}
	if attachment.SizeBytes > telegramAttachmentMaxBytes {
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", telegramAttachmentMaxBytes)
	}
	file, err := a.bot.GetFileWithContext(ctx, attachment.SourceID, nil)
	if err != nil {
		return "", 0, "", err
	}
	if file == nil || strings.TrimSpace(file.FilePath) == "" {
		return "", 0, "", fmt.Errorf("telegram file path is empty")
	}
	if file.FileSize > telegramAttachmentMaxBytes {
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", telegramAttachmentMaxBytes)
	}
	downloadURL := a.bot.FileURL(a.token, file.FilePath, nil)
	client := a.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
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
	if resp.ContentLength > telegramAttachmentMaxBytes {
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", telegramAttachmentMaxBytes)
	}

	dir := filepath.Join(a.attachmentDir, safePathSegment(eventID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", 0, "", err
	}
	name := safeAttachmentFilename(attachment.SourceID, attachment.Filename, attachment.ContentType)
	finalPath := filepath.Join(dir, name)
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return "", 0, "", err
	}
	tmpPath := tmp.Name()
	copied, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, telegramAttachmentMaxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpPath)
		return "", 0, "", closeErr
	}
	if copied > telegramAttachmentMaxBytes {
		_ = os.Remove(tmpPath)
		return "", 0, "", fmt.Errorf("attachment exceeds %d byte limit", telegramAttachmentMaxBytes)
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

func (a *Adapter) Report(ctx context.Context, event domain.Event, report domain.ReportMessage) ([]domain.ReportReceipt, error) {
	chatID, ok, err := telegramOutboundChatID(event)
	if err != nil {
		return nil, err
	}
	if a.bot == nil || !ok {
		return nil, nil
	}
	report, err = channels.ResolveReport(report, channels.ResolveOptions{
		WorkspaceRoot: a.workspaceRoot,
		Limits:        a.attachmentLimits,
	})
	if err != nil {
		return nil, err
	}
	if report.Empty() {
		return nil, nil
	}

	messageOpts, err := telegramSendMessageOptions(event, report)
	if err != nil {
		return nil, err
	}
	documentOpts, err := telegramSendDocumentOptions(event, report)
	if err != nil {
		return nil, err
	}

	receipts := []domain.ReportReceipt{}
	for _, chunk := range channels.SplitText(report.Text, a.messageLimits.MaxChars) {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		sent, err := a.bot.SendMessageWithContext(ctx, chatID, chunk, messageOpts)
		if err != nil {
			return nil, err
		}
		if receipt := telegramReportReceipt(event, sent); receipt.MessageID != "" || receipt.ThreadID != "" {
			receipt.Body = chunk
			receipts = append(receipts, receipt)
		}
	}
	for _, attachment := range report.Attachments {
		sent, err := a.sendDocument(ctx, chatID, attachment, documentOpts)
		if err != nil {
			return nil, err
		}
		if receipt := telegramReportReceipt(event, sent); receipt.MessageID != "" || receipt.ThreadID != "" {
			receipts = append(receipts, receipt)
		}
	}
	return receipts, nil
}

func (a *Adapter) sendDocument(ctx context.Context, chatID int64, attachment domain.ReportAttachment, opts *gotgbot.SendDocumentOpts) (sent *gotgbot.Message, err error) {
	file, err := os.Open(attachment.Path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()

	return a.bot.SendDocumentWithContext(ctx, chatID, &gotgbot.FileReader{
		Name: firstNonEmpty(attachment.Filename, filepath.Base(attachment.Path)),
		Data: file,
	}, opts)
}

func telegramOutboundChatID(event domain.Event) (int64, bool, error) {
	channelID := strings.TrimSpace(event.ChannelID)
	if channelID == "" {
		return 0, false, nil
	}
	chatID, err := strconv.ParseInt(channelID, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("telegram channel_id must be a numeric chat id: %w", err)
	}
	return chatID, true, nil
}

func telegramSendMessageOptions(event domain.Event, report domain.ReportMessage) (*gotgbot.SendMessageOpts, error) {
	threadID, err := telegramOutboundThreadID(event)
	if err != nil {
		return nil, err
	}
	reply, err := telegramReplyParameters(report)
	if err != nil {
		return nil, err
	}
	if threadID == 0 && reply == nil {
		return nil, nil
	}
	return &gotgbot.SendMessageOpts{
		MessageThreadId: threadID,
		ReplyParameters: reply,
	}, nil
}

func telegramSendDocumentOptions(event domain.Event, report domain.ReportMessage) (*gotgbot.SendDocumentOpts, error) {
	threadID, err := telegramOutboundThreadID(event)
	if err != nil {
		return nil, err
	}
	reply, err := telegramReplyParameters(report)
	if err != nil {
		return nil, err
	}
	if threadID == 0 && reply == nil {
		return nil, nil
	}
	return &gotgbot.SendDocumentOpts{
		MessageThreadId: threadID,
		ReplyParameters: reply,
	}, nil
}

func telegramOutboundThreadID(event domain.Event) (int64, error) {
	threadID := strings.TrimSpace(event.ThreadID)
	if threadID == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(threadID, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("telegram thread_id must be a numeric message_thread_id: %w", err)
	}
	return value, nil
}

func telegramReplyParameters(report domain.ReportMessage) (*gotgbot.ReplyParameters, error) {
	if report.ReplyTo == nil || report.ReplyTo.Empty() {
		return nil, nil
	}
	messageIDText := strings.TrimSpace(report.ReplyTo.MessageID)
	if messageIDText == "" {
		return nil, nil
	}
	_, messageIDText = splitTelegramMessageSourceID(messageIDText)
	messageID, err := strconv.ParseInt(messageIDText, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("telegram reply_to.message_id must be numeric: %w", err)
	}
	reply := &gotgbot.ReplyParameters{
		MessageId:                messageID,
		AllowSendingWithoutReply: true,
	}
	if channelID := strings.TrimSpace(report.ReplyTo.ChannelID); channelID != "" {
		chatID, err := strconv.ParseInt(channelID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("telegram reply_to.channel_id must be numeric: %w", err)
		}
		reply.ChatId = chatID
	}
	return reply, nil
}

func telegramReportReceipt(event domain.Event, sent *gotgbot.Message) domain.ReportReceipt {
	receipt := domain.ReportReceipt{
		ChannelID: strings.TrimSpace(event.ChannelID),
		ContextID: strings.TrimSpace(event.ChannelID),
		ThreadID:  strings.TrimSpace(event.ThreadID),
	}
	if sent != nil {
		chatID := strings.TrimSpace(receipt.ChannelID)
		if receipt.ChannelID == "" && sent.Chat.Id != 0 {
			chatID = strconv.FormatInt(sent.Chat.Id, 10)
			receipt.ChannelID = chatID
		}
		if sent.MessageId != 0 {
			if chatID != "" {
				receipt.MessageID = chatID + ":" + strconv.FormatInt(sent.MessageId, 10)
			} else {
				receipt.MessageID = strconv.FormatInt(sent.MessageId, 10)
			}
		}
		if sent.MessageThreadId != 0 {
			receipt.ThreadID = strconv.FormatInt(sent.MessageThreadId, 10)
		}
	}
	return receipt
}

func (a *Adapter) NotifyTyping(ctx context.Context, event domain.Event) error {
	chatID, ok, err := telegramOutboundChatID(event)
	if err != nil {
		return err
	}
	if a.bot == nil || !ok {
		return nil
	}
	threadID, err := telegramOutboundThreadID(event)
	if err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	typingCtx, cancel := context.WithTimeout(ctx, telegramTypingTimeout)
	defer cancel()
	_, err = a.bot.SendChatActionWithContext(typingCtx, chatID, "typing", &gotgbot.SendChatActionOpts{
		MessageThreadId: threadID,
	})
	return err
}

func telegramMessageSourceID(chatID, messageID int64) string {
	return strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(messageID, 10)
}

func splitTelegramMessageSourceID(value string) (string, string) {
	value = strings.TrimSpace(value)
	if chatID, messageID, ok := strings.Cut(value, ":"); ok {
		return strings.TrimSpace(chatID), strings.TrimSpace(messageID)
	}
	return "", value
}

func normalizeTelegramBody(content string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(content), " "))
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
	return prompts.MustRender("uploaded_attachments.tmpl", map[string]any{
		"Names": strings.Join(names, ", "),
	})
}

func safeAttachmentFilename(sourceID, filename, contentType string) string {
	filename = safePathSegment(filename)
	if filename == "" {
		filename = "attachment" + extensionForContentType(contentType)
	}
	sourceID = safePathSegment(sourceID)
	if sourceID == "" {
		return filename
	}
	return sourceID + "-" + filename
}

func extensionForContentType(contentType string) string {
	extensions, err := mime.ExtensionsByType(strings.TrimSpace(contentType))
	if err != nil || len(extensions) == 0 {
		return ""
	}
	return extensions[0]
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

func (a *Adapter) log() *slog.Logger {
	if a != nil && a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

func truncateTelegramLogText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit])) + "..."
}

func truncateTelegramMetadataText(text string, limit int) string {
	text = strings.TrimSpace(text)
	if text == "" || limit <= 0 || len(text) <= limit {
		return text
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return strings.TrimSpace(string(runes[:limit]))
}

func setTelegramPayloadString(payload map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	payload[key] = value
}

func setTelegramMetadataString(metadata domain.Metadata, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	metadata[key] = value
}

func copyTelegramMetadata(metadata domain.Metadata) domain.Metadata {
	if len(metadata) == 0 {
		return nil
	}
	out := make(domain.Metadata, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}
