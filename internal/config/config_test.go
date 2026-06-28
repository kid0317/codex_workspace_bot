package config_test

import (
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

func TestLoadLegacyConfigPreservesShapeAndRedactsSecrets(t *testing.T) {
	cfg, err := config.Load("../../testdata/legacy/config.redacted.yaml")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(cfg.Apps) != 28 {
		t.Fatalf("apps = %d, want 28", len(cfg.Apps))
	}
	if cfg.CountMode("work") != 21 || cfg.CountMode("companion") != 7 {
		t.Fatalf("mode counts work=%d companion=%d", cfg.CountMode("work"), cfg.CountMode("companion"))
	}
	providers := cfg.ProviderSet()
	if !providers["anthropic"] || !providers["bailian"] {
		t.Fatalf("provider set = %#v, want anthropic and bailian", providers)
	}
	if cfg.Engine.Type != "mock" {
		t.Fatalf("engine.type = %q, want mock", cfg.Engine.Type)
	}
	if cfg.Server.DebugBind != "127.0.0.1" {
		t.Fatalf("debug_bind = %q, want localhost", cfg.Server.DebugBind)
	}
	rendered := cfg.RedactedString()
	for _, secret := range []string{"REDACTED_FEISHU_APP_SECRET_ALPHA", "REDACTED_FEISHU_VERIFICATION_TOKEN_ALPHA", "REDACTED_FEISHU_ENCRYPT_KEY_ALPHA", "EXAMPLE_PROVIDER_TOKEN_DO_NOT_USE"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("redacted config leaked secret %q in %s", secret, rendered)
		}
	}
}

func TestLoadCheckedInTemplate(t *testing.T) {
	cfg, err := config.Load("../../config.yaml.template")
	if err != nil {
		t.Fatalf("Load(template) error = %v", err)
	}
	if len(cfg.Apps) != 1 || cfg.Apps[0].ID != "demo-assistant" {
		t.Fatalf("template apps = %#v", cfg.Apps)
	}
	if cfg.Codex.AppServer.Topology != "per-app" {
		t.Fatalf("template topology = %q", cfg.Codex.AppServer.Topology)
	}
}

func TestValidateRejectsUnsafeDebugBindWithoutExplicitOptIn(t *testing.T) {
	cfg := config.Config{
		Server: config.ServerConfig{DebugEnabled: true, DebugBind: "0.0.0.0"},
		Engine: config.EngineConfig{Type: "mock"},
		Apps: []config.AppConfig{{
			ID: "demo", FeishuAppID: "cli_demo", FeishuAppSecret: "secret", WorkspaceDir: t.TempDir(),
		}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() should reject non-local debug bind without opt-in")
	}
	cfg.Server.AllowNonLocalDebugBind = true
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with opt-in error = %v", err)
	}
}
