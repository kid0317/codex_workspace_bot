package attachment

import (
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

func TestReferenceProtectorRoundTripsWithBoundAAD(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	protector, err := NewReferenceProtector([]config.KeyConfig{{Version: 7, Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, version, err := protector.Seal("app-1", "attachment-1", "om-1", "resource-key")
	if err != nil || version != 7 {
		t.Fatalf("Seal() version=%d err=%v", version, err)
	}
	if string(ciphertext) == "resource-key" {
		t.Fatal("resource key was not encrypted")
	}
	got, err := protector.Open("app-1", "attachment-1", "om-1", ciphertext, version)
	if err != nil || got != "resource-key" {
		t.Fatalf("Open() value=%q err=%v", got, err)
	}
	if _, err := protector.Open("app-1", "attachment-1", "other-message", ciphertext, version); err == nil {
		t.Fatal("Open() accepted mismatched AAD")
	}
}
