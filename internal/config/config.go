package config

import (
	"encoding/json"
	"errors"
	"os"
	"strings"

	"github.com/opencto/opencto/internal/workspace"
)

const (
	EnvOpenCTOWorkspace = "OPENCTO_WORKSPACE"

	defaultDiscordOutboundMaxChars      = 2000
	defaultDiscordOutboundMaxFiles      = 10
	defaultDiscordOutboundMaxFileBytes  = 10 << 20
	defaultDiscordOutboundMaxTotalBytes = 25 << 20
	defaultMemoryEmbeddingProvider      = "openai"
	defaultMemoryEmbeddingModel         = "text-embedding-3-small"
	defaultMemoryEmbeddingDimensions    = 1536
)

type Config struct {
	General       GeneralConfig       `json:"general"`
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
	General       GeneralConfig          `json:"general"`
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
	WorkspaceRoot string `json:"workspace_root"`
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
	Enabled            bool                  `json:"enabled"`
	AutoContextLimit   int                   `json:"auto_context_limit"`
	AutoExtractEnabled bool                  `json:"auto_extract_enabled"`
	Embedding          MemoryEmbeddingConfig `json:"embedding"`
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
	Enabled            *bool                     `json:"enabled"`
	AutoContextLimit   int                       `json:"auto_context_limit"`
	AutoExtractEnabled *bool                     `json:"auto_extract_enabled"`
	Embedding          memoryEmbeddingFileConfig `json:"embedding"`
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
	Enabled bool `json:"enabled"`
}

type TemporalConfig struct {
	HostPort                 string `json:"host_port"`
	Namespace                string `json:"namespace"`
	TaskQueue                string `json:"task_queue"`
	ContinueAsNewAfterEvents int    `json:"continue_as_new_after_events"`
}

type ChannelsConfig struct {
	Discord DiscordConfig `json:"discord"`
}

type DiscordConfig struct {
	Enabled             bool                   `json:"enabled"`
	OutboundMessages    MessageLimitsConfig    `json:"outbound_messages"`
	OutboundAttachments AttachmentLimitsConfig `json:"outbound_attachments"`
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
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	var raw fileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, err
	}

	cfg := Config{
		General:      raw.General,
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

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}

	cfg.General.WorkspaceRoot, err = workspace.ResolveRoot(cfg.General.WorkspaceRoot)
	if err != nil {
		return Config{}, err
	}
	cfg.Runtime.StateDir, err = workspace.ResolveStateDir(cfg.Runtime.StateDir, cfg.General.WorkspaceRoot)
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

	requireString(c.General.WorkspaceRoot, "general.workspace_root")
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

	return errors.Join(errs...)
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
		Enabled:            enabled,
		AutoContextLimit:   limit,
		AutoExtractEnabled: defaultBool(value.AutoExtractEnabled, true),
		Embedding:          normalizeMemoryEmbedding(value.Embedding),
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
