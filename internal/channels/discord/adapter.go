package discord

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/runtime"
	"github.com/opencto/opencto/internal/runtime/signals"
)

const discordMessageMaxLength = 4000

type Adapter struct {
	projectID  string
	dispatcher *runtime.Dispatcher
	session    *discordgo.Session
	logger     *slog.Logger
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
	return &Adapter{
		projectID:  projectID,
		dispatcher: dispatcher,
		session:    session,
		logger:     logger,
	}, nil
}

func (a *Adapter) Start(ctx context.Context) error {
	a.session.AddHandler(func(session *discordgo.Session, message *discordgo.MessageCreate) {
		if message.Author == nil || message.Author.Bot {
			return
		}
		if signal, ok := a.parseApprovalDecision(message); ok {
			if err := a.dispatcher.SubmitApprovalDecision(ctx, signal); err != nil {
				a.logger.Error("submit approval decision", slog.String("error", err.Error()), slog.String("approval_id", signal.ApprovalID))
				_, _ = session.ChannelMessageSend(message.ChannelID, "Failed to record the approval decision. Check the worker logs.")
				return
			}
			verb := "approved"
			if !signal.Approved {
				verb = "rejected"
			}
			_, _ = session.ChannelMessageSend(message.ChannelID, fmt.Sprintf("Approval `%s` %s.", signal.ApprovalID, verb))
			return
		}
		event, err := a.NormalizeMessage(message)
		if err != nil {
			a.logger.Error("normalize discord message", slog.String("error", err.Error()))
			return
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

func (a *Adapter) NormalizeMessage(message *discordgo.MessageCreate) (domain.Event, error) {
	id, err := domain.NewID()
	if err != nil {
		return domain.Event{}, err
	}

	now := time.Now().UTC()
	body := normalizeDiscordBody(message.Content)
	return domain.Event{
		ID:          id,
		ProjectID:   a.projectID,
		Kind:        domain.EventKindMessage,
		ChannelID:   message.ChannelID,
		ChannelType: domain.ChannelTypeDiscord,
		ActorID:     message.Author.ID,
		ActorName:   message.Author.Username,
		Body:        body,
		Payload: map[string]any{
			"message_id": message.ID,
			"guild_id":   message.GuildID,
		},
		Provenance: domain.Provenance{
			Source:     string(domain.ChannelTypeDiscord),
			SourceID:   message.ID,
			Actor:      message.Author.Username,
			ObservedAt: now,
		},
		CreatedAt: now,
	}, nil
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

func (a *Adapter) Report(_ context.Context, event domain.Event, message string) error {
	if a.session == nil || event.ChannelID == "" {
		return nil
	}
	for _, chunk := range splitDiscordMessage(message, discordMessageMaxLength) {
		if _, err := a.session.ChannelMessageSend(event.ChannelID, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) parseApprovalDecision(message *discordgo.MessageCreate) (signals.ApprovalDecisionSignal, bool) {
	content := strings.TrimSpace(message.Content)
	fields := strings.Fields(content)
	if len(fields) < 2 {
		return signals.ApprovalDecisionSignal{}, false
	}

	command := strings.ToLower(fields[0])
	if command != "approve" && command != "reject" {
		return signals.ApprovalDecisionSignal{}, false
	}

	comment := ""
	if len(fields) > 2 {
		comment = strings.TrimSpace(strings.Join(fields[2:], " "))
	}

	return signals.ApprovalDecisionSignal{
		ProjectID:  a.projectID,
		ApprovalID: strings.TrimSpace(fields[1]),
		Approved:   command == "approve",
		ActorID:    message.Author.ID,
		ActorName:  message.Author.Username,
		Comment:    comment,
		DecidedAt:  time.Now().UTC(),
	}, true
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
