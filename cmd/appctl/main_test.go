package main

import (
	"strings"
	"testing"
)

func TestResolveAppSecretFromEnvironment(t *testing.T) {
	secret, err := resolveAppSecret("", "AIPM_FEISHU_APP_SECRET", func(name string) (string, bool) {
		if name == "AIPM_FEISHU_APP_SECRET" {
			return "secret-from-env", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("resolveAppSecret() error = %v", err)
	}
	if secret != "secret-from-env" {
		t.Fatalf("resolveAppSecret() = %q, want environment value", secret)
	}
}

func TestResolveAppSecretKeepsDirectFlagCompatibility(t *testing.T) {
	secret, err := resolveAppSecret("direct-secret", "", func(string) (string, bool) {
		return "", false
	})
	if err != nil {
		t.Fatalf("resolveAppSecret() error = %v", err)
	}
	if secret != "direct-secret" {
		t.Fatalf("resolveAppSecret() = %q, want direct value", secret)
	}
}

func TestResolveAppSecretRejectsAmbiguousSources(t *testing.T) {
	_, err := resolveAppSecret("direct", "AIPM_FEISHU_APP_SECRET", func(string) (string, bool) {
		return "environment", true
	})
	if err == nil || !strings.Contains(err.Error(), "only one") {
		t.Fatalf("resolveAppSecret() error = %v, want only-one-source error", err)
	}
}

func TestResolveAppSecretRejectsMissingEnvironment(t *testing.T) {
	_, err := resolveAppSecret("", "AIPM_FEISHU_APP_SECRET", func(string) (string, bool) {
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "not set") {
		t.Fatalf("resolveAppSecret() error = %v, want missing-environment error", err)
	}
}

func TestResolveAppSecretRequiresOneSource(t *testing.T) {
	_, err := resolveAppSecret("", "", func(string) (string, bool) {
		return "", false
	})
	if err == nil || !strings.Contains(err.Error(), "provide") {
		t.Fatalf("resolveAppSecret() error = %v, want source-required error", err)
	}
}

func TestResolveAppSecretRejectsInvalidEnvironmentName(t *testing.T) {
	_, err := resolveAppSecret("", "bad-name", func(string) (string, bool) {
		return "secret", true
	})
	if err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("resolveAppSecret() error = %v, want invalid-name error", err)
	}
}
