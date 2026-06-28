package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server      ServerConfig     `yaml:"server"`
	Engine      EngineConfig     `yaml:"engine"`
	Codex       CodexConfig      `yaml:"codex"`
	Claude      ClaudeConfig     `yaml:"claude"`
	Session     SessionConfig    `yaml:"session"`
	Cleanup     CleanupConfig    `yaml:"cleanup"`
	Attachments AttachmentConfig `yaml:"attachments"`
	Guardrails  GuardrailConfig  `yaml:"guardrails"`
	Approval    ApprovalConfig   `yaml:"approval"`
	Apps        []AppConfig      `yaml:"apps"`
}

type ServerConfig struct {
	Port                   int    `yaml:"port" json:"port"`
	DebugEnabled           bool   `yaml:"debug_enabled" json:"debug_enabled"`
	DebugBind              string `yaml:"debug_bind" json:"debug_bind"`
	DebugToken             string `yaml:"debug_token" json:"-"`
	MaxBodyBytes           int64  `yaml:"max_body_bytes" json:"max_body_bytes"`
	AllowNonLocalDebugBind bool   `yaml:"allow_non_local_debug_bind" json:"-"`
}

type EngineConfig struct {
	Type string `yaml:"type" json:"type"`
}

type CodexConfig struct {
	AppServer AppServerConfig `yaml:"app_server" json:"app_server"`
}

type AppServerConfig struct {
	Listen         string `yaml:"listen" json:"listen"`
	Auth           string `yaml:"auth" json:"auth"`
	ApprovalPolicy string `yaml:"approval_policy" json:"approval_policy"`
	SchemaVersion  string `yaml:"schema_version" json:"schema_version"`
	RuntimeDir     string `yaml:"runtime_dir" json:"runtime_dir"`
	Topology       string `yaml:"topology" json:"topology"`
}

type ClaudeConfig struct {
	DefaultProvider string                    `yaml:"default_provider" json:"default_provider"`
	Providers       map[string]ProviderConfig `yaml:"providers" json:"providers"`
}

type ProviderConfig struct {
	BaseURL   string `yaml:"base_url" json:"base_url"`
	AuthToken string `yaml:"auth_token" json:"auth_token"`
	Model     string `yaml:"model" json:"model"`
	Effort    string `yaml:"effort" json:"effort"`
}

type SessionConfig struct {
	WorkerIdleTimeoutMinutes int `yaml:"worker_idle_timeout_minutes" json:"worker_idle_timeout_minutes"`
	QueueSize                int `yaml:"queue_size" json:"queue_size"`
	DuplicateMessageTTLHours int `yaml:"duplicate_message_ttl_hours" json:"duplicate_message_ttl_hours"`
}

type CleanupConfig struct {
	AttachmentsRetentionDays int    `yaml:"attachments_retention_days" json:"attachments_retention_days"`
	AttachmentsMaxDays       int    `yaml:"attachments_max_days" json:"attachments_max_days"`
	Schedule                 string `yaml:"schedule" json:"schedule"`
}

type AttachmentConfig struct {
	PendingTTLMinutes     int    `yaml:"pending_ttl_minutes" json:"pending_ttl_minutes"`
	PendingMaxItems       int    `yaml:"pending_max_items" json:"pending_max_items"`
	MaxBytesPerAttachment int64  `yaml:"max_bytes_per_attachment" json:"max_bytes_per_attachment"`
	TempDir               string `yaml:"temp_dir" json:"temp_dir"`
}

type GuardrailConfig struct {
	MaxMessageBytes        int `yaml:"max_message_bytes" json:"max_message_bytes"`
	MaxOutputBytes         int `yaml:"max_output_bytes" json:"max_output_bytes"`
	MaxEventsPerTurn       int `yaml:"max_events_per_turn" json:"max_events_per_turn"`
	MaxTurnDurationMinutes int `yaml:"max_turn_duration_minutes" json:"max_turn_duration_minutes"`
}

type ApprovalConfig struct {
	MockPolicy     string `yaml:"mock_policy" json:"mock_policy"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type AppConfig struct {
	ID                      string          `yaml:"id" json:"id"`
	FeishuAppID             string          `yaml:"feishu_app_id" json:"feishu_app_id"`
	FeishuAppSecret         string          `yaml:"feishu_app_secret" json:"feishu_app_secret"`
	FeishuVerificationToken string          `yaml:"feishu_verification_token" json:"feishu_verification_token"`
	FeishuEncryptKey        string          `yaml:"feishu_encrypt_key" json:"feishu_encrypt_key"`
	WorkspaceDir            string          `yaml:"workspace_dir" json:"workspace_dir"`
	WorkspaceMode           string          `yaml:"workspace_mode" json:"workspace_mode"`
	AllowedChats            []string        `yaml:"allowed_chats" json:"allowed_chats"`
	Claude                  AppClaudeConfig `yaml:"claude" json:"claude"`
}

type AppClaudeConfig struct {
	PermissionMode string   `yaml:"permission_mode" json:"permission_mode"`
	AllowedTools   []string `yaml:"allowed_tools" json:"allowed_tools"`
	Provider       string   `yaml:"provider" json:"provider"`
	Model          string   `yaml:"model" json:"model"`
	Effort         string   `yaml:"effort" json:"effort"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置: %w", err)
	}
	cfg := defaultConfig()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置: %w", err)
	}
	applyDefaults(&cfg)
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func defaultConfig() Config {
	cfg := Config{}
	applyDefaults(&cfg)
	return cfg
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.DebugBind == "" {
		cfg.Server.DebugBind = "127.0.0.1"
	}
	if cfg.Server.MaxBodyBytes == 0 {
		cfg.Server.MaxBodyBytes = 1 << 20
	}
	if cfg.Engine.Type == "" {
		cfg.Engine.Type = "mock"
	}
	if cfg.Codex.AppServer.Topology == "" {
		cfg.Codex.AppServer.Topology = "per-app"
	}
	if cfg.Codex.AppServer.RuntimeDir == "" {
		cfg.Codex.AppServer.RuntimeDir = "./runtime/codex"
	}
	if cfg.Session.WorkerIdleTimeoutMinutes == 0 {
		cfg.Session.WorkerIdleTimeoutMinutes = 30
	}
	if cfg.Session.QueueSize == 0 {
		cfg.Session.QueueSize = 64
	}
	if cfg.Session.DuplicateMessageTTLHours == 0 {
		cfg.Session.DuplicateMessageTTLHours = 24
	}
	if cfg.Cleanup.AttachmentsRetentionDays == 0 {
		cfg.Cleanup.AttachmentsRetentionDays = 7
	}
	if cfg.Cleanup.AttachmentsMaxDays == 0 {
		cfg.Cleanup.AttachmentsMaxDays = 30
	}
	if cfg.Cleanup.Schedule == "" {
		cfg.Cleanup.Schedule = "0 2 * * *"
	}
	if cfg.Attachments.PendingTTLMinutes == 0 {
		cfg.Attachments.PendingTTLMinutes = 30
	}
	if cfg.Attachments.PendingMaxItems == 0 {
		cfg.Attachments.PendingMaxItems = 20
	}
	if cfg.Attachments.MaxBytesPerAttachment == 0 {
		cfg.Attachments.MaxBytesPerAttachment = 100 << 20
	}
	if cfg.Approval.MockPolicy == "" {
		cfg.Approval.MockPolicy = "auto_allow"
	}
	if cfg.Approval.TimeoutSeconds == 0 {
		cfg.Approval.TimeoutSeconds = 300
	}
	for i := range cfg.Apps {
		if cfg.Apps[i].WorkspaceMode == "" {
			cfg.Apps[i].WorkspaceMode = "work"
		}
	}
}

func (c Config) Validate() error {
	if len(c.Apps) == 0 {
		return fmt.Errorf("配置至少需要一个 app")
	}
	if c.Engine.Type != "mock" && c.Engine.Type != "codex-app-server" {
		return fmt.Errorf("不支持的 engine.type: %s", c.Engine.Type)
	}
	if c.Server.DebugEnabled && !c.Server.AllowNonLocalDebugBind && c.Server.DebugBind != "127.0.0.1" && c.Server.DebugBind != "localhost" {
		return fmt.Errorf("debug_bind 必须默认绑定本机，非本机地址需要显式 opt-in")
	}
	if c.Server.DebugEnabled && c.Server.DebugToken == "" {
		return fmt.Errorf("debug_enabled=true 必须配置 debug_token")
	}
	for _, app := range c.Apps {
		if app.ID == "" || strings.Contains(app.ID, "/") {
			return fmt.Errorf("app id 非法: %q", app.ID)
		}
		if app.FeishuAppID == "" || app.FeishuAppSecret == "" || app.WorkspaceDir == "" {
			return fmt.Errorf("app %s 缺少必填字段", app.ID)
		}
	}
	return nil
}

func (c Config) CountMode(mode string) int {
	n := 0
	for _, app := range c.Apps {
		if app.WorkspaceMode == mode {
			n++
		}
	}
	return n
}

func (c Config) ProviderSet() map[string]bool {
	out := map[string]bool{}
	for name := range c.Claude.Providers {
		out[name] = true
	}
	for _, app := range c.Apps {
		if app.Claude.Provider != "" {
			out[app.Claude.Provider] = true
		}
	}
	return out
}

func (c Config) RedactedString() string {
	copy := c
	copy.Server.DebugToken = redact(copy.Server.DebugToken)
	for name, p := range copy.Claude.Providers {
		if p.AuthToken != "" {
			p.AuthToken = "[REDACTED]"
			copy.Claude.Providers[name] = p
		}
	}
	for i := range copy.Apps {
		copy.Apps[i].FeishuAppID = redact(copy.Apps[i].FeishuAppID)
		copy.Apps[i].FeishuAppSecret = redact(copy.Apps[i].FeishuAppSecret)
		copy.Apps[i].FeishuVerificationToken = redact(copy.Apps[i].FeishuVerificationToken)
		copy.Apps[i].FeishuEncryptKey = redact(copy.Apps[i].FeishuEncryptKey)
	}
	data, _ := json.Marshal(copy)
	return string(data)
}

func redact(v string) string {
	if v == "" {
		return ""
	}
	return "[REDACTED]"
}
