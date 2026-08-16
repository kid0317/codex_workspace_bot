package codexapp

import (
	"reflect"
	"testing"
)

func TestChildEnvironmentAllowlistDropsBotSecrets(t *testing.T) {
	ambient := []string{
		"PATH=/usr/bin",
		"HOME=/space/system/home",
		"LANG=C.UTF-8",
		"CODEX_BRIDGE_ADDR=codex:7070",
		"FEISHU_APP_SECRET=should-not-leak",
		"CODEX_WORKSPACE_BOT_DB_PASSWORD=should-not-leak",
	}

	got := childEnvironment(ambient, "PATH,HOME,LANG,CODEX_BRIDGE_ADDR")
	want := []string{
		"PATH=/usr/bin",
		"HOME=/space/system/home",
		"LANG=C.UTF-8",
		"CODEX_BRIDGE_ADDR=codex:7070",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("child environment = %#v, want %#v", got, want)
	}
}

func TestChildEnvironmentUnsetAllowlistPreservesNativeMode(t *testing.T) {
	ambient := []string{"PATH=/usr/bin", "CUSTOM=value"}
	if got := childEnvironment(ambient, ""); !reflect.DeepEqual(got, ambient) {
		t.Fatalf("child environment = %#v, want native environment %#v", got, ambient)
	}
}

