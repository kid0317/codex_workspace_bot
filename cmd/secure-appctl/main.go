package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/secretfile"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func main() {
	fs := flag.NewFlagSet("secure-appctl", flag.ExitOnError)
	configPath := fs.String("config", "/space/config/bot.yaml", "server config")
	name := fs.String("name", "", "app name")
	appID := fs.String("app-id", "", "Feishu app id")
	secretPath := fs.String("secret-file", "", "path to one-line Feishu app secret")
	workspace := fs.String("workspace-dir", "", "absolute workspace directory")
	model := fs.String("model", "", "model")
	effort := fs.String("effort", "high", "reasoning effort")
	enabled := fs.Bool("enabled", true, "enabled")
	_ = fs.Parse(os.Args[1:])
	if *name == "" || *appID == "" || *secretPath == "" || *workspace == "" || *model == "" {
		fail("name, app-id, secret-file, workspace-dir and model are required")
	}
	info, err := os.Stat(*workspace)
	if err != nil || !info.IsDir() {
		fail("workspace-dir must be an existing directory")
	}
	secret, err := secretfile.Read(*secretPath)
	if err != nil {
		fail(err.Error())
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err.Error())
	}
	store, err := storage.Open(context.Background(), cfg.Database.DSN())
	if err != nil {
		fail(err.Error())
	}
	defer store.Close()
	if err := store.Migrate(context.Background(), filepath.Join("migrations")); err != nil {
		fail(err.Error())
	}
	app := storage.App{
		Name:            *name,
		FeishuAppID:     *appID,
		FeishuAppSecret: secret,
		WorkspaceDir:    *workspace,
		WorkspaceMode:   "work",
		Model:           *model,
		ReasoningEffort: *effort,
		Enabled:         *enabled,
	}
	if err := store.UpsertApp(context.Background(), app); err != nil {
		fail(err.Error())
	}
	fmt.Println("updated", *name)
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, "secure-appctl:", message)
	os.Exit(1)
}
