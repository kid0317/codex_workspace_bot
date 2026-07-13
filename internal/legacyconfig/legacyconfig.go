package legacyconfig

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

const (
	defaultModel           = "gpt-5.6-terra"
	defaultReasoningEffort = "medium"
)

// App is the subset of a legacy CC Workspace Bot application configuration
// that Story 1 persists. Its secret must never be written to logs.
type App struct {
	Name            string
	FeishuAppID     string
	FeishuAppSecret string
	WorkspaceDir    string
	WorkspaceMode   string
	Model           string
	ReasoningEffort string
}

type legacyFile struct {
	Apps []legacyApp `yaml:"apps"`
}

type legacyApp struct {
	ID              string `yaml:"id"`
	FeishuAppID     string `yaml:"feishu_app_id"`
	FeishuAppSecret string `yaml:"feishu_app_secret"`
	WorkspaceDir    string `yaml:"workspace_dir"`
	WorkspaceMode   string `yaml:"workspace_mode"`
	Claude          struct {
		Model string `yaml:"model"`
	} `yaml:"claude"`
}

func LoadApp(path, name string) (App, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return App{}, fmt.Errorf("read legacy config: %w", err)
	}
	var config legacyFile
	if err := yaml.Unmarshal(data, &config); err != nil {
		return App{}, fmt.Errorf("parse legacy config: %w", err)
	}
	for _, candidate := range config.Apps {
		if candidate.ID != name {
			continue
		}
		if candidate.FeishuAppID == "" || candidate.FeishuAppSecret == "" || candidate.WorkspaceDir == "" {
			return App{}, fmt.Errorf("legacy app %q has required fields missing", name)
		}
		workspaceMode := candidate.WorkspaceMode
		if workspaceMode == "" {
			workspaceMode = "work"
		}
		return App{
			Name:            candidate.ID,
			FeishuAppID:     candidate.FeishuAppID,
			FeishuAppSecret: candidate.FeishuAppSecret,
			WorkspaceDir:    candidate.WorkspaceDir,
			WorkspaceMode:   workspaceMode,
			// A legacy Claude model name is not a valid Codex model identifier.
			// Imported Apps use the current local Codex default; appctl create/update
			// remains the explicit per-App override surface.
			Model:           defaultModel,
			ReasoningEffort: defaultReasoningEffort,
		}, nil
	}
	return App{}, fmt.Errorf("legacy app %q not found", name)
}
