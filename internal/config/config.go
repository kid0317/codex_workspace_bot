package config

import (
	"encoding/base64"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Logging       LoggingConfig       `yaml:"logging"`
	Database      DatabaseConfig      `yaml:"database"`
	Worker        WorkerConfig        `yaml:"worker"`
	Codex         CodexConfig         `yaml:"codex"`
	Streaming     StreamingConfig     `yaml:"streaming"`
	Attachments   AttachmentsConfig   `yaml:"attachments"`
	FeishuActions FeishuActionsConfig `yaml:"feishu_actions"`
	Schedule      ScheduleConfig      `yaml:"schedule"`
	Scripts       ScriptsConfig       `yaml:"scripts"`
	Observability ObservabilityConfig `yaml:"observability"`
}

type ObservabilityConfig struct {
	Langfuse LangfuseConfig `yaml:"langfuse"`
}

// LangfuseConfig intentionally lets the server start in a degraded no-op mode
// if optional observability credentials are absent or invalid. It must never
// cause a valid personal-local Feishu ingress to stop accepting work.
type LangfuseConfig struct {
	Enabled                bool   `yaml:"enabled"`
	BaseURL                string `yaml:"base_url"`
	BaseURLEnv             string `yaml:"base_url_env"`
	PublicKeyEnv           string `yaml:"public_key_env"`
	SecretKeyEnv           string `yaml:"secret_key_env"`
	ProjectID              string `yaml:"project_id"`
	ProjectBindingNonce    string `yaml:"project_binding_nonce"`
	ProjectBindingVerified bool   `yaml:"project_binding_verified"`
	Environment            string `yaml:"environment"`
	ExportTimeoutSeconds   int    `yaml:"export_timeout_seconds"`
	MaxQueueSize           int    `yaml:"max_queue_size"`
	PublicKey              string `yaml:"-"`
	SecretKey              string `yaml:"-"`
}

type KeyConfig struct {
	Version int    `yaml:"version"`
	KeyEnv  string `yaml:"key_env"`
	Key     []byte `yaml:"-"`
}

type AttachmentsConfig struct {
	RootDir                string      `yaml:"root_dir"`
	MaxFileBytes           int64       `yaml:"max_file_bytes"`
	MaxBatchBytes          int64       `yaml:"max_batch_bytes"`
	DownloadTimeoutSeconds int         `yaml:"download_timeout_seconds"`
	RetentionDays          int         `yaml:"retention_days"`
	MaxRetentionDays       int         `yaml:"max_retention_days"`
	ResourceRefKeys        []KeyConfig `yaml:"resource_ref_keys"`
}

type FeishuActionsConfig struct {
	Enabled               bool        `yaml:"enabled"`
	DefaultDocFolderToken string      `yaml:"default_doc_folder_token"`
	ActionTimeoutSeconds  int         `yaml:"action_timeout_seconds"`
	ResultKeys            []KeyConfig `yaml:"result_keys"`
}

// ScheduleConfig is disabled by default so existing installations do not
// accidentally start a scheduler. Once enabled, both independent keyrings are
// mandatory and all task execution is scoped by these limits.
type ScheduleConfig struct {
	Enabled                  bool        `yaml:"enabled"`
	TickIntervalMS           int         `yaml:"tick_interval_ms"`
	MisfireGraceSeconds      int         `yaml:"misfire_grace_seconds"`
	MaxEnabledTasksPerOwner  int         `yaml:"max_enabled_tasks_per_owner"`
	MaxEnabledTasksPerApp    int         `yaml:"max_enabled_tasks_per_app"`
	MaxPromptDispatchPerTick int         `yaml:"max_prompt_dispatch_per_tick"`
	PayloadKeys              []KeyConfig `yaml:"payload_keys"`
	OwnerHMACKeys            []KeyConfig `yaml:"owner_hmac_keys"`
}

// ScriptsConfig controls direct local script commands scheduled by the bot.
// Commands run as the same OS user as the bot process; no descriptor registry,
// privilege drop, or network sandbox is involved in this personal-local mode.
type ScriptsConfig struct {
	Enabled        bool   `yaml:"enabled"`
	ShellPath      string `yaml:"shell_path"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	MaxOutputBytes int64  `yaml:"max_output_bytes"`
	MaxConcurrent  int    `yaml:"max_concurrent"`
}

type WorkerConfig struct {
	MaxWorkers              int `yaml:"max_workers"`
	QueueDepth              int `yaml:"queue_depth"`
	IdleTimeoutMinutes      int `yaml:"idle_timeout_minutes"`
	InProcessTimeoutMinutes int `yaml:"in_process_timeout_minutes"`
	StopGraceSeconds        int `yaml:"stop_grace_seconds"`
}

type CodexConfig struct {
	Command            string `yaml:"command"`
	RPCTimeoutSeconds  int    `yaml:"rpc_timeout_seconds"`
	TurnTimeoutSeconds int    `yaml:"turn_timeout_seconds"`
	IdleTimeoutSeconds int    `yaml:"idle_timeout_seconds"`
	GraceSeconds       int    `yaml:"grace_seconds"`
}

type StreamingConfig struct {
	CompanionSegmentDelayMS int `yaml:"companion_segment_delay_ms"`
	companionDelaySet       bool
}

func (c *StreamingConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		CompanionSegmentDelayMS *int `yaml:"companion_segment_delay_ms"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	if raw.CompanionSegmentDelayMS != nil {
		c.CompanionSegmentDelayMS = *raw.CompanionSegmentDelayMS
		c.companionDelaySet = true
	}
	return nil
}

type ServerConfig struct {
	ListenAddr string `yaml:"listen_addr"`
}

type LoggingConfig struct {
	Level         string `yaml:"level"`
	Dir           string `yaml:"dir"`
	RetentionDays int    `yaml:"retention_days"`
}

type DatabaseConfig struct {
	Host        string `yaml:"host"`
	Port        int    `yaml:"port"`
	Name        string `yaml:"name"`
	User        string `yaml:"user"`
	PasswordEnv string `yaml:"password_env"`
	Password    string `yaml:"-"`
	MaxOpen     int    `yaml:"max_open_conns"`
	MaxIdle     int    `yaml:"max_idle_conns"`
}

func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	applyDefaults(&cfg)
	if err := validate(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.ListenAddr == "" {
		cfg.Server.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.Logging.Level == "" {
		cfg.Logging.Level = "info"
	}
	if strings.TrimSpace(cfg.Logging.Dir) == "" {
		cfg.Logging.Dir = "logs"
	}
	if cfg.Logging.RetentionDays == 0 {
		cfg.Logging.RetentionDays = 30
	}
	if cfg.Database.Port == 0 {
		cfg.Database.Port = 3306
	}
	if cfg.Database.MaxOpen == 0 {
		cfg.Database.MaxOpen = 10
	}
	if cfg.Database.MaxIdle == 0 {
		cfg.Database.MaxIdle = 5
	}
	if cfg.Worker.MaxWorkers == 0 {
		cfg.Worker.MaxWorkers = 20
	}
	if cfg.Worker.QueueDepth == 0 {
		cfg.Worker.QueueDepth = 64
	}
	if cfg.Worker.IdleTimeoutMinutes == 0 {
		cfg.Worker.IdleTimeoutMinutes = 30
	}
	if cfg.Worker.InProcessTimeoutMinutes == 0 {
		cfg.Worker.InProcessTimeoutMinutes = 90
	}
	if cfg.Worker.StopGraceSeconds == 0 {
		cfg.Worker.StopGraceSeconds = 10
	}
	if cfg.Codex.Command == "" {
		cfg.Codex.Command = "codex"
	}
	if cfg.Codex.RPCTimeoutSeconds == 0 {
		cfg.Codex.RPCTimeoutSeconds = 30
	}
	if cfg.Codex.TurnTimeoutSeconds == 0 {
		cfg.Codex.TurnTimeoutSeconds = 3000
	}
	if cfg.Codex.IdleTimeoutSeconds == 0 {
		cfg.Codex.IdleTimeoutSeconds = 3000
	}
	if cfg.Codex.GraceSeconds == 0 {
		cfg.Codex.GraceSeconds = 10
	}
	if !cfg.Streaming.companionDelaySet {
		cfg.Streaming.CompanionSegmentDelayMS = 400
	}
	if cfg.Attachments.RootDir == "" {
		cfg.Attachments.RootDir = ".codex-workspace-bot/attachments"
	}
	if cfg.Attachments.MaxFileBytes == 0 {
		cfg.Attachments.MaxFileBytes = 30_000_000
	}
	if cfg.Attachments.MaxBatchBytes == 0 {
		cfg.Attachments.MaxBatchBytes = 30_000_000
	}
	if cfg.Attachments.DownloadTimeoutSeconds == 0 {
		cfg.Attachments.DownloadTimeoutSeconds = 30
	}
	if cfg.Attachments.RetentionDays == 0 {
		cfg.Attachments.RetentionDays = 7
	}
	if cfg.Attachments.MaxRetentionDays == 0 {
		cfg.Attachments.MaxRetentionDays = 30
	}
	if cfg.FeishuActions.ActionTimeoutSeconds == 0 {
		cfg.FeishuActions.ActionTimeoutSeconds = 20
	}
	if cfg.Schedule.TickIntervalMS == 0 {
		cfg.Schedule.TickIntervalMS = 1000
	}
	if cfg.Schedule.MisfireGraceSeconds == 0 {
		cfg.Schedule.MisfireGraceSeconds = 60
	}
	if cfg.Schedule.MaxEnabledTasksPerOwner == 0 {
		cfg.Schedule.MaxEnabledTasksPerOwner = 100
	}
	if cfg.Schedule.MaxEnabledTasksPerApp == 0 {
		cfg.Schedule.MaxEnabledTasksPerApp = 1000
	}
	if cfg.Schedule.MaxPromptDispatchPerTick == 0 {
		cfg.Schedule.MaxPromptDispatchPerTick = 20
	}
	if cfg.Scripts.ShellPath == "" {
		cfg.Scripts.ShellPath = "/bin/sh"
	}
	if cfg.Scripts.TimeoutSeconds == 0 {
		cfg.Scripts.TimeoutSeconds = 300
	}
	if cfg.Scripts.MaxOutputBytes == 0 {
		cfg.Scripts.MaxOutputBytes = 24 * 1024
	}
	if cfg.Scripts.MaxConcurrent == 0 {
		cfg.Scripts.MaxConcurrent = 2
	}
	if cfg.Observability.Langfuse.ExportTimeoutSeconds == 0 {
		cfg.Observability.Langfuse.ExportTimeoutSeconds = 2
	}
	if cfg.Observability.Langfuse.MaxQueueSize == 0 {
		cfg.Observability.Langfuse.MaxQueueSize = 4096
	}
	if cfg.Observability.Langfuse.Environment == "" {
		cfg.Observability.Langfuse.Environment = "development"
	}
}

func validate(cfg *Config) error {
	if cfg.Logging.Level != "debug" && cfg.Logging.Level != "info" && cfg.Logging.Level != "error" {
		return fmt.Errorf("config: logging level must be debug, info, or error")
	}
	if cfg.Logging.Dir == "" {
		return fmt.Errorf("config: logging dir is required")
	}
	if cfg.Logging.RetentionDays < 1 {
		return fmt.Errorf("config: logging retention_days must be positive")
	}
	if cfg.Database.Host == "" || cfg.Database.Name == "" || cfg.Database.User == "" {
		return fmt.Errorf("config: database host, name, and user are required")
	}
	if cfg.Database.PasswordEnv == "" {
		return fmt.Errorf("config: database password_env is required")
	}
	cfg.Database.Password = os.Getenv(cfg.Database.PasswordEnv)
	if cfg.Database.Password == "" {
		return fmt.Errorf("config: environment variable %s is required", cfg.Database.PasswordEnv)
	}
	if cfg.Observability.Langfuse.Enabled {
		if cfg.Observability.Langfuse.BaseURL == "" && cfg.Observability.Langfuse.BaseURLEnv != "" {
			cfg.Observability.Langfuse.BaseURL = os.Getenv(cfg.Observability.Langfuse.BaseURLEnv)
		}
		cfg.Observability.Langfuse.PublicKey = os.Getenv(cfg.Observability.Langfuse.PublicKeyEnv)
		cfg.Observability.Langfuse.SecretKey = os.Getenv(cfg.Observability.Langfuse.SecretKeyEnv)
	}
	if cfg.Database.Port < 1 || cfg.Database.Port > 65535 {
		return fmt.Errorf("config: database port is invalid")
	}
	if cfg.Database.MaxOpen < 1 || cfg.Database.MaxIdle < 0 || cfg.Database.MaxIdle > cfg.Database.MaxOpen {
		return fmt.Errorf("config: database pool limits are invalid")
	}
	if cfg.Worker.MaxWorkers < 1 || cfg.Worker.QueueDepth < 1 || cfg.Worker.IdleTimeoutMinutes < 1 || cfg.Worker.InProcessTimeoutMinutes < 1 || cfg.Worker.StopGraceSeconds < 1 {
		return fmt.Errorf("config: worker limits must be positive")
	}
	if strings.TrimSpace(cfg.Codex.Command) == "" || cfg.Codex.RPCTimeoutSeconds < 1 || cfg.Codex.TurnTimeoutSeconds < 1 || cfg.Codex.IdleTimeoutSeconds < 1 || cfg.Codex.GraceSeconds < 1 {
		return fmt.Errorf("config: codex runtime settings must be positive and command is required")
	}
	if cfg.Streaming.CompanionSegmentDelayMS < 1 || cfg.Streaming.CompanionSegmentDelayMS > 2000 {
		return fmt.Errorf("config: streaming companion_segment_delay_ms must be between 1 and 2000")
	}
	if strings.TrimSpace(cfg.Attachments.RootDir) == "" {
		return fmt.Errorf("config: attachments root_dir is required")
	}
	if cfg.Attachments.MaxFileBytes < 1 || cfg.Attachments.MaxBatchBytes < cfg.Attachments.MaxFileBytes || cfg.Attachments.DownloadTimeoutSeconds < 1 || cfg.Attachments.RetentionDays < 1 || cfg.Attachments.MaxRetentionDays < cfg.Attachments.RetentionDays {
		return fmt.Errorf("config: attachments limits are invalid")
	}
	if cfg.FeishuActions.ActionTimeoutSeconds < 1 {
		return fmt.Errorf("config: feishu_actions action_timeout_seconds must be positive")
	}
	if cfg.FeishuActions.Enabled {
		if err := loadKeyring("attachments.resource_ref_keys", cfg.Attachments.ResourceRefKeys); err != nil {
			return err
		}
		if err := loadKeyring("feishu_actions.result_keys", cfg.FeishuActions.ResultKeys); err != nil {
			return err
		}
	}
	if cfg.Schedule.Enabled {
		if cfg.Schedule.TickIntervalMS < 100 || cfg.Schedule.TickIntervalMS > 60_000 {
			return fmt.Errorf("config: schedule tick_interval_ms must be between 100 and 60000")
		}
		if cfg.Schedule.MisfireGraceSeconds < 1 || cfg.Schedule.MisfireGraceSeconds > 3600 {
			return fmt.Errorf("config: schedule misfire_grace_seconds must be between 1 and 3600")
		}
		if cfg.Schedule.MaxEnabledTasksPerOwner < 1 || cfg.Schedule.MaxEnabledTasksPerApp < cfg.Schedule.MaxEnabledTasksPerOwner || cfg.Schedule.MaxPromptDispatchPerTick < 1 {
			return fmt.Errorf("config: schedule quotas are invalid")
		}
		if err := loadKeyring("schedule.payload_keys", cfg.Schedule.PayloadKeys); err != nil {
			return err
		}
		if err := loadKeyring("schedule.owner_hmac_keys", cfg.Schedule.OwnerHMACKeys); err != nil {
			return err
		}
	}
	if cfg.Scripts.Enabled {
		if !cfg.Schedule.Enabled {
			return fmt.Errorf("config: scripts requires enabled schedule")
		}
		if err := validateScriptCapability(cfg.Scripts); err != nil {
			return err
		}
	}
	if cfg.Observability.Langfuse.ExportTimeoutSeconds < 1 || cfg.Observability.Langfuse.ExportTimeoutSeconds > 30 || cfg.Observability.Langfuse.MaxQueueSize < 1 || cfg.Observability.Langfuse.MaxQueueSize > 65536 {
		return fmt.Errorf("config: observability langfuse limits are invalid")
	}
	if _, _, err := net.SplitHostPort(cfg.Server.ListenAddr); err != nil {
		return fmt.Errorf("config: server listen_addr: %w", err)
	}
	return nil
}

func loadKeyring(name string, keys []KeyConfig) error {
	if len(keys) == 0 {
		return fmt.Errorf("config: %s is required", name)
	}
	seen := make(map[int]struct{}, len(keys))
	for i := range keys {
		key := &keys[i]
		if key.Version < 1 || strings.TrimSpace(key.KeyEnv) == "" {
			return fmt.Errorf("config: %s entry is invalid", name)
		}
		if _, ok := seen[key.Version]; ok {
			return fmt.Errorf("config: %s key version %d is duplicated", name, key.Version)
		}
		seen[key.Version] = struct{}{}
		encoded := os.Getenv(key.KeyEnv)
		if encoded == "" {
			return fmt.Errorf("config: environment variable %s is required", key.KeyEnv)
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("config: environment variable %s must be base64-encoded 32-byte key", key.KeyEnv)
		}
		key.Key = decoded
	}
	return nil
}

func validateScriptCapability(cfg ScriptsConfig) error {
	if !strings.HasPrefix(cfg.ShellPath, "/") || !isRegularExecutable(cfg.ShellPath) {
		return fmt.Errorf("config: scripts shell_path must be an absolute executable regular file")
	}
	if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 1800 || cfg.MaxOutputBytes < 1 || cfg.MaxOutputBytes > 64*1024 || cfg.MaxConcurrent < 1 {
		return fmt.Errorf("config: scripts limits are invalid")
	}
	return nil
}

func isRegularExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

func (c DatabaseConfig) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local&multiStatements=true&timeout=%s", c.User, c.Password, c.Host, c.Port, c.Name, (5 * time.Second).String())
}
