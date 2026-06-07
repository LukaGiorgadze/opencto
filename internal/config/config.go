package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/opencto/opencto/internal/workspace"
)

const (
	EnvOpenCTOWorkspace = "OPENCTO_WORKSPACE"

	localBifrostOpenAIBaseURL            = "http://127.0.0.1:8081/openai"
	defaultDiscordOutboundMaxChars       = 2000
	defaultDiscordOutboundMaxFiles       = 10
	defaultDiscordOutboundMaxFileBytes   = 10 << 20
	defaultDiscordOutboundMaxTotalBytes  = 25 << 20
	defaultTelegramOutboundMaxChars      = 4096
	defaultTelegramOutboundMaxFiles      = 10
	defaultTelegramOutboundMaxFileBytes  = 50 << 20
	defaultTelegramOutboundMaxTotalBytes = 50 << 20
	defaultTelegramWebhookListenAddr     = "127.0.0.1:8082"
	defaultTelegramWebhookPath           = "/telegram/webhook"
	defaultTelegramWebhookMaxConnections = 40
	defaultMemoryEmbeddingProvider       = "openai"
	defaultMemoryEmbeddingModel          = "text-embedding-3-small"
	defaultMemoryEmbeddingDimensions     = 1536
)

type Config struct {
	General       GeneralConfig       `json:"-"`
	Project       ProjectConfig       `json:"project"`
	Runtime       RuntimeConfig       `json:"runtime"`
	Storage       StorageConfig       `json:"storage"`
	Memory        MemoryConfig        `json:"memory"`
	Conversation  ConversationConfig  `json:"conversation"`
	LLM           LLMConfig           `json:"llm"`
	Temporal      TemporalConfig      `json:"temporal"`
	Channels      ChannelsConfig      `json:"channels"`
	Observability ObservabilityConfig `json:"observability"`
}

type fileConfig struct {
	Project       ProjectConfig          `json:"project"`
	Runtime       RuntimeConfig          `json:"runtime"`
	Storage       StorageConfig          `json:"storage"`
	Memory        memoryFileConfig       `json:"memory"`
	Conversation  conversationFileConfig `json:"conversation"`
	LLM           llmFileConfig          `json:"llm"`
	Temporal      TemporalConfig         `json:"temporal"`
	Channels      ChannelsConfig         `json:"channels"`
	Observability ObservabilityConfig    `json:"observability"`
}

type llmFileConfig struct {
	Provider           string        `json:"provider"`
	BaseURL            string        `json:"base_url"`
	ModelReasoning     string        `json:"model_reasoning"`
	ModelFast          string        `json:"model_fast"`
	ModelSummary       string        `json:"model_summary"`
	ModelTranscription string        `json:"model_transcription"`
	TranscriptionModel string        `json:"transcription_model"`
	APIKey             string        `json:"api_key"`
	Bifrost            BifrostConfig `json:"bifrost"`
}

type GeneralConfig struct {
	WorkspaceRoot string
}

type ProjectConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type RuntimeConfig struct {
	StateDir string `json:"state_dir"`
}

type StorageConfig struct {
	Provider string `json:"provider"`
}

type MemoryConfig struct {
	Enabled          bool                  `json:"enabled"`
	AutoContextLimit int                   `json:"auto_context_limit"`
	Embedding        MemoryEmbeddingConfig `json:"embedding"`
}

type ConversationConfig struct {
	Enabled               bool `json:"enabled"`
	HistoryLimit          int  `json:"history_limit"`
	MaxContextChars       int  `json:"max_context_chars"`
	SummaryEnabled        bool `json:"summary_enabled"`
	SummaryTriggerChars   int  `json:"summary_trigger_chars"`
	SummaryMaxChars       int  `json:"summary_max_chars"`
	SummaryRecentMessages int  `json:"summary_recent_messages"`
}

type memoryFileConfig struct {
	Enabled          *bool                     `json:"enabled"`
	AutoContextLimit int                       `json:"auto_context_limit"`
	Embedding        memoryEmbeddingFileConfig `json:"embedding"`
}

type memoryEmbeddingFileConfig struct {
	Enabled    *bool  `json:"enabled"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

type MemoryEmbeddingConfig struct {
	Enabled    bool   `json:"enabled"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	Dimensions int    `json:"dimensions"`
}

type conversationFileConfig struct {
	Enabled               *bool `json:"enabled"`
	HistoryLimit          int   `json:"history_limit"`
	MaxContextChars       int   `json:"max_context_chars"`
	SummaryEnabled        *bool `json:"summary_enabled"`
	SummaryTriggerChars   int   `json:"summary_trigger_chars"`
	SummaryMaxChars       int   `json:"summary_max_chars"`
	SummaryRecentMessages int   `json:"summary_recent_messages"`
}

type LLMConfig struct {
	Provider           string        `json:"provider"`
	BaseURL            string        `json:"base_url"`
	ModelReasoning     string        `json:"model_reasoning"`
	ModelFast          string        `json:"model_fast"`
	ModelSummary       string        `json:"model_summary"`
	ModelTranscription string        `json:"model_transcription"`
	APIKey             string        `json:"api_key"`
	Bifrost            BifrostConfig `json:"bifrost"`
}

type BifrostConfig struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
}

type TemporalConfig struct {
	HostPort                 string `json:"host_port"`
	Namespace                string `json:"namespace"`
	TaskQueue                string `json:"task_queue"`
	ContinueAsNewAfterEvents int    `json:"continue_as_new_after_events"`
}

type ChannelsConfig struct {
	Discord  DiscordConfig  `json:"discord"`
	Telegram TelegramConfig `json:"telegram"`
}

type DiscordConfig struct {
	Enabled             bool                   `json:"enabled"`
	OutboundMessages    MessageLimitsConfig    `json:"outbound_messages"`
	OutboundAttachments AttachmentLimitsConfig `json:"outbound_attachments"`
}

type TelegramConfig struct {
	Enabled             bool                   `json:"enabled"`
	Webhook             TelegramWebhookConfig  `json:"webhook"`
	OutboundMessages    MessageLimitsConfig    `json:"outbound_messages"`
	OutboundAttachments AttachmentLimitsConfig `json:"outbound_attachments"`
}

type TelegramWebhookConfig struct {
	URL                string `json:"url"`
	ListenAddr         string `json:"listen_addr"`
	Path               string `json:"path"`
	MaxConnections     int    `json:"max_connections"`
	DropPendingUpdates bool   `json:"drop_pending_updates"`
}

type MessageLimitsConfig struct {
	MaxChars int `json:"max_chars"`
}

type AttachmentLimitsConfig struct {
	MaxFiles      int   `json:"max_files"`
	MaxFileBytes  int64 `json:"max_file_bytes"`
	MaxTotalBytes int64 `json:"max_total_bytes"`
}

type ObservabilityConfig struct {
	LogLevel string `json:"log_level"`
	JSONLogs bool   `json:"json_logs"`
}

func Load(path string) (Config, error) {
	configPath, err := filepath.Abs(path)
	if err != nil {
		return Config{}, fmt.Errorf("resolve config path: %w", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, err
	}

	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Project:      raw.Project,
		Runtime:      raw.Runtime,
		Storage:      normalizeStorage(raw.Storage),
		Memory:       normalizeMemory(raw.Memory),
		Conversation: normalizeConversation(raw.Conversation),
		LLM: LLMConfig{
			Provider:           raw.LLM.Provider,
			BaseURL:            raw.LLM.BaseURL,
			ModelReasoning:     raw.LLM.ModelReasoning,
			ModelFast:          raw.LLM.ModelFast,
			ModelSummary:       firstNonEmpty(raw.LLM.ModelSummary, raw.LLM.ModelFast),
			ModelTranscription: firstNonEmpty(raw.LLM.ModelTranscription, raw.LLM.TranscriptionModel),
			APIKey:             raw.LLM.APIKey,
			Bifrost:            raw.LLM.Bifrost,
		},
		Temporal:      raw.Temporal,
		Channels:      raw.Channels,
		Observability: raw.Observability,
	}
	cfg.Channels.Discord.OutboundMessages = normalizeMessageLimits(cfg.Channels.Discord.OutboundMessages, MessageLimitsConfig{
		MaxChars: defaultDiscordOutboundMaxChars,
	})
	cfg.Channels.Discord.OutboundAttachments = normalizeAttachmentLimits(cfg.Channels.Discord.OutboundAttachments, AttachmentLimitsConfig{
		MaxFiles:      defaultDiscordOutboundMaxFiles,
		MaxFileBytes:  defaultDiscordOutboundMaxFileBytes,
		MaxTotalBytes: defaultDiscordOutboundMaxTotalBytes,
	})
	cfg.Channels.Telegram.Webhook = normalizeTelegramWebhook(cfg.Channels.Telegram.Webhook)
	cfg.Channels.Telegram.OutboundMessages = normalizeMessageLimits(cfg.Channels.Telegram.OutboundMessages, MessageLimitsConfig{
		MaxChars: defaultTelegramOutboundMaxChars,
	})
	cfg.Channels.Telegram.OutboundAttachments = normalizeAttachmentLimits(cfg.Channels.Telegram.OutboundAttachments, AttachmentLimitsConfig{
		MaxFiles:      defaultTelegramOutboundMaxFiles,
		MaxFileBytes:  defaultTelegramOutboundMaxFileBytes,
		MaxTotalBytes: defaultTelegramOutboundMaxTotalBytes,
	})
	cfg.LLM = normalizeLLMConfig(cfg.LLM)

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	if strings.TrimSpace(cfg.Runtime.StateDir) != "" {
		cfg.Runtime.StateDir, err = workspace.ResolveStateDirWithBase(cfg.Runtime.StateDir, ".", filepath.Dir(configPath))
		if err != nil {
			return Config{}, err
		}
	}

	return cfg, nil
}

func WithWorkspaceRoot(cfg Config, workspaceRoot string) (Config, error) {
	root, err := workspace.ResolveRoot(workspaceRoot)
	if err != nil {
		return Config{}, err
	}
	cfg.General.WorkspaceRoot = root
	cfg.Runtime.StateDir, err = workspace.ResolveStateDir(cfg.Runtime.StateDir, root)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var missing []string

	requireString := func(value, field string) {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, field)
		}
	}

	requireString(c.LLM.Provider, "llm.provider")
	requireString(c.LLM.BaseURL, "llm.base_url")
	requireString(c.LLM.ModelReasoning, "llm.model_reasoning")
	requireString(c.LLM.ModelFast, "llm.model_fast")
	requireString(c.LLM.ModelSummary, "llm.model_summary")
	requireString(c.LLM.ModelTranscription, "llm.model_transcription")
	requireString(c.Temporal.HostPort, "temporal.host_port")
	requireString(c.Temporal.Namespace, "temporal.namespace")
	requireString(c.Temporal.TaskQueue, "temporal.task_queue")
	requireString(c.Observability.LogLevel, "observability.log_level")

	var errs []error
	if len(missing) > 0 {
		errs = append(errs, errors.New("missing required config values: "+strings.Join(missing, ", ")))
	}
	if c.Temporal.ContinueAsNewAfterEvents <= 0 {
		errs = append(errs, errors.New("temporal.continue_as_new_after_events must be greater than 0"))
	}
	if c.Storage.Provider != "sqlite" {
		errs = append(errs, errors.New("storage.provider must be sqlite"))
	}
	if c.Memory.AutoContextLimit < 1 || c.Memory.AutoContextLimit > 20 {
		errs = append(errs, errors.New("memory.auto_context_limit must be between 1 and 20"))
	}
	if !c.LLM.Bifrost.Enabled && isLocalBifrostOpenAIBaseURL(c.LLM.BaseURL) {
		errs = append(errs, errors.New(`llm.bifrost.enabled is false but llm.base_url points at the local Bifrost gateway; set llm.base_url to "https://api.openai.com/v1" for direct OpenAI calls, or set llm.bifrost.enabled to true and provide BIFROST_API_KEY`))
	}
	if c.LLM.Bifrost.Enabled && !isAbsoluteHTTPURL(c.LLM.Bifrost.BaseURL) {
		errs = append(errs, errors.New("llm.bifrost.base_url must be an absolute http or https URL when Bifrost is enabled"))
	}
	if c.Memory.Embedding.Enabled {
		if c.Memory.Embedding.Provider != "openai" {
			errs = append(errs, errors.New("memory.embedding.provider must be openai"))
		}
		if strings.TrimSpace(c.Memory.Embedding.Model) == "" {
			errs = append(errs, errors.New("memory.embedding.model is required when memory embeddings are enabled"))
		}
		if c.Memory.Embedding.Dimensions != defaultMemoryEmbeddingDimensions {
			errs = append(errs, errors.New("memory.embedding.dimensions must be 1536"))
		}
	}
	if c.Conversation.HistoryLimit < 1 || c.Conversation.HistoryLimit > 50 {
		errs = append(errs, errors.New("conversation.history_limit must be between 1 and 50"))
	}
	if c.Conversation.MaxContextChars < 1 || c.Conversation.MaxContextChars > 100000 {
		errs = append(errs, errors.New("conversation.max_context_chars must be between 1 and 100000"))
	}
	if c.Conversation.SummaryTriggerChars < 1000 || c.Conversation.SummaryTriggerChars > 500000 {
		errs = append(errs, errors.New("conversation.summary_trigger_chars must be between 1000 and 500000"))
	}
	if c.Conversation.SummaryMaxChars < 1000 || c.Conversation.SummaryMaxChars > 50000 {
		errs = append(errs, errors.New("conversation.summary_max_chars must be between 1000 and 50000"))
	}
	if c.Conversation.SummaryRecentMessages < 1 || c.Conversation.SummaryRecentMessages > 50 {
		errs = append(errs, errors.New("conversation.summary_recent_messages must be between 1 and 50"))
	}
	if err := validateDiscordMessageLimits(c.Channels.Discord.OutboundMessages); err != nil {
		errs = append(errs, err)
	}
	if err := validateDiscordAttachmentLimits(c.Channels.Discord.OutboundAttachments); err != nil {
		errs = append(errs, err)
	}
	if c.Channels.Telegram.Enabled {
		if err := validateTelegramWebhook(c.Channels.Telegram.Webhook); err != nil {
			errs = append(errs, err)
		}
		if err := validateTelegramMessageLimits(c.Channels.Telegram.OutboundMessages); err != nil {
			errs = append(errs, err)
		}
		if err := validateTelegramAttachmentLimits(c.Channels.Telegram.OutboundAttachments); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

func normalizeLLMConfig(value LLMConfig) LLMConfig {
	value.BaseURL = strings.TrimSpace(value.BaseURL)
	value.Bifrost.BaseURL = strings.TrimSpace(value.Bifrost.BaseURL)
	if value.Bifrost.BaseURL == "" {
		value.Bifrost.BaseURL = localBifrostOpenAIBaseURL
	}
	if value.Bifrost.Enabled {
		value.BaseURL = value.Bifrost.BaseURL
	}
	return value
}

func isAbsoluteHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return parsed.IsAbs() && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func isLocalBifrostOpenAIBaseURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	if strings.Trim(parsed.EscapedPath(), "/") != "openai" {
		return false
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") {
		return parsed.Port() == "8081"
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && parsed.Port() == "8081"
}

func normalizeStorage(value StorageConfig) StorageConfig {
	if strings.TrimSpace(value.Provider) == "" {
		value.Provider = "sqlite"
	}
	value.Provider = strings.ToLower(strings.TrimSpace(value.Provider))
	return value
}

func normalizeMemory(value memoryFileConfig) MemoryConfig {
	enabled := true
	if value.Enabled != nil {
		enabled = *value.Enabled
	}
	limit := value.AutoContextLimit
	if limit == 0 {
		limit = 5
	}
	return MemoryConfig{
		Enabled:          enabled,
		AutoContextLimit: limit,
		Embedding:        normalizeMemoryEmbedding(value.Embedding),
	}
}

func defaultBool(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func normalizeMemoryEmbedding(value memoryEmbeddingFileConfig) MemoryEmbeddingConfig {
	enabled := true
	if value.Enabled != nil {
		enabled = *value.Enabled
	}
	provider := strings.ToLower(strings.TrimSpace(value.Provider))
	if provider == "" {
		provider = defaultMemoryEmbeddingProvider
	}
	model := strings.TrimSpace(value.Model)
	if model == "" {
		model = defaultMemoryEmbeddingModel
	}
	dimensions := value.Dimensions
	if dimensions == 0 {
		dimensions = defaultMemoryEmbeddingDimensions
	}
	return MemoryEmbeddingConfig{
		Enabled:    enabled,
		Provider:   provider,
		Model:      model,
		Dimensions: dimensions,
	}
}

func normalizeConversation(value conversationFileConfig) ConversationConfig {
	enabled := true
	if value.Enabled != nil {
		enabled = *value.Enabled
	}
	limit := value.HistoryLimit
	if limit == 0 {
		limit = 20
	}
	maxChars := value.MaxContextChars
	if maxChars == 0 {
		maxChars = 20000
	}
	triggerChars := value.SummaryTriggerChars
	if triggerChars == 0 {
		triggerChars = 24000
	}
	summaryMaxChars := value.SummaryMaxChars
	if summaryMaxChars == 0 {
		summaryMaxChars = 6000
	}
	recentMessages := value.SummaryRecentMessages
	if recentMessages == 0 {
		recentMessages = 10
	}
	return ConversationConfig{
		Enabled:               enabled,
		HistoryLimit:          limit,
		MaxContextChars:       maxChars,
		SummaryEnabled:        defaultBool(value.SummaryEnabled, true),
		SummaryTriggerChars:   triggerChars,
		SummaryMaxChars:       summaryMaxChars,
		SummaryRecentMessages: recentMessages,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeMessageLimits(value, defaults MessageLimitsConfig) MessageLimitsConfig {
	if value.MaxChars == 0 {
		value.MaxChars = defaults.MaxChars
	}
	return value
}

func normalizeAttachmentLimits(value, defaults AttachmentLimitsConfig) AttachmentLimitsConfig {
	if value.MaxFiles == 0 {
		value.MaxFiles = defaults.MaxFiles
	}
	if value.MaxFileBytes == 0 {
		value.MaxFileBytes = defaults.MaxFileBytes
	}
	if value.MaxTotalBytes == 0 {
		value.MaxTotalBytes = defaults.MaxTotalBytes
	}
	return value
}

func normalizeTelegramWebhook(value TelegramWebhookConfig) TelegramWebhookConfig {
	value.URL = strings.TrimSpace(value.URL)
	value.ListenAddr = strings.TrimSpace(value.ListenAddr)
	if value.ListenAddr == "" {
		value.ListenAddr = defaultTelegramWebhookListenAddr
	}
	value.Path = strings.TrimSpace(value.Path)
	if value.Path == "" {
		value.Path = defaultTelegramWebhookPath
	}
	if !strings.HasPrefix(value.Path, "/") {
		value.Path = "/" + value.Path
	}
	if value.MaxConnections == 0 {
		value.MaxConnections = defaultTelegramWebhookMaxConnections
	}
	return value
}

func validateDiscordMessageLimits(value MessageLimitsConfig) error {
	if value.MaxChars < 1 || value.MaxChars > defaultDiscordOutboundMaxChars {
		return errors.New("channels.discord.outbound_messages.max_chars must be between 1 and 2000")
	}
	return nil
}

func validateDiscordAttachmentLimits(value AttachmentLimitsConfig) error {
	var errs []error
	if value.MaxFiles < 1 || value.MaxFiles > defaultDiscordOutboundMaxFiles {
		errs = append(errs, errors.New("channels.discord.outbound_attachments.max_files must be between 1 and 10"))
	}
	if value.MaxFileBytes <= 0 {
		errs = append(errs, errors.New("channels.discord.outbound_attachments.max_file_bytes must be greater than 0"))
	}
	if value.MaxTotalBytes <= 0 {
		errs = append(errs, errors.New("channels.discord.outbound_attachments.max_total_bytes must be greater than 0"))
	}
	if value.MaxFileBytes > 0 && value.MaxTotalBytes > 0 && value.MaxFileBytes > value.MaxTotalBytes {
		errs = append(errs, errors.New("channels.discord.outbound_attachments.max_file_bytes must not exceed max_total_bytes"))
	}
	return errors.Join(errs...)
}

func validateTelegramWebhook(value TelegramWebhookConfig) error {
	var errs []error
	if strings.TrimSpace(value.ListenAddr) == "" {
		errs = append(errs, errors.New("channels.telegram.webhook.listen_addr is required"))
	}
	path := strings.TrimSpace(value.Path)
	if path == "" || !strings.HasPrefix(path, "/") {
		errs = append(errs, errors.New("channels.telegram.webhook.path must start with /"))
	}
	if value.MaxConnections < 1 || value.MaxConnections > 100 {
		errs = append(errs, errors.New("channels.telegram.webhook.max_connections must be between 1 and 100"))
	}
	if rawURL := strings.TrimSpace(value.URL); rawURL != "" {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			errs = append(errs, errors.New("channels.telegram.webhook.url must be an absolute URL"))
		} else if parsed.Scheme != "https" {
			errs = append(errs, errors.New("channels.telegram.webhook.url must use https"))
		}
	}
	return errors.Join(errs...)
}

func validateTelegramMessageLimits(value MessageLimitsConfig) error {
	if value.MaxChars < 1 || value.MaxChars > defaultTelegramOutboundMaxChars {
		return errors.New("channels.telegram.outbound_messages.max_chars must be between 1 and 4096")
	}
	return nil
}

func validateTelegramAttachmentLimits(value AttachmentLimitsConfig) error {
	var errs []error
	if value.MaxFiles < 1 || value.MaxFiles > defaultTelegramOutboundMaxFiles {
		errs = append(errs, errors.New("channels.telegram.outbound_attachments.max_files must be between 1 and 10"))
	}
	if value.MaxFileBytes <= 0 {
		errs = append(errs, errors.New("channels.telegram.outbound_attachments.max_file_bytes must be greater than 0"))
	}
	if value.MaxTotalBytes <= 0 {
		errs = append(errs, errors.New("channels.telegram.outbound_attachments.max_total_bytes must be greater than 0"))
	}
	if value.MaxFileBytes > 0 && value.MaxTotalBytes > 0 && value.MaxFileBytes > value.MaxTotalBytes {
		errs = append(errs, errors.New("channels.telegram.outbound_attachments.max_file_bytes must not exceed max_total_bytes"))
	}
	return errors.Join(errs...)
}
