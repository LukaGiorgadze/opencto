package telegram

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/PaulSonOfLars/gotgbot/v2"

	"github.com/opencto/opencto/internal/domain"
)

type fakeTelegramBot struct {
	fileURL       string
	files         map[string]*gotgbot.File
	messages      []fakeTelegramMessageSend
	documents     []fakeTelegramDocumentSend
	chatActions   []fakeTelegramChatAction
	webhookURL    string
	webhookOpts   *gotgbot.SetWebhookOpts
	nextMessageID int64
}

type fakeTelegramMessageSend struct {
	chatID int64
	text   string
	opts   *gotgbot.SendMessageOpts
}

type fakeTelegramDocumentSend struct {
	chatID int64
	opts   *gotgbot.SendDocumentOpts
}

type fakeTelegramChatAction struct {
	chatID int64
	action string
	opts   *gotgbot.SendChatActionOpts
}

func (b *fakeTelegramBot) SetWebhookWithContext(_ context.Context, url string, opts *gotgbot.SetWebhookOpts) (bool, error) {
	b.webhookURL = url
	b.webhookOpts = opts
	return true, nil
}

func (b *fakeTelegramBot) GetFileWithContext(_ context.Context, fileID string, _ *gotgbot.GetFileOpts) (*gotgbot.File, error) {
	if b.files == nil {
		return nil, nil
	}
	return b.files[fileID], nil
}

func (b *fakeTelegramBot) SendMessageWithContext(_ context.Context, chatID int64, text string, opts *gotgbot.SendMessageOpts) (*gotgbot.Message, error) {
	b.messages = append(b.messages, fakeTelegramMessageSend{chatID: chatID, text: text, opts: opts})
	return b.nextMessage(chatID, optsMessageThreadID(opts)), nil
}

func (b *fakeTelegramBot) SendDocumentWithContext(_ context.Context, chatID int64, _ gotgbot.InputFileOrString, opts *gotgbot.SendDocumentOpts) (*gotgbot.Message, error) {
	b.documents = append(b.documents, fakeTelegramDocumentSend{chatID: chatID, opts: opts})
	return b.nextMessage(chatID, optsDocumentThreadID(opts)), nil
}

func (b *fakeTelegramBot) SendChatActionWithContext(_ context.Context, chatID int64, action string, opts *gotgbot.SendChatActionOpts) (bool, error) {
	b.chatActions = append(b.chatActions, fakeTelegramChatAction{chatID: chatID, action: action, opts: opts})
	return true, nil
}

func (b *fakeTelegramBot) FileURL(_ string, _ string, _ *gotgbot.RequestOpts) string {
	return b.fileURL
}

func (b *fakeTelegramBot) nextMessage(chatID, threadID int64) *gotgbot.Message {
	b.nextMessageID++
	if b.nextMessageID == 0 {
		b.nextMessageID = 1
	}
	return &gotgbot.Message{
		MessageId:       b.nextMessageID,
		MessageThreadId: threadID,
		Chat:            gotgbot.Chat{Id: chatID},
	}
}

func optsMessageThreadID(opts *gotgbot.SendMessageOpts) int64 {
	if opts == nil {
		return 0
	}
	return opts.MessageThreadId
}

func optsDocumentThreadID(opts *gotgbot.SendDocumentOpts) int64 {
	if opts == nil {
		return 0
	}
	return opts.MessageThreadId
}

func TestNewAdapterStoresAttachmentsUnderWorkspaceRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	adapter, err := newAdapter("project-1", "123:token", &fakeTelegramBot{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		WorkspaceRoot: root,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	want := filepath.Join(root, "data", "attachments", "project-1", "telegram")
	if adapter.attachmentDir != want {
		t.Fatalf("expected attachment dir %q, got %q", want, adapter.attachmentDir)
	}
}

func TestNormalizeMessageMapsTopicReplyAndDownloadsPhoto(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("jpeg"))
	}))
	t.Cleanup(server.Close)

	bot := &fakeTelegramBot{
		fileURL: server.URL + "/photo",
		files: map[string]*gotgbot.File{
			"photo-big": {FileId: "photo-big", FilePath: "photos/photo-big.jpg", FileSize: 4},
		},
	}
	adapter, err := newAdapter("project-1", "123:token", bot, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	adapter.httpClient = server.Client()
	adapter.attachmentDir = t.TempDir()

	event, err := adapter.NormalizeMessage(context.Background(), 99, &gotgbot.Message{
		MessageId:       55,
		MessageThreadId: 7,
		Chat:            gotgbot.Chat{Id: -100123, Type: "supergroup", Title: "Team", IsForum: true},
		From:            &gotgbot.User{Id: 42, FirstName: "Luka", Username: "luka"},
		Text:            "  hello   telegram  ",
		ReplyToMessage: &gotgbot.Message{
			MessageId: 40,
			Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup"},
			From:      &gotgbot.User{Id: 7, FirstName: "Ada"},
			Text:      "original",
		},
		Photo: []gotgbot.PhotoSize{
			{FileId: "photo-small", FileUniqueId: "small", Width: 10, Height: 10, FileSize: 1},
			{FileId: "photo-big", FileUniqueId: "big", Width: 100, Height: 100, FileSize: 4},
		},
	})
	if err != nil {
		t.Fatalf("normalize message: %v", err)
	}

	if event.ChannelType != domain.ChannelTypeTelegram || event.ChannelID != "-100123" || event.ThreadID != "7" {
		t.Fatalf("unexpected channel fields: %#v", event)
	}
	if event.Provenance.SourceID != "-100123:55" {
		t.Fatalf("unexpected source id: %q", event.Provenance.SourceID)
	}
	if event.ActorID != "42" || event.ActorName != "Luka" || event.Body != "hello telegram" {
		t.Fatalf("unexpected actor/body: %#v", event)
	}
	if event.Metadata[domain.MetadataKeyReplyToMessageID] != "-100123:40" ||
		event.Metadata[domain.MetadataKeyReplyToChannelID] != "-100123" ||
		event.Metadata[domain.MetadataKeyReplyToActorID] != "7" {
		t.Fatalf("unexpected reply metadata: %#v", event.Metadata)
	}
	attachments, ok := event.Payload[telegramAttachmentPayloadKey].([]domain.EventAttachment)
	if !ok || len(attachments) != 1 {
		t.Fatalf("expected one attachment, got %#v", event.Payload[telegramAttachmentPayloadKey])
	}
	attachment := attachments[0]
	if attachment.SourceID != "photo-big" || attachment.Filename != "photo.jpg" || attachment.ContentType != "image/jpeg" {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
	if attachment.LocalPath == "" {
		t.Fatalf("expected downloaded attachment path: %#v", attachment)
	}
	if data, err := os.ReadFile(attachment.LocalPath); err != nil || string(data) != "jpeg" {
		t.Fatalf("unexpected downloaded attachment data %q: %v", string(data), err)
	}
}

func TestNormalizeMessageUsesStableEventID(t *testing.T) {
	t.Parallel()

	adapter, err := newAdapter("project-1", "123:token", &fakeTelegramBot{}, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	message := &gotgbot.Message{
		MessageId: 55,
		Chat:      gotgbot.Chat{Id: -100123, Type: "supergroup", Title: "Team"},
		From:      &gotgbot.User{Id: 42, FirstName: "Luka"},
		Text:      "hello",
	}
	first, err := adapter.NormalizeMessage(context.Background(), 99, message)
	if err != nil {
		t.Fatalf("normalize first message: %v", err)
	}
	second, err := adapter.NormalizeMessage(context.Background(), 99, message)
	if err != nil {
		t.Fatalf("normalize second message: %v", err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("expected stable event id, got %q and %q", first.ID, second.ID)
	}

	nextUpdate, err := adapter.NormalizeMessage(context.Background(), 100, message)
	if err != nil {
		t.Fatalf("normalize next update: %v", err)
	}
	if nextUpdate.ID == first.ID {
		t.Fatalf("expected different update id to produce a different event id")
	}
}

func TestReportSendsThreadedMessagesAndDocuments(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	attachmentPath := filepath.Join(root, "report.txt")
	if err := os.WriteFile(attachmentPath, []byte("report"), 0o644); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	bot := &fakeTelegramBot{}
	adapter, err := newAdapter("project-1", "123:token", bot, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{
		WorkspaceRoot: root,
		MessageLimits: MessageLimits{MaxChars: 6},
		AttachmentLimits: AttachmentLimits{
			MaxFiles:      2,
			MaxFileBytes:  1024,
			MaxTotalBytes: 2048,
		},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	receipts, err := adapter.Report(context.Background(), domain.Event{
		ChannelID:   "-100123",
		ChannelType: domain.ChannelTypeTelegram,
		ThreadID:    "7",
	}, domain.ReportMessage{
		Text:        "hello world",
		Attachments: []domain.ReportAttachment{{Path: attachmentPath}},
		ReplyTo: &domain.ReportReply{
			MessageID: "-100123:55",
			ChannelID: "-100123",
		},
	})
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if len(bot.messages) != 2 || bot.messages[0].text != "hello" || bot.messages[1].text != "world" {
		t.Fatalf("unexpected message sends: %#v", bot.messages)
	}
	if len(bot.documents) != 1 {
		t.Fatalf("expected one document send, got %#v", bot.documents)
	}
	messageOpts := bot.messages[0].opts
	if messageOpts == nil || messageOpts.MessageThreadId != 7 ||
		messageOpts.ReplyParameters == nil ||
		messageOpts.ReplyParameters.MessageId != 55 ||
		messageOpts.ReplyParameters.ChatId != -100123 {
		t.Fatalf("unexpected message opts: %#v", messageOpts)
	}
	documentOpts := bot.documents[0].opts
	if documentOpts == nil || documentOpts.MessageThreadId != 7 ||
		documentOpts.ReplyParameters == nil ||
		documentOpts.ReplyParameters.MessageId != 55 {
		t.Fatalf("unexpected document opts: %#v", documentOpts)
	}
	if len(receipts) != 3 ||
		receipts[0].MessageID != "-100123:1" ||
		receipts[1].MessageID != "-100123:2" ||
		receipts[2].MessageID != "-100123:3" ||
		receipts[0].ThreadID != "7" ||
		receipts[0].ChannelID != "-100123" {
		t.Fatalf("unexpected receipts: %#v", receipts)
	}
}

func TestTelegramWebhookURLAppendsConfiguredPath(t *testing.T) {
	t.Parallel()

	got, err := telegramWebhookURL("https://example.com", "/telegram/webhook")
	if err != nil {
		t.Fatalf("webhook url: %v", err)
	}
	if got != "https://example.com/telegram/webhook" {
		t.Fatalf("unexpected webhook url: %q", got)
	}
}
