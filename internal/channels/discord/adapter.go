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
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/agent/prompts"
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
const discordTypingTimeout = 3 * time.Second
const discordReferencedContentMaxChars = 2000
const discordResetCommandName = "new"
const discordResetCommandDescription = "Start a new OpenCTO conversation"

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
	channelMu        sync.Mutex
	threadChannels   map[string]string
	threadParents    map[string]string
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
		threadChannels:   map[string]string{},
		threadParents:    map[string]string{},
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
	a.session.AddHandler(func(session *discordgo.Session, interaction *discordgo.InteractionCreate) {
		a.handleInteractionCreate(ctx, session, interaction)
	})
	a.session.AddHandler(func(session *discordgo.Session, message *discordgo.MessageCreate) {
		if message.Author == nil || message.Author.Bot {
			return
		}
		event, err := a.NormalizeMessage(ctx, message)
		if err != nil {
			a.logger.Error("normalize discord message", slog.String("error", err.Error()))
			return
		}
		if discordEventEmptyUserMessage(event) {
			a.logger.Warn("ignore empty discord message",
				slog.String("event_id", event.ID),
				slog.String("source_message_id", event.Provenance.SourceID),
				slog.String("channel_id", event.ChannelID),
				slog.String("thread_id", event.ThreadID),
			)
			return
		}
		if !domain.IsConversationResetCommand(event.Body) {
			if err := a.NotifyTyping(ctx, event); err != nil {
				a.logger.Warn("notify discord typing", slog.String("error", err.Error()), slog.String("event_id", event.ID))
			}
		}
		if err := a.dispatcher.EnqueueEvent(ctx, event); err != nil {
			a.logger.Error("enqueue discord event", slog.String("error", err.Error()), slog.String("event_id", event.ID))
		}
	})
	if err := a.session.Open(); err != nil {
		return err
	}
	if err := a.registerApplicationCommands(ctx); err != nil {
		a.logger.Warn("register discord application commands", slog.String("error", err.Error()))
	}
	return nil
}

func (a *Adapter) handleInteractionCreate(ctx context.Context, session *discordgo.Session, interaction *discordgo.InteractionCreate) {
	if interaction == nil || interaction.Interaction == nil || interaction.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := interaction.ApplicationCommandData()
	if strings.TrimSpace(data.Name) != discordResetCommandName {
		return
	}
	event, err := a.resetEventFromInteraction(ctx, session, interaction)
	if err != nil {
		a.logger.Error("normalize discord reset interaction", slog.String("error", err.Error()))
		_ = respondDiscordInteraction(session, interaction.Interaction, "I couldn't start a new conversation.")
		return
	}
	if a.dispatcher != nil {
		if err := a.dispatcher.EnqueueEvent(ctx, event); err != nil {
			a.logger.Error("enqueue discord reset interaction", slog.String("error", err.Error()), slog.String("event_id", event.ID))
			_ = respondDiscordInteraction(session, interaction.Interaction, "I couldn't start a new conversation.")
			return
		}
	}
	if err := respondDiscordInteraction(session, interaction.Interaction, "Started a new conversation."); err != nil {
		a.logger.Warn("respond discord reset interaction", slog.String("error", err.Error()), slog.String("event_id", event.ID))
	}
}

func respondDiscordInteraction(session *discordgo.Session, interaction *discordgo.Interaction, content string) error {
	if session == nil || interaction == nil {
		return nil
	}
	return session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Content: strings.TrimSpace(content),
		},
	})
}

func (a *Adapter) resetEventFromInteraction(ctx context.Context, session *discordgo.Session, interaction *discordgo.InteractionCreate) (domain.Event, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.Event{}, err
	}
	now := time.Now().UTC()
	channelID := strings.TrimSpace(interaction.ChannelID)
	threadID, parentID := a.discordChannelThreadContext(ctx, session, channelID)
	if threadID != "" && parentID != "" {
		channelID = parentID
	}
	actorID, actorName := discordInteractionActor(interaction)
	return domain.Event{
		ID:          id,
		ProjectID:   a.projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   channelID,
		ChannelType: domain.ChannelTypeDiscord,
		ThreadID:    threadID,
		ActorID:     actorID,
		ActorName:   actorName,
		Body:        domain.ConversationResetSlashCommand,
		Metadata: domain.Metadata{
			domain.MetadataKeyCommandResponseAcknowledged: "true",
		},
		Payload: map[string]any{
			"interaction_id": interaction.ID,
			"guild_id":       interaction.GuildID,
			"channel_id":     interaction.ChannelID,
		},
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeDiscord),
			SourceID:   interaction.ID,
			Actor:      actorName,
			ObservedAt: now,
		},
		CreatedAt: now,
	}, nil
}

func discordInteractionActor(interaction *discordgo.InteractionCreate) (string, string) {
	if interaction == nil || interaction.Interaction == nil {
		return "", ""
	}
	if interaction.Member != nil && interaction.Member.User != nil {
		return strings.TrimSpace(interaction.Member.User.ID), strings.TrimSpace(interaction.Member.User.Username)
	}
	if interaction.User != nil {
		return strings.TrimSpace(interaction.User.ID), strings.TrimSpace(interaction.User.Username)
	}
	return "", ""
}

func (a *Adapter) registerApplicationCommands(ctx context.Context) error {
	if a == nil || a.session == nil {
		return nil
	}
	appID, err := a.discordApplicationID(ctx)
	if err != nil {
		return err
	}
	command := &discordgo.ApplicationCommand{
		Name:        discordResetCommandName,
		Description: discordResetCommandDescription,
		Type:        discordgo.ChatApplicationCommand,
	}
	commands, err := a.session.ApplicationCommands(appID, "", discordgo.WithContext(ctx))
	if err != nil {
		return err
	}
	for _, existing := range commands {
		if existing == nil || existing.Name != command.Name || existing.Type != command.Type {
			continue
		}
		if existing.Description == command.Description {
			return nil
		}
		_, err := a.session.ApplicationCommandEdit(appID, "", existing.ID, command, discordgo.WithContext(ctx))
		return err
	}
	_, err = a.session.ApplicationCommandCreate(appID, "", command, discordgo.WithContext(ctx))
	return err
}

func (a *Adapter) discordApplicationID(ctx context.Context) (string, error) {
	if a.session.State != nil && a.session.State.User != nil {
		if id := strings.TrimSpace(a.session.State.User.ID); id != "" {
			return id, nil
		}
	}
	user, err := a.session.User("@me", discordgo.WithContext(ctx))
	if err != nil {
		return "", err
	}
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return "", fmt.Errorf("discord application id is unavailable")
	}
	return strings.TrimSpace(user.ID), nil
}

func (a *Adapter) discordChannelThreadContext(ctx context.Context, session *discordgo.Session, channelID string) (string, string) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", ""
	}
	a.channelMu.Lock()
	if a.threadChannels != nil {
		if threadID, ok := a.threadChannels[channelID]; ok {
			parentID := ""
			if a.threadParents != nil {
				parentID = a.threadParents[channelID]
			}
			a.channelMu.Unlock()
			return threadID, parentID
		}
	}
	a.channelMu.Unlock()
	if session == nil {
		return "", ""
	}
	if session.State != nil {
		if channel, err := session.State.Channel(channelID); err == nil && channel != nil {
			return a.cacheAndReturnDiscordChannelThreadContext(channelID, channel)
		}
	}
	channel, err := session.Channel(channelID, discordgo.WithContext(ctx))
	if err != nil {
		a.log().Warn("discord interaction channel lookup failed",
			slog.String("channel_id", channelID),
			slog.String("error", err.Error()),
		)
		return "", ""
	}
	return a.cacheAndReturnDiscordChannelThreadContext(channelID, channel)
}

func (a *Adapter) cacheAndReturnDiscordChannelThreadContext(channelID string, channel *discordgo.Channel) (string, string) {
	threadID := ""
	parentID := ""
	if channel != nil && channel.IsThread() {
		threadID = strings.TrimSpace(channel.ID)
		parentID = strings.TrimSpace(channel.ParentID)
	}
	a.cacheDiscordThreadContext(channelID, threadID, parentID)
	return threadID, parentID
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
	a.hydrateEmptyDiscordMessage(ctx, message)
	body := normalizeDiscordBody(message.Content)
	threadID, parentChannelID := a.discordThreadContext(message)
	channelID := strings.TrimSpace(message.ChannelID)
	if threadID != "" && parentChannelID != "" {
		channelID = parentChannelID
	}
	attachments, err := a.normalizeAttachments(ctx, id, message.Attachments, now)
	if err != nil {
		return domain.Event{}, err
	}
	body = bodyWithAttachmentFallback(body, attachments)
	payload := map[string]any{
		"message_id": message.ID,
		"guild_id":   message.GuildID,
	}
	metadata := domain.Metadata{}
	addDiscordReplyMetadata(message, payload, metadata)
	if len(attachments) > 0 {
		payload[discordAttachmentPayloadKey] = attachments
	}
	event := domain.Event{
		ID:          id,
		ProjectID:   a.projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   channelID,
		ChannelType: domain.ChannelTypeDiscord,
		ThreadID:    threadID,
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
	}
	if len(metadata) > 0 {
		event.Metadata = metadata
		event.Provenance.Metadata = copyDiscordMetadata(metadata)
	}
	a.log().Info("discord message normalized",
		slog.String("event_id", event.ID),
		slog.String("source_message_id", message.ID),
		slog.String("channel_id", channelID),
		slog.String("guild_id", message.GuildID),
		slog.String("thread_id", threadID),
		slog.String("reply_to_message_id", metadata[domain.MetadataKeyReplyToMessageID]),
		slog.String("body", truncateDiscordLogText(body, 180)),
	)
	return event, nil
}

func discordEventEmptyUserMessage(event domain.Event) bool {
	if strings.TrimSpace(event.Body) != "" {
		return false
	}
	attachments, ok := event.Payload[discordAttachmentPayloadKey]
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

func (a *Adapter) hydrateEmptyDiscordMessage(ctx context.Context, message *discordgo.MessageCreate) {
	if a == nil || a.session == nil || message == nil || message.Message == nil {
		return
	}
	if strings.TrimSpace(message.Content) != "" || len(message.Attachments) > 0 {
		return
	}
	channelID := strings.TrimSpace(message.ChannelID)
	messageID := strings.TrimSpace(message.ID)
	if channelID == "" || messageID == "" {
		return
	}
	fetched, err := a.session.ChannelMessage(channelID, messageID, discordgo.WithContext(ctx))
	if err != nil {
		a.log().Warn("hydrate empty discord message failed",
			slog.String("channel_id", channelID),
			slog.String("message_id", messageID),
			slog.String("error", err.Error()),
		)
		return
	}
	if fetched == nil {
		return
	}
	if strings.TrimSpace(message.Content) == "" && strings.TrimSpace(fetched.Content) != "" {
		message.Content = fetched.Content
	}
	if len(message.Attachments) == 0 && len(fetched.Attachments) > 0 {
		message.Attachments = fetched.Attachments
	}
	if message.MessageReference == nil && fetched.MessageReference != nil {
		message.MessageReference = fetched.MessageReference
	}
	if message.ReferencedMessage == nil && fetched.ReferencedMessage != nil {
		message.ReferencedMessage = fetched.ReferencedMessage
	}
	if message.GuildID == "" && fetched.GuildID != "" {
		message.GuildID = fetched.GuildID
	}
	if message.Author == nil && fetched.Author != nil {
		message.Author = fetched.Author
	}
}

func addDiscordReplyMetadata(message *discordgo.MessageCreate, payload map[string]any, metadata domain.Metadata) {
	if message == nil || message.Message == nil {
		return
	}
	if ref := message.MessageReference; ref != nil {
		setDiscordPayloadString(payload, domain.MetadataKeyReplyToMessageID, ref.MessageID)
		setDiscordPayloadString(payload, domain.MetadataKeyReplyToChannelID, ref.ChannelID)
		setDiscordPayloadString(payload, domain.MetadataKeyReplyToContextID, ref.GuildID)
		setDiscordMetadataString(metadata, domain.MetadataKeyReplyToMessageID, ref.MessageID)
		setDiscordMetadataString(metadata, domain.MetadataKeyReplyToChannelID, ref.ChannelID)
		setDiscordMetadataString(metadata, domain.MetadataKeyReplyToContextID, ref.GuildID)
	}
	referenced := message.ReferencedMessage
	if referenced == nil {
		return
	}
	setDiscordPayloadString(payload, domain.MetadataKeyReplyToMessageID, referenced.ID)
	setDiscordPayloadString(payload, domain.MetadataKeyReplyToChannelID, referenced.ChannelID)
	setDiscordPayloadString(payload, domain.MetadataKeyReplyToContextID, referenced.GuildID)
	setDiscordMetadataString(metadata, domain.MetadataKeyReplyToMessageID, referenced.ID)
	setDiscordMetadataString(metadata, domain.MetadataKeyReplyToChannelID, referenced.ChannelID)
	setDiscordMetadataString(metadata, domain.MetadataKeyReplyToContextID, referenced.GuildID)
	if referenced.Author != nil {
		setDiscordPayloadString(payload, domain.MetadataKeyReplyToActorID, referenced.Author.ID)
		setDiscordPayloadString(payload, "reply_to_author_name", referenced.Author.Username)
		setDiscordMetadataString(metadata, domain.MetadataKeyReplyToActorID, referenced.Author.ID)
	}
	if content := truncateDiscordMetadataText(normalizeDiscordBody(referenced.Content), discordReferencedContentMaxChars); content != "" {
		payload["reply_to_content"] = content
	}
}

func (a *Adapter) discordThreadID(message *discordgo.MessageCreate) string {
	threadID, _ := a.discordThreadContext(message)
	return threadID
}

func (a *Adapter) discordThreadContext(message *discordgo.MessageCreate) (string, string) {
	if message == nil || message.Message == nil {
		return "", ""
	}
	if message.Thread != nil && message.Thread.IsThread() {
		a.log().Info("discord thread detected from message thread",
			slog.String("channel_id", message.ChannelID),
			slog.String("thread_id", strings.TrimSpace(message.Thread.ID)),
		)
		return strings.TrimSpace(message.Thread.ID), strings.TrimSpace(message.Thread.ParentID)
	}
	channelID := strings.TrimSpace(message.ChannelID)
	if channelID == "" || a == nil || a.session == nil {
		return "", ""
	}
	a.channelMu.Lock()
	if a.threadChannels != nil {
		if threadID, ok := a.threadChannels[channelID]; ok {
			parentID := ""
			if a.threadParents != nil {
				parentID = a.threadParents[channelID]
			}
			a.channelMu.Unlock()
			a.log().Info("discord thread cache hit",
				slog.String("channel_id", channelID),
				slog.String("thread_id", threadID),
			)
			return threadID, parentID
		}
	}
	a.channelMu.Unlock()

	if a.session.State != nil {
		if channel, err := a.session.State.Channel(channelID); err == nil && channel != nil {
			threadID := ""
			parentID := ""
			if channel.IsThread() {
				threadID = strings.TrimSpace(channel.ID)
				parentID = strings.TrimSpace(channel.ParentID)
			}
			a.cacheDiscordThreadContext(channelID, threadID, parentID)
			a.log().Info("discord thread detected from state",
				slog.String("channel_id", channelID),
				slog.String("thread_id", threadID),
			)
			return threadID, parentID
		}
	}

	channel, err := a.session.Channel(channelID)
	threadID := ""
	parentID := ""
	if err == nil && channel != nil && channel.IsThread() {
		threadID = strings.TrimSpace(channel.ID)
		parentID = strings.TrimSpace(channel.ParentID)
	}
	if err != nil {
		a.log().Warn("discord thread lookup failed",
			slog.String("channel_id", channelID),
			slog.String("error", err.Error()),
		)
		return "", ""
	}

	a.cacheDiscordThreadContext(channelID, threadID, parentID)
	a.log().Info("discord thread detected from rest",
		slog.String("channel_id", channelID),
		slog.String("thread_id", threadID),
	)
	return threadID, parentID
}

func (a *Adapter) cacheDiscordThreadContext(channelID, threadID, parentID string) {
	a.channelMu.Lock()
	defer a.channelMu.Unlock()
	if a.threadChannels == nil {
		a.threadChannels = map[string]string{}
	}
	if a.threadParents == nil {
		a.threadParents = map[string]string{}
	}
	a.threadChannels[channelID] = threadID
	a.threadParents[channelID] = parentID
}

func (a *Adapter) log() *slog.Logger {
	if a != nil && a.logger != nil {
		return a.logger
	}
	return slog.Default()
}

func truncateDiscordLogText(text string, limit int) string {
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

func setDiscordPayloadString(payload map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	payload[key] = value
}

func setDiscordMetadataString(metadata domain.Metadata, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	metadata[key] = value
}

func truncateDiscordMetadataText(text string, limit int) string {
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

func copyDiscordMetadata(metadata domain.Metadata) domain.Metadata {
	if len(metadata) == 0 {
		return nil
	}
	out := make(domain.Metadata, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
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
	return prompts.MustRender("uploaded_attachments.tmpl", map[string]any{
		"Names": strings.Join(names, ", "),
	})
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

func (a *Adapter) Report(ctx context.Context, event domain.Event, report domain.ReportMessage) ([]domain.ReportReceipt, error) {
	targetChannelID := discordOutboundChannelID(event)
	if a.session == nil || targetChannelID == "" {
		return nil, nil
	}
	report, err := channels.ResolveReport(report, channels.ResolveOptions{
		WorkspaceRoot: a.workspaceRoot,
		Limits:        a.attachmentLimits,
	})
	if err != nil {
		return nil, err
	}
	if report.Empty() {
		return nil, nil
	}

	attachments := report.Attachments
	chunks := channels.SplitText(report.Text, a.messageLimits.MaxChars)
	reference := discordReportReference(event, report)
	receipts := make([]domain.ReportReceipt, 0, len(chunks))
	for i, chunk := range chunks {
		var files []*discordgo.File
		var closers []io.Closer
		if i == 0 && len(attachments) > 0 {
			files, closers, err = discordFiles(attachments)
			if err != nil {
				return nil, err
			}
		}
		sent, err := sendDiscordMessage(ctx, a.session, targetChannelID, chunk, files, reference)
		if err != nil {
			closeDiscordFiles(closers)
			return nil, err
		}
		if receipt := discordReportReceipt(event, sent); receipt.MessageID != "" || receipt.ThreadID != "" {
			receipt.Body = chunk
			receipts = append(receipts, receipt)
		}
		closeDiscordFiles(closers)
	}
	return receipts, nil
}

func discordOutboundChannelID(event domain.Event) string {
	if threadID := strings.TrimSpace(event.ThreadID); threadID != "" {
		return threadID
	}
	return strings.TrimSpace(event.ChannelID)
}

func discordReportReceipt(event domain.Event, sent *discordgo.Message) domain.ReportReceipt {
	receipt := domain.ReportReceipt{
		ChannelID: strings.TrimSpace(event.ChannelID),
		ContextID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToContextID]),
		ThreadID:  strings.TrimSpace(event.ThreadID),
	}
	if sent != nil {
		receipt.MessageID = strings.TrimSpace(sent.ID)
		if strings.TrimSpace(receipt.ChannelID) == "" && strings.TrimSpace(sent.ChannelID) != "" {
			receipt.ChannelID = strings.TrimSpace(sent.ChannelID)
		}
		if strings.TrimSpace(sent.GuildID) != "" {
			receipt.ContextID = strings.TrimSpace(sent.GuildID)
		}
	}
	return receipt
}

func discordReportReference(event domain.Event, report domain.ReportMessage) *discordgo.MessageReference {
	if report.ReplyTo == nil || report.ReplyTo.Empty() {
		return nil
	}
	messageID := strings.TrimSpace(report.ReplyTo.MessageID)
	if messageID == "" {
		return nil
	}
	failIfNotExists := false
	return &discordgo.MessageReference{
		MessageID:       messageID,
		ChannelID:       firstNonEmpty(report.ReplyTo.ChannelID, event.ChannelID),
		GuildID:         firstNonEmpty(report.ReplyTo.ContextID, event.Metadata[domain.MetadataKeyReplyToContextID]),
		FailIfNotExists: &failIfNotExists,
	}
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

func sendDiscordMessage(ctx context.Context, session *discordgo.Session, channelID, content string, files []*discordgo.File, reference *discordgo.MessageReference) (*discordgo.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if strings.TrimSpace(content) == "" && len(files) == 0 {
		return nil, nil
	}
	message, err := session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		Files:           files,
		Reference:       reference,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	return message, err
}

func closeDiscordFiles(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func (a *Adapter) NotifyTyping(ctx context.Context, event domain.Event) error {
	targetChannelID := discordOutboundChannelID(event)
	if a.session == nil || targetChannelID == "" {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	typingCtx, cancel := context.WithTimeout(ctx, discordTypingTimeout)
	defer cancel()
	return a.session.ChannelTyping(
		targetChannelID,
		discordgo.WithContext(typingCtx),
		discordgo.WithRetryOnRatelimit(false),
		discordgo.WithRestRetries(0),
	)
}
