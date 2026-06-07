package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	opencto "github.com/opencto/opencto"
	"github.com/opencto/opencto/internal/config"
	"github.com/opencto/opencto/internal/observability"
	"github.com/opencto/opencto/internal/workspace"
)

type commandEnvironment struct {
	ConfigPath          string
	Config              config.Config
	Logger              *slog.Logger
	Created             []string
	UserEditableCreated []string
	SkillsRoot          string
}

type starterFiles struct {
	Created             []string
	UserEditableCreated []string
}

func loadCommandEnvironment(logOut io.Writer) (commandEnvironment, error) {
	workspaceRoot, err := defaultWorkspaceRoot()
	if err != nil {
		return commandEnvironment{}, err
	}
	if repoRoot, ok, err := devRepoRoot(); err != nil {
		return commandEnvironment{}, err
	} else if ok {
		return loadCommandEnvironmentFromRepo(repoRoot, workspaceRoot, logOut)
	}
	starter, err := ensureStarterFiles(workspaceRoot)
	if err != nil {
		return commandEnvironment{}, err
	}
	if err := loadDotEnv(workspaceRoot); err != nil {
		return commandEnvironment{}, err
	}
	configPath := filepath.Join(workspaceRoot, "config.json")
	env, err := loadCommandEnvironmentWithRoot(configPath, workspaceRoot, logOut)
	if err != nil {
		return commandEnvironment{}, err
	}
	env.Created = starter.Created
	env.UserEditableCreated = starter.UserEditableCreated
	return env, nil
}

func loadCommandEnvironmentFromRepo(repoRoot, workspaceRoot string, logOut io.Writer) (commandEnvironment, error) {
	repoRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return commandEnvironment{}, fmt.Errorf("resolve repo root: %w", err)
	}
	if err := loadDotEnv(repoRoot); err != nil {
		return commandEnvironment{}, err
	}

	env, err := loadCommandEnvironmentWithRoot(filepath.Join(repoRoot, "config.json"), workspaceRoot, logOut)
	if err != nil {
		return commandEnvironment{}, err
	}
	skillsRoot, ok, err := localSkillsRoot(repoRoot)
	if err != nil {
		return commandEnvironment{}, err
	}
	if ok {
		env.SkillsRoot = skillsRoot
	}
	return env, nil
}

func devRepoRoot() (string, bool, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", false, fmt.Errorf("get working directory: %w", err)
	}
	root, err := filepath.Abs(cwd)
	if err != nil {
		return "", false, fmt.Errorf("resolve working directory: %w", err)
	}
	if ok, err := regularFileExists(filepath.Join(root, "config.json")); err != nil || !ok {
		return "", false, err
	}
	if ok, err := directoryExists(filepath.Join(root, "skills")); err != nil || !ok {
		return "", false, err
	}
	if ok, err := hasOpenCTOModule(root); err != nil || !ok {
		return "", false, err
	}
	return root, true, nil
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.Mode().IsRegular(), nil
}

func directoryExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return info.IsDir(), nil
}

func hasOpenCTOModule(root string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read go.mod: %w", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "module" {
			return fields[1] == "github.com/opencto/opencto", nil
		}
	}
	return false, nil
}

func loadCommandEnvironmentWithRoot(configPath, workspaceRoot string, logOut io.Writer) (commandEnvironment, error) {
	resolvedConfigPath, cfg, err := loadConfig(configPath, workspaceRoot)
	if err != nil {
		return commandEnvironment{}, err
	}
	return commandEnvironment{
		ConfigPath: resolvedConfigPath,
		Config:     cfg,
		Logger:     observability.NewLogger(cfg.Observability.LogLevel, cfg.Observability.JSONLogs, logOut),
	}, nil
}

func loadConfig(configPath, workspaceRoot string) (string, config.Config, error) {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return "", config.Config{}, fmt.Errorf("config path is required")
	}

	resolvedConfigPath, err := filepath.Abs(configPath)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("resolve config path: %w", err)
	}
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("load config %s: %w", resolvedConfigPath, err)
	}
	cfg, err = config.WithWorkspaceRoot(cfg, workspaceRoot)
	if err != nil {
		return "", config.Config{}, err
	}
	return resolvedConfigPath, cfg, nil
}

func defaultWorkspaceRoot() (string, error) {
	if value := strings.TrimSpace(os.Getenv(config.EnvOpenCTOWorkspace)); value != "" {
		return workspace.ResolveRoot(value)
	}
	return workspace.DefaultRoot()
}

func ensureStarterFiles(workspaceRoot string) (starterFiles, error) {
	if err := ensureWorkspaceDirs(workspaceRoot); err != nil {
		return starterFiles{}, err
	}

	configPath := filepath.Join(workspaceRoot, "config.json")
	envPath := filepath.Join(workspaceRoot, ".env")
	var starter starterFiles
	if ok, err := writeFileIfMissing(configPath, defaultConfigJSON(), 0o644); err != nil {
		return starterFiles{}, err
	} else if ok {
		starter.Created = append(starter.Created, configPath)
		starter.UserEditableCreated = append(starter.UserEditableCreated, configPath)
	}
	if ok, err := writeFileIfMissing(envPath, []byte(defaultEnvFile()), 0o600); err != nil {
		return starterFiles{}, err
	} else if ok {
		starter.Created = append(starter.Created, envPath)
		starter.UserEditableCreated = append(starter.UserEditableCreated, envPath)
	}
	if skills, err := ensureWorkspaceSkills(workspaceRoot); err != nil {
		return starterFiles{}, err
	} else {
		starter.Created = append(starter.Created, skills...)
	}
	return starter, nil
}

func ensureWorkspaceSkills(workspaceRoot string) ([]string, error) {
	target := filepath.Join(workspaceRoot, "skills")
	if info, err := os.Stat(target); err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace skills path %s is not a directory", target)
		}
		return nil, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat workspace skills %s: %w", target, err)
	}
	if err := copyFS(opencto.SkillsFS(), target); err != nil {
		return nil, err
	}
	return []string{target}, nil
}

func localSkillsRoot(root string) (string, bool, error) {
	path := filepath.Join(root, "skills")
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("stat local skills %s: %w", path, err)
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("local skills path %s is not a directory", path)
	}
	return path, true, nil
}

func copyFS(source fs.FS, target string) error {
	return fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		targetPath := target
		if path != "." {
			targetPath = filepath.Join(target, filepath.FromSlash(path))
		}
		if entry.IsDir() {
			if err := os.MkdirAll(targetPath, 0o755); err != nil {
				return fmt.Errorf("create target directory %s: %w", targetPath, err)
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat embedded file %s: %w", path, err)
		}
		if info.Mode().Type() != 0 {
			return nil
		}
		data, err := fs.ReadFile(source, path)
		if err != nil {
			return fmt.Errorf("read embedded file %s: %w", path, err)
		}
		if err := os.WriteFile(targetPath, data, 0o644); err != nil {
			return fmt.Errorf("write target file %s: %w", targetPath, err)
		}
		return nil
	})
}

func writeFileIfMissing(path string, data []byte, perm os.FileMode) (bool, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("create %s: %w", path, err)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if writeErr != nil {
		return false, fmt.Errorf("write %s: %w", path, writeErr)
	}
	if closeErr != nil {
		return false, fmt.Errorf("close %s: %w", path, closeErr)
	}
	return true, nil
}

func defaultConfigJSON() []byte {
	data, err := json.MarshalIndent(defaultConfig(), "", "  ")
	if err != nil {
		return []byte("{}\n")
	}
	return append(data, '\n')
}

func defaultConfig() config.Config {
	return config.Config{
		Storage: config.StorageConfig{
			Provider: "sqlite",
		},
		LLM: config.LLMConfig{
			Provider:           "openai",
			BaseURL:            "https://api.openai.com/v1",
			ModelReasoning:     "gpt-5.4-nano",
			ModelFast:          "gpt-5.4-nano",
			ModelSummary:       "gpt-5.4-nano",
			ModelTranscription: "gpt-4o-mini-transcribe",
			Bifrost: config.BifrostConfig{
				Enabled: false,
				BaseURL: "http://127.0.0.1:8081/openai",
			},
		},
		Memory: config.MemoryConfig{
			Enabled:          true,
			AutoContextLimit: 5,
			Embedding: config.MemoryEmbeddingConfig{
				Enabled:    true,
				Provider:   "openai",
				Model:      "text-embedding-3-small",
				Dimensions: 1536,
			},
		},
		Conversation: config.ConversationConfig{
			Enabled:               true,
			HistoryLimit:          20,
			MaxContextChars:       20000,
			SummaryEnabled:        true,
			SummaryTriggerChars:   24000,
			SummaryMaxChars:       6000,
			SummaryRecentMessages: 10,
		},
		Temporal: config.TemporalConfig{
			HostPort:                 "127.0.0.1:7233",
			Namespace:                "default",
			TaskQueue:                "opencto",
			ContinueAsNewAfterEvents: 1000,
		},
		Channels: config.ChannelsConfig{
			Discord: config.DiscordConfig{
				Enabled: true,
				OutboundMessages: config.MessageLimitsConfig{
					MaxChars: 2000,
				},
				OutboundAttachments: config.AttachmentLimitsConfig{
					MaxFiles:      10,
					MaxFileBytes:  10 << 20,
					MaxTotalBytes: 25 << 20,
				},
			},
			Telegram: config.TelegramConfig{
				Enabled: false,
				Webhook: config.TelegramWebhookConfig{
					URL:                "",
					ListenAddr:         "127.0.0.1:8082",
					Path:               "/telegram/webhook",
					MaxConnections:     40,
					DropPendingUpdates: false,
				},
				OutboundMessages: config.MessageLimitsConfig{
					MaxChars: 4096,
				},
				OutboundAttachments: config.AttachmentLimitsConfig{
					MaxFiles:      10,
					MaxFileBytes:  50 << 20,
					MaxTotalBytes: 50 << 20,
				},
			},
		},
		Observability: config.ObservabilityConfig{
			LogLevel: "INFO",
			JSONLogs: true,
		},
	}
}

func defaultEnvFile() string {
	return `OPENAI_API_KEY=
BIFROST_API_KEY=sk-bf-opencto-local
DISCORD_TOKEN=
DISCORD_APPLICATION_ID=
TELEGRAM_BOT_TOKEN=
TELEGRAM_WEBHOOK_SECRET=
`
}

func loadDotEnv(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil
	}
	file, err := os.Open(filepath.Join(dir, ".env"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open .env: %w", err)
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
			return fmt.Errorf("set env %s from .env: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read .env: %w", err)
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
