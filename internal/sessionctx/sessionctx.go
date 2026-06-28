package sessionctx

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Context struct {
	AppID          string
	WorkspaceMode  string
	WorkspaceDir   string
	SessionID      string
	ChannelKey     string
	RoutingKey     string
	ChatType       string
	ChatID         string
	ThreadID       string
	ReceiveID      string
	ReceiveType    string
	SenderID       string
	MessageID      string
	TaskID         string
	TaskName       string
	TargetType     string
	TargetID       string
	SystemSlug     string
	AttachmentsDir string
	MemoryDir      string
	TasksDir       string
	EngineThreadID string
	CurrentTime    time.Time
}

type Writer struct {
	WorkspaceDir string
}

func (w Writer) Write(ctx Context) (string, error) {
	if ctx.CurrentTime.IsZero() {
		ctx.CurrentTime = time.Now()
	}
	ctx.WorkspaceDir = first(ctx.WorkspaceDir, w.WorkspaceDir)
	ctx.MemoryDir = first(ctx.MemoryDir, filepath.Join(ctx.WorkspaceDir, "memory"))
	ctx.TasksDir = first(ctx.TasksDir, filepath.Join(ctx.WorkspaceDir, "tasks"))
	var dir string
	if ctx.SystemSlug != "" {
		if !safeSlug(ctx.SystemSlug) {
			return "", errors.New("system slug 非法")
		}
		dir = filepath.Join(ctx.WorkspaceDir, "sessions", "_system", ctx.SystemSlug)
	} else {
		dir = filepath.Join(ctx.WorkspaceDir, "sessions", ctx.SessionID)
	}
	if ctx.AttachmentsDir == "" {
		ctx.AttachmentsDir = filepath.Join(dir, "attachments")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "SESSION_CONTEXT.md")
	return path, os.WriteFile(path, []byte(render(ctx)), 0o644)
}

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func safeSlug(slug string) bool {
	return slug != "" &&
		slug != "." &&
		slug != ".." &&
		!strings.Contains(slug, "/") &&
		!strings.Contains(slug, `\`) &&
		slugPattern.MatchString(slug)
}

func InjectRouting(prompt string, ctx Context) string {
	return fmt.Sprintf("<system_routing>\napp_id: %s\nchannel_key: %s\ntask_id: %s\n</system_routing>\n%s", ctx.AppID, ctx.ChannelKey, ctx.TaskID, prompt)
}

func render(ctx Context) string {
	var b strings.Builder
	fields := [][2]string{
		{"app_id", ctx.AppID}, {"workspace_mode", ctx.WorkspaceMode}, {"workspace_dir", ctx.WorkspaceDir},
		{"session_id", ctx.SessionID}, {"channel_key", ctx.ChannelKey}, {"routing_key", ctx.RoutingKey},
		{"chat_type", ctx.ChatType}, {"chat_id", ctx.ChatID}, {"thread_id", ctx.ThreadID},
		{"receive_id", ctx.ReceiveID}, {"receive_type", ctx.ReceiveType}, {"sender_id", ctx.SenderID},
		{"message_id", ctx.MessageID}, {"task_id", ctx.TaskID}, {"task_name", ctx.TaskName},
		{"task_target_type", ctx.TargetType}, {"task_target_id", ctx.TargetID},
		{"attachments_dir", ctx.AttachmentsDir}, {"memory_dir", ctx.MemoryDir}, {"tasks_dir", ctx.TasksDir},
		{"engine_thread_id", ctx.EngineThreadID}, {"current_timestamp", ctx.CurrentTime.Format(time.RFC3339Nano)},
	}
	for _, field := range fields {
		if field[1] != "" {
			fmt.Fprintf(&b, "%s: %s\n", field[0], field[1])
		}
	}
	return b.String()
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
