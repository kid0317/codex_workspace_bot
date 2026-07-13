package feishuaction

import (
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

func TestResultProtectorBindsActionIdentity(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	protector, err := NewResultProtector([]config.KeyConfig{{Version: 1, Key: key}})
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, version, err := protector.Seal("app-1", "thread-1", "turn-1", "call-1", "tool", []byte(`{"outcome":"sent"}`))
	if err != nil || version != 1 {
		t.Fatalf("seal version=%d err=%v", version, err)
	}
	plaintext, err := protector.Open("app-1", "thread-1", "turn-1", "call-1", "tool", ciphertext, version)
	if err != nil || string(plaintext) != `{"outcome":"sent"}` {
		t.Fatalf("open=%q err=%v", plaintext, err)
	}
	if _, err := protector.Open("app-1", "thread-1", "turn-1", "other", "tool", ciphertext, version); err == nil {
		t.Fatal("wrong call id was accepted")
	}
}
