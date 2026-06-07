package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/domain"
)

type reportCommandOptions struct {
	ChannelType string
	ChannelID   string
	ThreadID    string
	Message     string
	Attachments []domain.ReportAttachment
}

func runReportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if commandHelpRequested(args) {
		writeCommandHelp(stdout, "report")
		return nil
	}
	if len(args) == 0 {
		return commandUsageError(stderr, "report", "missing report arguments")
	}
	options, err := parseReportCommandArgs(args)
	if err != nil {
		return commandUsageError(stderr, "report", err.Error())
	}
	env, err := loadCommandEnvironment(stderr)
	if err != nil {
		return err
	}

	reporters, err := newConfiguredChannelReporter(env.Config, nil, env.Logger, domain.ChannelType(options.ChannelType))
	if err != nil {
		return err
	}
	defer reporters.Close()
	eventID, err := domain.NewID()
	if err != nil {
		return err
	}
	event := domain.Event{
		ID:          eventID,
		ProjectID:   defaultProject.ID,
		Kind:        domain.EventKindSystem,
		ChannelType: domain.ChannelType(options.ChannelType),
		ChannelID:   options.ChannelID,
		ThreadID:    options.ThreadID,
		Body:        options.Message,
		Provenance: domain.Provenance{
			Source:     "opencto_report",
			Actor:      "opencto",
			ObservedAt: time.Now().UTC(),
		},
		CreatedAt: time.Now().UTC(),
	}
	report := domain.ReportMessage{
		Text:        options.Message,
		Attachments: append([]domain.ReportAttachment(nil), options.Attachments...),
	}
	if _, err := reporters.Reporter.Report(ctx, event, report); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "report sent")
	return nil
}

func parseReportCommandArgs(args []string) (reportCommandOptions, error) {
	var options reportCommandOptions
	var message []string
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if arg == "--" {
			message = append(message, args[index+1:]...)
			break
		}
		if strings.HasPrefix(arg, "-") {
			name, value, consumed, err := parseReportFlag(args, index)
			if err != nil {
				return reportCommandOptions{}, err
			}
			index += consumed
			switch name {
			case "channel_type", "channel-type":
				options.ChannelType = value
			case "channel_id", "channel-id":
				options.ChannelID = value
			case "thread_id", "thread-id":
				options.ThreadID = value
			case "file":
				options.Attachments = append(options.Attachments, domain.ReportAttachment{Path: value})
			default:
				return reportCommandOptions{}, fmt.Errorf("unknown report flag -%s", name)
			}
			continue
		}
		message = append(message, arg)
	}
	options.Message = strings.TrimSpace(strings.Join(message, " "))
	if options.Message == "" && len(options.Attachments) == 0 {
		return reportCommandOptions{}, fmt.Errorf("report message or -file attachment is required")
	}
	channelType, err := domain.NormalizeChannelType(options.ChannelType)
	if err != nil {
		return reportCommandOptions{}, err
	}
	options.ChannelType = string(channelType)
	options.ChannelID = strings.TrimSpace(options.ChannelID)
	if options.ChannelID == "" {
		return reportCommandOptions{}, fmt.Errorf("channel_id is required")
	}
	options.ThreadID = strings.TrimSpace(options.ThreadID)
	return options, nil
}

func parseReportFlag(args []string, index int) (string, string, int, error) {
	raw := strings.TrimLeft(strings.TrimSpace(args[index]), "-")
	if raw == "" {
		return "", "", 0, fmt.Errorf("empty report flag")
	}
	if name, value, ok := strings.Cut(raw, "="); ok {
		return strings.TrimSpace(name), strings.TrimSpace(value), 0, nil
	}
	if index+1 >= len(args) {
		return "", "", 0, fmt.Errorf("report flag -%s requires a value", raw)
	}
	value := strings.TrimSpace(args[index+1])
	if value == "" {
		return "", "", 0, fmt.Errorf("report flag -%s requires a value", raw)
	}
	return strings.TrimSpace(raw), value, 1, nil
}
