package activities

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

func (a *Activities) ReportResponse(ctx context.Context, request ReportResponseRequest) (ReportResponseResult, error) {
	report := domain.ReportMessage{
		Text:        strings.TrimSpace(request.Message),
		Attachments: append([]domain.ReportAttachment(nil), request.Attachments...),
		ReplyTo:     cleanReportReply(request.ReplyTo),
	}
	if report.Empty() || a.Reporter == nil {
		return ReportResponseResult{}, nil
	}
	a.logActivityStep(
		"ReportResponse", "start",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
		slog.String("channel_type", string(request.Event.ChannelType)),
		slog.String("channel_id", strings.TrimSpace(request.Event.ChannelID)),
	)
	receipts, err := a.Reporter.Report(ctx, request.Event, report)
	if err != nil {
		a.logActivityStep(
			"ReportResponse", "error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	if err := a.persistReportedConversationMessages(ctx, request.Event, report, receipts); err != nil {
		a.logActivityStep(
			"ReportResponse", "conversation_error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	if err := a.persistReportedConversationThreads(ctx, request.Event, receipts); err != nil {
		a.logActivityStep(
			"ReportResponse", "thread_error",
			slog.String("project_id", request.Event.ProjectID),
			slog.String("event_id", request.Event.ID),
			slog.String("error", err.Error()),
		)
		return ReportResponseResult{}, err
	}
	a.logActivityStep(
		"ReportResponse", "done",
		slog.String("project_id", request.Event.ProjectID),
		slog.String("event_id", request.Event.ID),
	)
	return ReportResponseResult{Receipts: receipts}, nil
}

func (a *Activities) persistReportedConversationMessages(ctx context.Context, event domain.Event, report domain.ReportMessage, receipts []domain.ReportReceipt) error {
	if a.Store == nil || strings.TrimSpace(report.Text) == "" {
		return nil
	}
	projectID := strings.TrimSpace(event.ProjectID)
	if projectID == "" {
		projectID = strings.TrimSpace(a.Project.ID)
	}
	if projectID == "" {
		return nil
	}
	targets := reportedConversationTargets(event, receipts)
	for index, target := range targets {
		if !shouldPersistReportedConversationTarget(event, target) {
			continue
		}
		metadata := domain.Metadata{
			"source": "report_response",
		}
		if target.MessageID != "" {
			metadata["message_id"] = target.MessageID
		}
		body := firstNonEmpty(target.Body, report.Text)
		message := domain.ConversationMessage{
			ID:          stableActivityID("conversation-assistant-report", projectID, event.ID, strconv.Itoa(index), target.MessageID, target.ChannelID, target.ThreadID, body),
			ProjectID:   projectID,
			EventID:     event.ID,
			Role:        domain.ConversationRoleAssistant,
			ChannelType: event.ChannelType,
			ChannelID:   target.ChannelID,
			ThreadID:    target.ThreadID,
			Body:        body,
			Metadata:    metadata,
			CreatedAt:   time.Now().UTC(),
		}
		if strings.TrimSpace(message.ChannelID) == "" {
			continue
		}
		if err := a.Store.UpsertConversationMessage(ctx, message); err != nil {
			return err
		}
	}
	return nil
}

func shouldPersistReportedConversationTarget(event domain.Event, target reportedConversationTarget) bool {
	return strings.TrimSpace(target.ChannelID) != strings.TrimSpace(event.ChannelID) ||
		strings.TrimSpace(target.ThreadID) != strings.TrimSpace(event.ThreadID)
}

type reportedConversationTarget struct {
	MessageID string
	ChannelID string
	ThreadID  string
	Body      string
}

func reportedConversationTargets(event domain.Event, receipts []domain.ReportReceipt) []reportedConversationTarget {
	seen := map[string]bool{}
	var targets []reportedConversationTarget
	add := func(target reportedConversationTarget) {
		target.MessageID = strings.TrimSpace(target.MessageID)
		target.ChannelID = strings.TrimSpace(target.ChannelID)
		target.ThreadID = strings.TrimSpace(target.ThreadID)
		target.Body = strings.TrimSpace(target.Body)
		if target.ChannelID == "" {
			return
		}
		key := target.MessageID + "\x00" + target.ChannelID + "\x00" + target.ThreadID + "\x00" + target.Body
		if seen[key] {
			return
		}
		seen[key] = true
		targets = append(targets, target)
	}
	for _, receipt := range receipts {
		channelID := strings.TrimSpace(firstNonEmpty(receipt.ChannelID, event.ChannelID))
		threadID := strings.TrimSpace(receipt.ThreadID)
		messageID := strings.TrimSpace(receipt.MessageID)
		add(reportedConversationTarget{
			MessageID: messageID,
			ChannelID: channelID,
			ThreadID:  threadID,
			Body:      receipt.Body,
		})
		if event.ChannelType == domain.ChannelTypeDiscord && threadID == "" && messageID != "" {
			add(reportedConversationTarget{
				MessageID: messageID,
				ChannelID: channelID,
				ThreadID:  messageID,
				Body:      receipt.Body,
			})
		}
	}
	if len(receipts) == 0 {
		add(reportedConversationTarget{
			ChannelID: strings.TrimSpace(event.ChannelID),
			ThreadID:  strings.TrimSpace(event.ThreadID),
		})
	}
	return targets
}

func (a *Activities) persistReportedConversationThreads(ctx context.Context, event domain.Event, receipts []domain.ReportReceipt) error {
	if a.Store == nil {
		return nil
	}
	event = inferDiscordThreadContext(event)
	if strings.TrimSpace(event.ProjectID) == "" {
		event.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	for _, target := range reportedConversationTargets(event, receipts) {
		if strings.TrimSpace(target.ThreadID) == "" {
			continue
		}
		thread := domain.ConversationThread{
			ID:            stableActivityID("conversation-thread", event.ProjectID, string(event.ChannelType), target.ChannelID, target.ThreadID),
			ProjectID:     strings.TrimSpace(event.ProjectID),
			ChannelType:   event.ChannelType,
			ChannelID:     strings.TrimSpace(target.ChannelID),
			ThreadID:      strings.TrimSpace(target.ThreadID),
			RootMessageID: strings.TrimSpace(target.MessageID),
			EventID:       strings.TrimSpace(event.ID),
			Metadata: domain.Metadata{
				"source": "report_response",
			},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
			LastMessageAt: time.Now().UTC(),
		}
		if thread.RootMessageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
			thread.RootMessageID = thread.ThreadID
		}
		if err := a.persistConversationThread(ctx, thread); err != nil {
			return err
		}
	}
	return nil
}

func (a *Activities) persistConversationThread(ctx context.Context, thread domain.ConversationThread) error {
	if a.Store == nil {
		return nil
	}
	thread.ProjectID = strings.TrimSpace(thread.ProjectID)
	if thread.ProjectID == "" {
		thread.ProjectID = strings.TrimSpace(a.Project.ID)
	}
	thread.ChannelID = strings.TrimSpace(thread.ChannelID)
	thread.ThreadID = strings.TrimSpace(thread.ThreadID)
	if thread.ProjectID == "" || thread.ChannelID == "" || thread.ThreadID == "" {
		return nil
	}
	if thread.ID == "" {
		thread.ID = stableActivityID("conversation-thread", thread.ProjectID, string(thread.ChannelType), thread.ChannelID, thread.ThreadID)
	}
	return a.Store.UpsertConversationThread(ctx, thread)
}

func conversationThreadFromEvent(event domain.Event) domain.ConversationThread {
	event = inferDiscordThreadContext(event)
	projectID := strings.TrimSpace(event.ProjectID)
	channelID := strings.TrimSpace(event.ChannelID)
	threadID := strings.TrimSpace(event.ThreadID)
	if projectID == "" || channelID == "" || threadID == "" {
		return domain.ConversationThread{}
	}
	createdAt := firstNonZeroTime(event.CreatedAt, time.Now().UTC())
	thread := domain.ConversationThread{
		ID:            stableActivityID("conversation-thread", projectID, string(event.ChannelType), channelID, threadID),
		ProjectID:     projectID,
		ChannelType:   event.ChannelType,
		ChannelID:     channelID,
		ThreadID:      threadID,
		RootMessageID: strings.TrimSpace(event.Metadata[domain.MetadataKeyReplyToMessageID]),
		EventID:       strings.TrimSpace(event.ID),
		Title:         strings.TrimSpace(event.Body),
		Metadata: domain.Metadata{
			"source": "event",
		},
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		LastMessageAt: createdAt,
	}
	if thread.RootMessageID == "" && event.ChannelType == domain.ChannelTypeDiscord {
		thread.RootMessageID = threadID
	}
	return thread
}

func cleanReportReply(reply *domain.ReportReply) *domain.ReportReply {
	if reply == nil {
		return nil
	}
	cleaned := domain.ReportReply{
		MessageID: strings.TrimSpace(reply.MessageID),
		ChannelID: strings.TrimSpace(reply.ChannelID),
		ContextID: strings.TrimSpace(reply.ContextID),
	}
	if cleaned.Empty() {
		return nil
	}
	return &cleaned
}
