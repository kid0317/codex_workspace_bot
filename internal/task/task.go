package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kid0317/codex-workspace-bot/internal/model"
	"gopkg.in/yaml.v3"
)

type yamlTask struct {
	Name        string `yaml:"name"`
	Cron        string `yaml:"cron"`
	Enabled     *bool  `yaml:"enabled"`
	TargetType  string `yaml:"target_type"`
	TargetID    string `yaml:"target_id"`
	Prompt      string `yaml:"prompt"`
	SendOutput  *bool  `yaml:"send_output"`
	PostArchive bool   `yaml:"post_archive"`
	CreatedBy   string `yaml:"created_by"`
}

func LoadYAML(path, appID string) (model.Task, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Task{}, fmt.Errorf("读取任务 YAML: %w", err)
	}
	var y yamlTask
	if err := yaml.Unmarshal(data, &y); err != nil {
		return model.Task{}, fmt.Errorf("解析任务 YAML: %w", err)
	}
	enabled := true
	if y.Enabled != nil {
		enabled = *y.Enabled
	}
	sendOutput := true
	if y.SendOutput != nil {
		sendOutput = *y.SendOutput
	}
	task := model.Task{
		ID:          appID + "/" + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		AppID:       appID,
		Name:        y.Name,
		CronExpr:    y.Cron,
		TargetType:  y.TargetType,
		TargetID:    y.TargetID,
		Prompt:      y.Prompt,
		Enabled:     enabled,
		SendOutput:  sendOutput,
		PostArchive: y.PostArchive,
		CreatedBy:   y.CreatedBy,
	}
	if err := validate(task); err != nil {
		return model.Task{}, err
	}
	return task, nil
}

func validate(t model.Task) error {
	if t.Enabled && strings.TrimSpace(t.CronExpr) == "" {
		return fmt.Errorf("启用任务必须配置 cron")
	}
	targetSet := t.TargetType != "" && t.TargetID != ""
	targetPartial := (t.TargetType == "") != (t.TargetID == "")
	if targetPartial {
		return fmt.Errorf("target_type 与 target_id 必须同时设置")
	}
	if t.SendOutput && !targetSet {
		return fmt.Errorf("send_output=true 必须指定目标频道")
	}
	if t.PostArchive && (t.SendOutput || !targetSet) {
		return fmt.Errorf("post_archive 只允许 borrow-channel 任务")
	}
	if strings.Contains(t.Prompt, "__PLACEHOLDER__") {
		return fmt.Errorf("任务 prompt 包含未替换占位符")
	}
	return nil
}
