package discord

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"

	"github.com/opencto/opencto/internal/domain"
)

func TestSplitDiscordMessageByLines(t *testing.T) {
	message := strings.Repeat("a", discordMessageMaxLength-10) + "\n" + strings.Repeat("b", 50)

	chunks := splitDiscordMessage(message, discordMessageMaxLength)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if utf8.RuneCountInString(chunk) > discordMessageMaxLength {
			t.Fatalf("chunk %d exceeds limit: %d", i, utf8.RuneCountInString(chunk))
		}
	}
	if chunks[0] != strings.Repeat("a", discordMessageMaxLength-10) {
		t.Fatalf("unexpected first chunk length/content")
	}
	if chunks[1] != strings.Repeat("b", 50) {
		t.Fatalf("unexpected second chunk length/content")
	}
}

func TestSplitDiscordMessageFallsBackToHardSplit(t *testing.T) {
	message := strings.Repeat("x", discordMessageMaxLength+25)

	chunks := splitDiscordMessage(message, discordMessageMaxLength)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if utf8.RuneCountInString(chunks[0]) != discordMessageMaxLength {
		t.Fatalf("unexpected first chunk size: %d", utf8.RuneCountInString(chunks[0]))
	}
	if utf8.RuneCountInString(chunks[1]) != 25 {
		t.Fatalf("unexpected second chunk size: %d", utf8.RuneCountInString(chunks[1]))
	}
	if strings.Join(chunks, "") != message {
		t.Fatalf("split/join did not preserve content")
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
