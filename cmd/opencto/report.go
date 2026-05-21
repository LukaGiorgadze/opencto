package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/domain"
	"github.com/opencto/opencto/internal/observability"
)

type reportCommandOptions struct {
	ConfigPath  string
	ChannelType string
	ChannelID   string
	Message     string
	Attachments []domain.ReportAttachment
}

func runReportCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	openCTORoot, err := resolveReportOpenCTORoot()
	if err != nil {
		return err
	}
	configPath, err := resolveReportConfigPath(openCTORoot)
	if err != nil {
		return err
	}
	options, err := parseReportCommandArgs(args, configPath)
	if err != nil {
		return err
	}
	if err := loadOpenCTORootDotEnv(openCTORoot); err != nil {
		return err
	}
	cfg, err := loadReportConfig(options.ConfigPath)
	if err != nil {
		return err
	}

	logger := observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.JSONLogs, stderr)
	reporters, err := newConfiguredChannelReporter(cfg, nil, logger, domain.ChannelType(options.ChannelType))
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

func resolveReportConfigPath(openCTORoot string) (string, error) {
	if path, ok, err := readInstalledOpenCTOConfigPath(); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}
	openCTORoot = strings.TrimSpace(openCTORoot)
	if openCTORoot == "" {
		return "", fmt.Errorf("OpenCTO root is required")
	}
	return filepath.Join(openCTORoot, "config.json"), nil
}

func resolveReportOpenCTORoot() (string, error) {
	if root := strings.TrimSpace(os.Getenv("OPENCTO_ROOT")); root != "" {
		return filepath.Clean(root), nil
	}
	if root, ok, err := readInstalledOpenCTORootPath(); err != nil {
		return "", err
	} else if ok {
		return root, nil
	}
	root, err := resolveOpenCTORoot()
	if err != nil {
		return "", fmt.Errorf("resolve OpenCTO root: %w", err)
	}
	return root, nil
}

func readInstalledOpenCTOConfigPath() (string, bool, error) {
	return readInstalledPathMarker(installedOpenCTOConfigFilename, "OpenCTO config")
}

func readInstalledOpenCTORootPath() (string, bool, error) {
	return readInstalledPathMarker(installedOpenCTORootFilename, "OpenCTO root")
}

func readInstalledPathMarker(filename, label string) (string, bool, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", false, nil
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(executable), filename))
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s marker: %w", label, err)
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return "", false, fmt.Errorf("%s marker is empty", label)
	}
	return filepath.Clean(path), true, nil
}

func loadOpenCTORootDotEnv(openCTORoot string) error {
	openCTORoot = strings.TrimSpace(openCTORoot)
	if openCTORoot == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(openCTORoot, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open OpenCTO .env: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		name, value, ok := parseDotEnvLine(scanner.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("set env %s from OpenCTO .env: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read OpenCTO .env: %w", err)
	}
	return nil
}

func parseDotEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	name, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", false
	}
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		quote := value[0]
		if (quote == '"' || quote == '\'') && value[len(value)-1] == quote {
			value = value[1 : len(value)-1]
		}
	}
	return name, value, true
}

func parseReportCommandArgs(args []string, configPath string) (reportCommandOptions, error) {
	options := reportCommandOptions{ConfigPath: strings.TrimSpace(configPath)}
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

func loadReportConfig(path string) (config.Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return config.Config{}, fmt.Errorf("root config path is required")
	}
	cfg, err := config.Load(path)
	if err != nil {
		return config.Config{}, fmt.Errorf("load root config %s: %w", path, err)
	}
	return cfg, nil
}
