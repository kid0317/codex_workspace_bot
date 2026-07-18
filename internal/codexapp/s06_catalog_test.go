package codexapp_test

import (
	"strings"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/codexapp"
)

func TestS06DynamicToolsIncludeFeishuAndStrictScheduleSchemas(t *testing.T) {
	tools := codexapp.S06DynamicTools()
	if len(tools) != 7 {
		t.Fatalf("tool count=%d want 7", len(tools))
	}
	seen := make(map[string]codexapp.DynamicTool, len(tools))
	for _, tool := range tools {
		seen[tool.Namespace+"."+tool.Name] = tool
	}
	for _, name := range []string{"feishu.message_send_current_channel", "feishu.file_upload_and_send_current_channel", "feishu.doc_create_and_announce", "feishu.doc_read", "schedule.list_own", "schedule.create", "schedule.update"} {
		if _, ok := seen[name]; !ok {
			t.Fatalf("missing %s", name)
		}
	}
	if string(seen["schedule.create"].InputSchema) == "" || string(seen["schedule.create"].InputSchema) == string(seen["schedule.update"].InputSchema) {
		t.Fatalf("schedule schemas missing or reused: create=%s update=%s", seen["schedule.create"].InputSchema, seen["schedule.update"].InputSchema)
	}
	if codexapp.S06ToolCatalogVersion != "s06-schedule-v4" {
		t.Fatalf("version=%q", codexapp.S06ToolCatalogVersion)
	}
}

func TestS06UpdateSchemaAcceptsOnlyMatchingListedTaskKind(t *testing.T) {
	for _, tool := range codexapp.S06DynamicTools() {
		if tool.Namespace == "schedule" && tool.Name == "update" {
			schema := string(tool.InputSchema)
			if !strings.Contains(schema, `"kind":{"enum":["prompt","script"]`) || !strings.Contains(schema, "不能用来改变任务类型") {
				t.Fatalf("update schema=%s", schema)
			}
			return
		}
	}
	t.Fatal("schedule.update schema is missing")
}

func TestS06UpdateSchemaUsesIDReturnedByList(t *testing.T) {
	for _, tool := range codexapp.S06DynamicTools() {
		if tool.Namespace == "schedule" && tool.Name == "update" {
			schema := string(tool.InputSchema)
			if !strings.Contains(schema, `"required":["id","version"]`) || !strings.Contains(schema, "不要改名为 task_id") {
				t.Fatalf("update schema=%s", schema)
			}
			return
		}
	}
	t.Fatal("schedule.update schema is missing")
}

func TestS06PromptSchemaExplainsFutureIndependentUserInstruction(t *testing.T) {
	tools := codexapp.S06DynamicTools()
	seen := make(map[string]codexapp.DynamicTool, len(tools))
	for _, tool := range tools {
		seen[tool.Namespace+"."+tool.Name] = tool
	}
	for _, name := range []string{"schedule.create", "schedule.update"} {
		schema := string(seen[name].InputSchema)
		for _, want := range []string{
			"未来执行的 Agent 不会看到创建任务时的对话上下文",
			"使用“请……”的用户口吻",
			"发送到当前飞书会话",
			"避免使用“你、我、这里、刚才”",
		} {
			if !strings.Contains(schema, want) {
				t.Fatalf("%s prompt description is missing %q: %s", name, want, schema)
			}
		}
	}
}

func TestS06ScriptSchemaUsesDirectCommandInsteadOfScriptRegistry(t *testing.T) {
	tools := codexapp.S06DynamicTools()
	seen := make(map[string]codexapp.DynamicTool, len(tools))
	for _, tool := range tools {
		seen[tool.Namespace+"."+tool.Name] = tool
	}
	for _, name := range []string{"schedule.create", "schedule.update"} {
		schema := string(seen[name].InputSchema)
		if !strings.Contains(schema, `"command"`) || strings.Contains(schema, `"script_id":{"`) || !strings.Contains(schema, "管理员登记") {
			t.Fatalf("%s script schema=%s", name, schema)
		}
	}
}
