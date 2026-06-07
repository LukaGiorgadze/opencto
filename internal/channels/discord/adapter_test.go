package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/domain"
)

func TestReportSplitsMessagesByConfiguredLimit(t *testing.T) {
	var mu sync.Mutex
	var contents []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/channels/thread-1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}

		var payload struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}

		mu.Lock()
		contents = append(contents, payload.Content)
		id := len(contents)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":         strconv.Itoa(id),
			"channel_id": "thread-1",
			"content":    payload.Content,
		})
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	session.Client = server.Client()

	adapter := &Adapter{
		session:       session,
		messageLimits: MessageLimits{MaxChars: 10},
	}
	receipts, err := adapter.Report(context.Background(), domain.Event{
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		ThreadID:    "thread-1",
	}, domain.ReportMessage{
		Text: "hello world. done",
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"hello", "world.", "done"}
	if len(contents) != len(want) {
		t.Fatalf("expected %d messages, got %d: %#v", len(want), len(contents), contents)
	}
	for i := range want {
		if contents[i] != want[i] {
			t.Fatalf("message %d: expected %q, got %q", i, want[i], contents[i])
		}
	}
	if len(receipts) != len(want) {
		t.Fatalf("expected %d receipts, got %#v", len(want), receipts)
	}
	for i, receipt := range receipts {
		if receipt.MessageID != strconv.Itoa(i+1) || receipt.ChannelID != "channel-1" || receipt.ThreadID != "thread-1" {
			t.Fatalf("unexpected receipt %d: %#v", i, receipt)
		}
	}
}

func TestReportCanReplyToDiscordMessage(t *testing.T) {
	var payload struct {
		Content          string `json:"content"`
		MessageReference struct {
			MessageID       string `json:"message_id"`
			ChannelID       string `json:"channel_id"`
			GuildID         string `json:"guild_id"`
			FailIfNotExists *bool  `json:"fail_if_not_exists"`
		} `json:"message_reference"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels/channel-1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":         "bot-message-1",
			"channel_id": "channel-1",
			"guild_id":   "guild-1",
			"content":    payload.Content,
		})
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	session.Client = server.Client()

	adapter := &Adapter{
		session:       session,
		messageLimits: MessageLimits{MaxChars: 2000},
	}
	_, err = adapter.Report(context.Background(), domain.Event{
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
	}, domain.ReportMessage{
		Text: "approve?",
		ReplyTo: &domain.ReportReply{
			MessageID: "user-message-1",
			ChannelID: "channel-1",
			ContextID: "guild-1",
		},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if payload.MessageReference.MessageID != "user-message-1" ||
		payload.MessageReference.ChannelID != "channel-1" ||
		payload.MessageReference.GuildID != "guild-1" ||
		payload.MessageReference.FailIfNotExists == nil ||
		*payload.MessageReference.FailIfNotExists {
		t.Fatalf("unexpected message reference: %#v", payload.MessageReference)
	}
}

func TestNotifyTypingUsesContext(t *testing.T) {
	var once sync.Once
	var cancel context.CancelFunc
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/channels/channel-1/typing" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		once.Do(func() { close(started) })
		cancel()
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	client := server.Client()
	client.Timeout = 200 * time.Millisecond
	session.Client = client

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	adapter := &Adapter{session: session}
	startedAt := time.Now()
	err = adapter.NotifyTyping(ctx, domain.Event{ChannelID: "channel-1"})
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context error, got %v", err)
	}
	if time.Since(startedAt) >= client.Timeout {
		t.Fatalf("notify typing was not bounded by context")
	}
	select {
	case <-started:
	default:
		t.Fatalf("expected typing request to reach server")
	}
}

func TestNotifyTypingUsesThreadTarget(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	session.Client = server.Client()

	adapter := &Adapter{session: session}
	err = adapter.NotifyTyping(context.Background(), domain.Event{
		ChannelID:   "channel-1",
		ChannelType: domain.ChannelTypeDiscord,
		ThreadID:    "thread-1",
	})
	if err != nil {
		t.Fatalf("notify typing: %v", err)
	}
	if gotPath != "/channels/thread-1/typing" {
		t.Fatalf("expected typing in thread, got path %q", gotPath)
	}
}

func TestNormalizeMessageUsesStateThreadChannel(t *testing.T) {
	t.Parallel()

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	if err := session.State.GuildAdd(&discordgo.Guild{ID: "guild-1"}); err != nil {
		t.Fatalf("add guild: %v", err)
	}
	if err := session.State.ChannelAdd(&discordgo.Channel{
		ID:       "thread-1",
		GuildID:  "guild-1",
		ParentID: "channel-1",
		Type:     discordgo.ChannelTypeGuildPublicThread,
	}); err != nil {
		t.Fatalf("add thread channel: %v", err)
	}

	adapter := &Adapter{
		projectID:      "project-1",
		session:        session,
		threadChannels: map[string]string{},
	}
	event, err := adapter.NormalizeMessage(context.Background(), &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message-1",
			ChannelID: "thread-1",
			GuildID:   "guild-1",
			Content:   "in orange colors",
			Author:    &discordgo.User{ID: "user-1", Username: "luka"},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}
	if event.ThreadID != "thread-1" {
		t.Fatalf("expected thread id from state channel, got %q", event.ThreadID)
	}
	if event.ChannelID != "channel-1" {
		t.Fatalf("expected parent channel id for thread message, got %q", event.ChannelID)
	}
}

func TestNormalizeMessageHydratesEmptyGatewayContent(t *testing.T) {
	var requestedPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/channels/thread-1/messages/message-1" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":         "message-1",
			"channel_id": "thread-1",
			"guild_id":   "guild-1",
			"content":    "1",
			"author": map[string]any{
				"id":       "user-1",
				"username": "luka",
			},
		})
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	session.Client = server.Client()
	adapter := &Adapter{
		projectID:      "project-1",
		session:        session,
		threadChannels: map[string]string{"thread-1": "thread-1"},
	}

	event, err := adapter.NormalizeMessage(context.Background(), &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message-1",
			ChannelID: "thread-1",
			GuildID:   "guild-1",
			Content:   "",
			Author: &discordgo.User{
				ID:       "user-1",
				Username: "luka",
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}
	if requestedPath == "" {
		t.Fatalf("expected adapter to fetch the empty message")
	}
	if event.Body != "1" {
		t.Fatalf("expected hydrated message body, got %q", event.Body)
	}
	if event.ThreadID != "thread-1" {
		t.Fatalf("expected thread id to be preserved, got %q", event.ThreadID)
	}
}

func TestDiscordThreadIDDoesNotCacheFailedLookup(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	oldEndpointChannels := discordgo.EndpointChannels
	discordgo.EndpointChannels = server.URL + "/channels/"
	t.Cleanup(func() {
		discordgo.EndpointChannels = oldEndpointChannels
	})

	session, err := discordgo.New("Bot test-token")
	if err != nil {
		t.Fatalf("new discord session: %v", err)
	}
	session.Client = server.Client()
	adapter := &Adapter{
		session:        session,
		threadChannels: map[string]string{},
	}
	message := &discordgo.MessageCreate{Message: &discordgo.Message{ChannelID: "thread-1"}}
	if got := adapter.discordThreadID(message); got != "" {
		t.Fatalf("expected no thread id while lookup fails, got %q", got)
	}
	if _, ok := adapter.threadChannels["thread-1"]; ok {
		t.Fatalf("failed lookup should not cache thread miss")
	}
}

func TestNormalizeMessageDownloadsAttachments(t *testing.T) {
	t.Parallel()

	data := []byte{0, 1, 2, 3}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{
		projectID:     "project-1",
		attachmentDir: t.TempDir(),
		httpClient:    server.Client(),
	}
	event, err := adapter.NormalizeMessage(context.Background(), &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message-1",
			ChannelID: "channel-1",
			Author: &discordgo.User{
				ID:       "user-1",
				Username: "luka",
			},
			Attachments: []*discordgo.MessageAttachment{{
				ID:          "attachment-1",
				URL:         server.URL + "/voice.wav",
				Filename:    "voice.wav",
				ContentType: "audio/wav",
				Size:        len(data),
			}},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}
	if !strings.Contains(event.Body, "voice.wav") {
		t.Fatalf("expected attachment fallback body, got %q", event.Body)
	}

	attachments, ok := event.Payload[discordAttachmentPayloadKey].([]domain.EventAttachment)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one typed attachment in payload, got %#v", event.Payload[discordAttachmentPayloadKey])
	}
	attachment := attachments[0]
	if attachment.ProjectID != "project-1" || attachment.EventID != event.ID || attachment.ContentType != "audio/wav" {
		t.Fatalf("unexpected attachment metadata: %#v", attachment)
	}
	got, err := os.ReadFile(attachment.LocalPath)
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("downloaded attachment data mismatch: %v", got)
	}
}

func TestNormalizeMessageKeepsImageOnlyMessage(t *testing.T) {
	t.Parallel()

	data := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(data)
	}))
	t.Cleanup(server.Close)

	adapter := &Adapter{
		projectID:     "project-1",
		attachmentDir: t.TempDir(),
		httpClient:    server.Client(),
	}
	event, err := adapter.NormalizeMessage(context.Background(), &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message-1",
			ChannelID: "channel-1",
			Author: &discordgo.User{
				ID:       "user-1",
				Username: "luka",
			},
			Attachments: []*discordgo.MessageAttachment{{
				ID:          "attachment-1",
				URL:         server.URL + "/screenshot.png",
				Filename:    "screenshot.png",
				ContentType: "image/png",
				Size:        len(data),
			}},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}
	if discordEventEmptyUserMessage(event) {
		t.Fatalf("image-only message should not be considered empty")
	}
	if !strings.Contains(event.Body, "screenshot.png") || !strings.Contains(event.Body, "image/png") {
		t.Fatalf("expected attachment fallback body, got %q", event.Body)
	}
	attachments, ok := event.Payload[discordAttachmentPayloadKey].([]domain.EventAttachment)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one typed attachment, got %#v", event.Payload[discordAttachmentPayloadKey])
	}
	got, err := os.ReadFile(attachments[0].LocalPath)
	if err != nil {
		t.Fatalf("read downloaded attachment: %v", err)
	}
	if string(got) != string(data) {
		t.Fatalf("downloaded attachment data mismatch: %v", got)
	}
}

func TestNormalizeMessageCapturesReplyMetadataWithReferencedContent(t *testing.T) {
	t.Parallel()

	adapter := &Adapter{
		projectID: "project-1",
	}
	event, err := adapter.NormalizeMessage(context.Background(), &discordgo.MessageCreate{
		Message: &discordgo.Message{
			ID:        "message-2",
			ChannelID: "channel-1",
			GuildID:   "guild-1",
			Content:   "Do it!",
			Author: &discordgo.User{
				ID:       "user-1",
				Username: "luka",
			},
			MessageReference: &discordgo.MessageReference{
				MessageID: "bot-message-1",
				ChannelID: "channel-1",
				GuildID:   "guild-1",
			},
			ReferencedMessage: &discordgo.Message{
				ID:        "bot-message-1",
				ChannelID: "channel-1",
				GuildID:   "guild-1",
				Content:   "I can do that. Which workspace should I use?",
				Author: &discordgo.User{
					ID:       "bot-1",
					Username: "opencto",
					Bot:      true,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}
	if event.Metadata[domain.MetadataKeyReplyToMessageID] != "bot-message-1" ||
		event.Metadata[domain.MetadataKeyReplyToChannelID] != "channel-1" ||
		event.Metadata[domain.MetadataKeyReplyToContextID] != "guild-1" ||
		event.Metadata[domain.MetadataKeyReplyToActorID] != "bot-1" {
		t.Fatalf("unexpected reply metadata: %#v", event.Metadata)
	}
	if event.Payload[domain.MetadataKeyReplyToMessageID] != "bot-message-1" ||
		event.Payload[domain.MetadataKeyReplyToChannelID] != "channel-1" ||
		event.Payload[domain.MetadataKeyReplyToContextID] != "guild-1" ||
		event.Payload[domain.MetadataKeyReplyToActorID] != "bot-1" {
		t.Fatalf("unexpected reply payload: %#v", event.Payload)
	}
	content, ok := event.Payload["reply_to_content"].(string)
	if !ok || !strings.Contains(content, "Which workspace should I use?") {
		t.Fatalf("expected referenced content in payload, got %#v", event.Payload["reply_to_content"])
	}
}

func TestDiscordFilesOpenResolvedAttachments(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "screen shot.png")
	if err := os.WriteFile(path, []byte("image"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}

	files, closers, err := discordFiles([]domain.ReportAttachment{{
		Path:        path,
		Filename:    "../Screen Shot.png",
		ContentType: "image/png",
	}})
	defer closeDiscordFiles(closers)
	if err != nil {
		t.Fatalf("discord files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected one file, got %d", len(files))
	}
	if files[0].Name != "Screen-Shot.png" || files[0].ContentType != "image/png" {
		t.Fatalf("unexpected file metadata: %#v", files[0])
	}
	got, err := io.ReadAll(files[0].Reader)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != "image" {
		t.Fatalf("unexpected file content: %q", got)
	}
}
