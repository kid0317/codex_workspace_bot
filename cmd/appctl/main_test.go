package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestResolveAppSecretFromEnvironment(t *testing.T) {
	secret, err := resolveAppSecret("", "AIPM_FEISHU_APP_SECRET", false, func(name string) (string, bool) {
		if name == "AIPM_FEISHU_APP_SECRET" {
			return "secret-from-env", true
		}
		return "", false
	}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("resolveAppSecret() error = %v", err)
	}
	if secret != "secret-from-env" {
		t.Fatalf("resolveAppSecret() = %q, want environment value", secret)
	}
}

func TestResolveAppSecretKeepsDirectFlagCompatibility(t *testing.T) {
	secret, err := resolveAppSecret("direct-secret", "", false, func(string) (string, bool) {
		return "", false
	}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("resolveAppSecret() error = %v", err)
	}
	if secret != "direct-secret" {
		t.Fatalf("resolveAppSecret() = %q, want direct value", secret)
	}
}

func TestResolveAppSecretRejectsAmbiguousSources(t *testing.T) {
	_, err := resolveAppSecret("direct", "AIPM_FEISHU_APP_SECRET", false, func(string) (string, bool) {
		return "environment", true
	}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("resolveAppSecret() error = %v, want only-one-source error", err)
	}
}

func TestResolveAppSecretRejectsMissingEnvironment(t *testing.T) {
	_, err := resolveAppSecret("", "AIPM_FEISHU_APP_SECRET", false, func(string) (string, bool) {
		return "", false
	}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("resolveAppSecret() error = %v, want missing-environment error", err)
	}
}

func TestResolveAppSecretRequiresOneSource(t *testing.T) {
	_, err := resolveAppSecret("", "", false, func(string) (string, bool) {
		return "", false
	}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "provide") {
		t.Fatalf("resolveAppSecret() error = %v, want source-required error", err)
	}
}

func TestResolveAppSecretRejectsInvalidEnvironmentName(t *testing.T) {
	_, err := resolveAppSecret("", "bad-name", false, func(string) (string, bool) {
		return "secret", true
	}, strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("resolveAppSecret() error = %v, want invalid-name error", err)
	}
}

func TestResolveAppSecretFromStdin(t *testing.T) {
	secret, err := resolveAppSecret("", "", true, func(string) (string, bool) { return "", false }, strings.NewReader("stdin-secret"))
	if err != nil {
		t.Fatalf("resolveAppSecret(stdin) error = %v", err)
	}
	if secret != "stdin-secret" {
		t.Fatalf("resolveAppSecret(stdin) = %q", secret)
	}
}

func TestResolveAppSecretStdinRejectsAmbiguousSources(t *testing.T) {
	for _, tc := range []struct {
		name, direct, environment string
	}{
		{name: "direct", direct: "direct-secret"},
		{name: "environment", environment: "APP_SECRET"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveAppSecret(tc.direct, tc.environment, true, func(string) (string, bool) { return "env-secret", true }, strings.NewReader("stdin-secret"))
			if err == nil || !strings.Contains(err.Error(), "only one") {
				t.Fatalf("resolveAppSecret() error = %v, want only-one-source error", err)
			}
		})
	}
}

func TestResolveAppSecretStdinRejectsEmptyNewlineAndOversize(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []byte
	}{
		{name: "empty", input: nil},
		{name: "line-feed", input: []byte("secret\n")},
		{name: "carriage-return", input: []byte("secret\r")},
		{name: "oversize", input: bytes.Repeat([]byte{'x'}, 257)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveAppSecret("", "", true, func(string) (string, bool) { return "", false }, bytes.NewReader(tc.input))
			if err == nil {
				t.Fatal("resolveAppSecret() error = nil, want validation failure")
			}
		})
	}
}
