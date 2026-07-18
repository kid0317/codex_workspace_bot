package config_test

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

func TestLoadUsesEnvironmentPasswordAndDefaults(t *testing.T) {
	t.Setenv("CODEX_WORKSPACE_BOT_DB_PASSWORD", "test-password")
	path := writeConfig(t, `
database:
  host: 127.0.0.1
  name: codex_workspace_bot
  user: codex_workspace_bot
  password_env: CODEX_WORKSPACE_BOT_DB_PASSWORD
`)

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Database.Port != 3306 {
		t.Fatalf("database port = %d, want 3306", cfg.Database.Port)
	}
	if cfg.Database.Password != "test-password" {
		t.Fatal("database password was not loaded from environment")
	}
	if cfg.Server.ListenAddr != "127.0.0.1:8080" {
		t.Fatalf("listen addr = %q", cfg.Server.ListenAddr)
	}
}

func TestLoadReadsOptionalLangfuseCredentialsWithoutMakingIngressConfigFatal(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "test-password")
	t.Setenv("TEST_LANGFUSE_PUBLIC", "pk-test")
	t.Setenv("TEST_LANGFUSE_SECRET", "sk-test")
	path := writeConfig(t, `
database:
  host: 127.0.0.1
  name: codex_workspace_bot
  user: codex_workspace_bot
  password_env: TEST_DB_PASSWORD
observability:
  langfuse:
    enabled: true
    base_url: https://langfuse.local
    public_key_env: TEST_LANGFUSE_PUBLIC
    secret_key_env: TEST_LANGFUSE_SECRET
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Observability.Langfuse.Enabled || cfg.Observability.Langfuse.PublicKey != "pk-test" || cfg.Observability.Langfuse.SecretKey != "sk-test" {
		t.Fatalf("langfuse config = %#v", cfg.Observability.Langfuse)
	}
	if cfg.Observability.Langfuse.ExportTimeoutSeconds != 2 || cfg.Observability.Langfuse.MaxQueueSize != 4096 {
		t.Fatalf("langfuse defaults = %#v", cfg.Observability.Langfuse)
	}
}

func TestLoadAllowsLangfuseBaseURLFromEnvironment(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "test-password")
	t.Setenv("TEST_LANGFUSE_BASE_URL", "https://langfuse.local")
	path := writeConfig(t, `
database:
  host: 127.0.0.1
  name: codex_workspace_bot
  user: codex_workspace_bot
  password_env: TEST_DB_PASSWORD
observability:
  langfuse:
    enabled: true
    base_url_env: TEST_LANGFUSE_BASE_URL
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Observability.Langfuse.BaseURL != "https://langfuse.local" {
		t.Fatalf("base URL = %q", cfg.Observability.Langfuse.BaseURL)
	}
}

func TestLoadRejectsMissingDatabasePassword(t *testing.T) {
	_ = os.Unsetenv("MISSING_DB_PASSWORD")
	path := writeConfig(t, `
database:
  host: 127.0.0.1
  name: codex_workspace_bot
  user: codex_workspace_bot
  password_env: MISSING_DB_PASSWORD
`)

	_, err := config.Load(path)
	if err == nil {
		t.Fatal("Load() error = nil, want missing password error")
	}
}

func TestLoadAppliesAndValidatesLoggingSettings(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	dir := t.TempDir()
	validPath := filepath.Join(dir, "valid.yaml")
	if err := os.WriteFile(validPath, []byte("database:\n  host: localhost\n  name: bot\n  user: bot\n  password_env: TEST_DB_PASSWORD\nlogging:\n  level: debug\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Logging.Dir != "logs" || cfg.Logging.Level != "debug" {
		t.Fatalf("logging = %#v", cfg.Logging)
	}
	invalidPath := filepath.Join(dir, "invalid.yaml")
	if err := os.WriteFile(invalidPath, []byte("database:\n  host: localhost\n  name: bot\n  user: bot\n  password_env: TEST_DB_PASSWORD\nlogging:\n  level: verbose\n  dir: '   '\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(invalidPath); err == nil {
		t.Fatal("Load() accepted invalid logging configuration")
	}
}

func TestLoadAppliesCodexRuntimeDefaultsAndRejectsInvalidCommand(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	path := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
codex:
  command: "   "
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("Load() accepted blank codex command")
	}

	path = writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Codex.Command != "codex" || cfg.Codex.RPCTimeoutSeconds != 30 || cfg.Codex.TurnTimeoutSeconds != 3000 || cfg.Codex.IdleTimeoutSeconds != 3000 || cfg.Codex.GraceSeconds != 10 {
		t.Fatalf("codex defaults = %#v", cfg.Codex)
	}
}

func TestLoadAppliesAndValidatesStreamingCompanionDelay(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	defaultPath := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
`)
	cfg, err := config.Load(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Streaming.CompanionSegmentDelayMS != 400 {
		t.Fatalf("default companion delay = %d, want 400", cfg.Streaming.CompanionSegmentDelayMS)
	}

	validPath := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
streaming:
  companion_segment_delay_ms: 1200
`)
	cfg, err = config.Load(validPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Streaming.CompanionSegmentDelayMS != 1200 {
		t.Fatalf("companion delay = %d, want 1200", cfg.Streaming.CompanionSegmentDelayMS)
	}

	for _, invalid := range []string{"0", "2001"} {
		path := writeConfig(t, "database:\n  host: localhost\n  name: bot\n  user: bot\n  password_env: TEST_DB_PASSWORD\nstreaming:\n  companion_segment_delay_ms: "+invalid+"\n")
		if _, err := config.Load(path); err == nil {
			t.Fatalf("Load() accepted companion_segment_delay_ms=%s", invalid)
		}
	}
}

func TestLoadAppliesAndValidatesS05AttachmentAndActionSettings(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	attachmentKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	actionKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	t.Setenv("TEST_S05_ATTACHMENT_KEY", attachmentKey)
	t.Setenv("TEST_S05_ACTION_KEY", actionKey)

	path := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
attachments:
  resource_ref_keys:
    - version: 1
      key_env: TEST_S05_ATTACHMENT_KEY
feishu_actions:
  enabled: true
  result_keys:
    - version: 1
      key_env: TEST_S05_ACTION_KEY
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Attachments.RootDir != ".codex-workspace-bot/attachments" || cfg.Attachments.MaxFileBytes != 30_000_000 || cfg.Attachments.MaxBatchBytes != 30_000_000 || cfg.Attachments.DownloadTimeoutSeconds != 30 || cfg.Attachments.RetentionDays != 7 || cfg.Attachments.MaxRetentionDays != 30 {
		t.Fatalf("attachment defaults = %#v", cfg.Attachments)
	}
	if len(cfg.Attachments.ResourceRefKeys) != 1 || !bytes.Equal(cfg.Attachments.ResourceRefKeys[0].Key, bytes.Repeat([]byte{1}, 32)) {
		t.Fatalf("attachment keyring = %#v", cfg.Attachments.ResourceRefKeys)
	}
	if !cfg.FeishuActions.Enabled || cfg.FeishuActions.ActionTimeoutSeconds != 20 || len(cfg.FeishuActions.ResultKeys) != 1 || !bytes.Equal(cfg.FeishuActions.ResultKeys[0].Key, bytes.Repeat([]byte{2}, 32)) {
		t.Fatalf("feishu action settings = %#v", cfg.FeishuActions)
	}
}

func TestLoadAllowsAbsoluteS05AttachmentRootAndRejectsInvalidKeys(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	t.Setenv("TEST_S05_ATTACHMENT_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	t.Setenv("TEST_S05_ACTION_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32)))

	validPath := writeConfig(t, "database:\n  host: localhost\n  name: bot\n  user: bot\n  password_env: TEST_DB_PASSWORD\nattachments:\n  root_dir: /tmp/attachments\n  resource_ref_keys: [{version: 1, key_env: TEST_S05_ATTACHMENT_KEY}]\nfeishu_actions:\n  enabled: true\n  result_keys: [{version: 1, key_env: TEST_S05_ACTION_KEY}]")
	if _, err := config.Load(validPath); err != nil {
		t.Fatalf("Load() rejected absolute attachment root: %v", err)
	}
	t.Setenv("TEST_S05_ATTACHMENT_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31)))
	for name, contents := range map[string]string{
		"short key": `attachments:
  root_dir: /tmp/attachments
  resource_ref_keys: [{version: 1, key_env: TEST_S05_ATTACHMENT_KEY}]
feishu_actions:
  enabled: true
  result_keys: [{version: 1, key_env: TEST_S05_ACTION_KEY}]`,
	} {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, "database:\n  host: localhost\n  name: bot\n  user: bot\n  password_env: TEST_DB_PASSWORD\n"+contents)
			if _, err := config.Load(path); err == nil {
				t.Fatal("Load() accepted unsafe S05 configuration")
			}
		})
	}
}

func TestLoadRejectsEnabledScheduleWithoutIndependentKeyrings(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	path := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
schedule:
  enabled: true
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("Load() accepted enabled schedule without keyrings")
	}
}

func TestLoadAppliesAndValidatesScheduleConfiguration(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	payloadKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	ownerKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	t.Setenv("TEST_S06_PAYLOAD_KEY", payloadKey)
	t.Setenv("TEST_S06_OWNER_KEY", ownerKey)

	path := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
schedule:
  enabled: true
  payload_keys: [{version: 2, key_env: TEST_S06_PAYLOAD_KEY}]
  owner_hmac_keys: [{version: 3, key_env: TEST_S06_OWNER_KEY}]
`)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Schedule.Enabled || cfg.Schedule.TickIntervalMS != 1000 || cfg.Schedule.MisfireGraceSeconds != 60 || cfg.Schedule.MaxEnabledTasksPerOwner != 100 || cfg.Schedule.MaxEnabledTasksPerApp != 1000 || cfg.Schedule.MaxPromptDispatchPerTick != 20 {
		t.Fatalf("schedule defaults = %#v", cfg.Schedule)
	}
	if len(cfg.Schedule.PayloadKeys) != 1 || !bytes.Equal(cfg.Schedule.PayloadKeys[0].Key, bytes.Repeat([]byte{3}, 32)) {
		t.Fatalf("payload keyring = %#v", cfg.Schedule.PayloadKeys)
	}
	if len(cfg.Schedule.OwnerHMACKeys) != 1 || !bytes.Equal(cfg.Schedule.OwnerHMACKeys[0].Key, bytes.Repeat([]byte{4}, 32)) {
		t.Fatalf("owner keyring = %#v", cfg.Schedule.OwnerHMACKeys)
	}
}

func TestLoadRejectsInvalidEnabledScriptLimits(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	payloadKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	ownerKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	t.Setenv("TEST_S06_PAYLOAD_KEY", payloadKey)
	t.Setenv("TEST_S06_OWNER_KEY", ownerKey)
	path := writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
schedule:
  enabled: true
  payload_keys: [{version: 1, key_env: TEST_S06_PAYLOAD_KEY}]
  owner_hmac_keys: [{version: 1, key_env: TEST_S06_OWNER_KEY}]
scripts:
  enabled: true
  timeout_seconds: 1801
`)
	if _, err := config.Load(path); err == nil {
		t.Fatal("Load() accepted invalid script limits")
	}
}

func TestValidateScriptCapabilityDoesNotRequireRunnerOrNetworkIsolation(t *testing.T) {
	t.Setenv("TEST_DB_PASSWORD", "password")
	payloadKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32))
	ownerKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	t.Setenv("TEST_S06_PAYLOAD_KEY", payloadKey)
	t.Setenv("TEST_S06_OWNER_KEY", ownerKey)
	_, err := config.Load(writeConfig(t, `
database:
  host: localhost
  name: bot
  user: bot
  password_env: TEST_DB_PASSWORD
schedule:
  enabled: true
  payload_keys: [{version: 1, key_env: TEST_S06_PAYLOAD_KEY}]
  owner_hmac_keys: [{version: 1, key_env: TEST_S06_OWNER_KEY}]
scripts:
  enabled: true
  shell_path: "/bin/sh"
  timeout_seconds: 300
  max_output_bytes: 24576
  max_concurrent: 2
`))
	if err != nil {
		t.Fatalf("Load() error=%v", err)
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
