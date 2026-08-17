package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseDotenvTreatsValuesAsData(t *testing.T) {
	values, err := parseDotenv(strings.NewReader(strings.Join([]string{
		`CODEX_WORKSPACE_BOT_DB_PASSWORD=db-secret`,
		`CODEX_HOME=/Users/test/My\ Workspace/中文`,
		`OPENAI_API_KEY=sk-abc\$literal`,
		`USER_DIR=/Users/test/用户目录`,
		`AIPM_STATE=use`,
	}, "\n")))
	if err != nil {
		t.Fatalf("parseDotenv() error = %v", err)
	}
	want := map[string]string{
		"CODEX_WORKSPACE_BOT_DB_PASSWORD": "db-secret",
		"CODEX_HOME":                      "/Users/test/My Workspace/中文",
		"OPENAI_API_KEY":                  "sk-abc$literal",
		"USER_DIR":                        "/Users/test/用户目录",
		"AIPM_STATE":                      "use",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("parseDotenv() = %#v, want %#v", values, want)
	}
}

func TestParseDotenvRejectsExecutableOrDangerousContent(t *testing.T) {
	tests := []string{
		"set -x\nCODEX_WORKSPACE_BOT_DB_PASSWORD=sentinel",
		"printf() { :; }\nCODEX_WORKSPACE_BOT_DB_PASSWORD=sentinel",
		"trap 'printf leaked' DEBUG\nCODEX_WORKSPACE_BOT_DB_PASSWORD=sentinel",
		"CODEX_WORKSPACE_BOT_DB_PASSWORD=$(printf sentinel)",
		"CODEX_WORKSPACE_BOT_DB_PASSWORD=`printf sentinel`",
		"PATH=/attacker/bin",
		"HOME=/attacker/home",
		"BASH_ENV=/attacker/env",
		"PROMPT_COMMAND=printf\\ sentinel",
		"CODEX_WORKSPACE_BOT_DB_PASSWORD=one\nCODEX_WORKSPACE_BOT_DB_PASSWORD=two",
	}
	for _, input := range tests {
		if _, err := parseDotenv(strings.NewReader(input)); err == nil {
			t.Fatalf("parseDotenv(%q) error = nil, want rejection", input)
		}
	}
}

func TestChildEnvironmentIsMinimalAndDotenvOverridesAllowedKeysOnly(t *testing.T) {
	got := childEnvironment(
		[]string{"PATH=/usr/bin:/bin", "HOME=/Users/real", "LANG=zh_CN.UTF-8", "PARENT_SECRET=drop-me", "BASH_ENV=/tmp/evil"},
		map[string]string{"CODEX_WORKSPACE_BOT_DB_PASSWORD": "db-secret", "OPENAI_API_KEY": "provider-secret"},
	)
	want := []string{
		"CODEX_WORKSPACE_BOT_DB_PASSWORD=db-secret",
		"HOME=/Users/real",
		"LANG=zh_CN.UTF-8",
		"OPENAI_API_KEY=provider-secret",
		"PATH=/usr/bin:/bin",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("childEnvironment() = %#v, want %#v", got, want)
	}
}
