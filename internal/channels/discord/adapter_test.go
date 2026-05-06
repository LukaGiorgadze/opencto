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
		if r.URL.Path != "/channels/channel-1/messages" {
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
			"channel_id": "channel-1",
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
	err = adapter.Report(context.Background(), domain.Event{ChannelID: "channel-1"}, domain.ReportMessage{
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
