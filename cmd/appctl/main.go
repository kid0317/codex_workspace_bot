package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

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
	secretEnv := fs.String("secret-env", "", "environment variable containing the Feishu app secret")
	secretStdin := fs.Bool("secret-stdin", false, "read the Feishu app secret from stdin")
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
	case "create", "update", "upsert":
		resolvedSecret, secretErr := resolveAppSecret(*secret, *secretEnv, *secretStdin, os.LookupEnv, os.Stdin)
		if secretErr != nil {
			fail(secretErr)
		}
		if *name == "" || *appID == "" || resolvedSecret == "" || *workspace == "" {
			usage()
		}
		info, statErr := os.Stat(*workspace)
		if statErr != nil || !info.IsDir() {
			fail(fmt.Errorf("workspace-dir must be an existing directory"))
		}
		app := storage.App{Name: *name, FeishuAppID: *appID, FeishuAppSecret: resolvedSecret, WorkspaceDir: *workspace, WorkspaceMode: "work", Model: *model, ReasoningEffort: *effort, Enabled: *enabled}
		var writeErr error
		switch cmd {
		case "create":
			writeErr = store.CreateApp(context.Background(), app)
		case "update":
			writeErr = store.UpdateApp(context.Background(), app)
		case "upsert":
			writeErr = store.UpsertApp(context.Background(), app)
		}
		if writeErr != nil {
			fail(writeErr)
		}
		fmt.Println("updated", *name)
	default:
		usage()
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "usage: appctl create|update|upsert|import-legacy-app|list|enable|disable|delete --config config.yaml [--name NAME] [--secret-stdin|--secret-env ENV_NAME]")
	os.Exit(2)
}
func fail(err error) { fmt.Fprintln(os.Stderr, "appctl:", err); os.Exit(1) }

var environmentNamePattern = regexp.MustCompile("^[A-Za-z_][A-Za-z0-9_]*$")

const maxStdinSecretBytes = 256

func resolveAppSecret(direct, environmentName string, stdin bool, lookup func(string) (string, bool), reader io.Reader) (string, error) {
	sourceCount := 0
	if direct != "" {
		sourceCount++
	}
	if environmentName != "" {
		sourceCount++
	}
	if stdin {
		sourceCount++
	}
	if sourceCount > 1 {
		return "", fmt.Errorf("provide only one of --secret, --secret-env or --secret-stdin")
	}
	if stdin {
		value, err := io.ReadAll(io.LimitReader(reader, maxStdinSecretBytes+1))
		if err != nil {
			return "", fmt.Errorf("read --secret-stdin: %w", err)
		}
		if len(value) == 0 {
			return "", fmt.Errorf("--secret-stdin is empty")
		}
		if len(value) > maxStdinSecretBytes {
			return "", fmt.Errorf("--secret-stdin exceeds %d bytes", maxStdinSecretBytes)
		}
		if bytesContainLineBreak(value) {
			return "", fmt.Errorf("--secret-stdin must not contain line breaks")
		}
		return string(value), nil
	}
	if environmentName == "" {
		if direct == "" {
			return "", fmt.Errorf("provide --secret-stdin (recommended), --secret-env or --secret")
		}
		return direct, nil
	}
	if !environmentNamePattern.MatchString(environmentName) {
		return "", fmt.Errorf("invalid --secret-env name")
	}
	secret, ok := lookup(environmentName)
	if !ok || secret == "" {
		return "", fmt.Errorf("environment variable %s is not set or is empty", environmentName)
	}
	return secret, nil
}

func bytesContainLineBreak(value []byte) bool {
	for _, b := range value {
		if b == '\n' || b == '\r' {
			return true
		}
	}
	return false
}
