package codexapp

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Config struct {
	RuntimeDir string
	Topology   string
}

type App struct {
	ID           string
	WorkspaceDir string
}

type RuntimeSpec struct {
	AppID         string
	CWD           string
	StateDir      string
	SocketPath    string
	AuthTokenPath string
}

type RuntimeManager interface {
	Resolve(app App) (RuntimeSpec, error)
}

type Client interface{}
type EventNormalizer interface{}
type ApprovalAdapter interface{}
type InterruptAdapter interface{}

func ResolveRuntime(cfg Config, app App) (RuntimeSpec, error) {
	if cfg.Topology == "" {
		cfg.Topology = "per-app"
	}
	if cfg.Topology != "per-app" {
		return RuntimeSpec{}, fmt.Errorf("不支持的 app-server topology: %s", cfg.Topology)
	}
	if app.ID == "" || strings.Contains(app.ID, "/") || strings.Contains(app.ID, `\`) || app.ID == "." || app.ID == ".." || strings.Contains(app.ID, "..") {
		return RuntimeSpec{}, fmt.Errorf("非法 app id: %s", app.ID)
	}
	if cfg.RuntimeDir == "" {
		cfg.RuntimeDir = "./runtime/codex"
	}
	stateDir := filepath.Join(cfg.RuntimeDir, app.ID)
	return RuntimeSpec{
		AppID:         app.ID,
		CWD:           app.WorkspaceDir,
		StateDir:      stateDir,
		SocketPath:    filepath.Join(stateDir, "appserver.sock"),
		AuthTokenPath: filepath.Join(stateDir, "auth.token"),
	}, nil
}
