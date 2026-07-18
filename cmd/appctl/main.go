package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kid0317/codex-workspace-bot/internal/appimport"
	"github.com/kid0317/codex-workspace-bot/internal/config"
	"github.com/kid0317/codex-workspace-bot/internal/legacyconfig"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "server config")
	name := fs.String("name", "", "app name")
	legacyPath := fs.String("legacy-config", "", "legacy config")
	enabled := fs.Bool("enabled", true, "enabled")
	appID := fs.String("app-id", "", "Feishu app id")
	secret := fs.String("secret", "", "Feishu app secret")
	workspace := fs.String("workspace-dir", "", "absolute workspace directory")
	model := fs.String("model", "gpt-5.6-terra", "model")
	effort := fs.String("effort", "medium", "reasoning effort")
	_ = fs.Parse(args)
	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	store, err := storage.Open(context.Background(), cfg.Database.DSN())
	if err != nil {
		fail(err)
	}
	defer store.Close()
	if err := store.Migrate(context.Background(), filepath.Join("migrations")); err != nil {
		fail(err)
	}
	switch cmd {
	case "import-legacy-app":
		if *name == "" || *legacyPath == "" {
			usage()
		}
		app, err := legacyconfig.LoadApp(*legacyPath, *name)
		if err != nil {
			fail(err)
		}
		if err = appimport.Import(context.Background(), store, app); err != nil {
			fail(err)
		}
		fmt.Println("imported", *name)
	case "list":
		apps, err := store.ListApps(context.Background())
		if err != nil {
			fail(err)
		}
		for _, a := range apps {
			fmt.Printf("%s\t%s\t%s\t%t\n", a.Name, a.FeishuAppID, a.WorkspaceDir, a.Enabled)
		}
	case "enable", "disable":
		if *name == "" {
			usage()
		}
		if err := store.SetAppEnabled(context.Background(), *name, cmd == "enable"); err != nil {
			fail(err)
		}
		fmt.Println("updated", *name)
	case "delete":
		if *name == "" {
			usage()
		}
		if err := store.DeleteApp(context.Background(), *name); err != nil {
			fail(err)
		}
		fmt.Println("deleted", *name)
	case "create", "update":
		if *name == "" || *appID == "" || *secret == "" || *workspace == "" {
			usage()
		}
		info, statErr := os.Stat(*workspace)
		if statErr != nil || !info.IsDir() {
			fail(fmt.Errorf("workspace-dir must be an existing directory"))
		}
		if err := store.UpsertApp(context.Background(), storage.App{Name: *name, FeishuAppID: *appID, FeishuAppSecret: *secret, WorkspaceDir: *workspace, WorkspaceMode: "work", Model: *model, ReasoningEffort: *effort, Enabled: *enabled}); err != nil {
			fail(err)
		}
		fmt.Println("updated", *name)
	default:
		usage()
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: appctl create|update|import-legacy-app|list|enable|disable|delete --config config.yaml [--name NAME]")
	os.Exit(2)
}
func fail(err error) { fmt.Fprintln(os.Stderr, "appctl:", err); os.Exit(1) }
