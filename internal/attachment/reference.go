package attachment

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

// ReferenceProtector encrypts short-lived Feishu resource keys before they
// leave ingress memory. Ciphertexts are nonce-prefixed AES-256-GCM values.
type ReferenceProtector struct {
	keys          map[int][]byte
	activeVersion int
}

func NewReferenceProtector(keyring []config.KeyConfig) (*ReferenceProtector, error) {
	if len(keyring) == 0 {
		return nil, fmt.Errorf("attachment reference keyring is empty")
	}
	protector := &ReferenceProtector{keys: make(map[int][]byte, len(keyring))}
	for _, entry := range keyring {
		if entry.Version < 1 || len(entry.Key) != 32 {
			return nil, fmt.Errorf("invalid attachment reference key version %d", entry.Version)
		}
		if _, exists := protector.keys[entry.Version]; exists {
			return nil, fmt.Errorf("duplicate attachment reference key version %d", entry.Version)
		}
		protector.keys[entry.Version] = append([]byte(nil), entry.Key...)
		if entry.Version > protector.activeVersion {
			protector.activeVersion = entry.Version
		}
	}
	return protector, nil
}

func (p *ReferenceProtector) Seal(appID, attachmentID, sourceMessageID, resourceKey string) ([]byte, int, error) {
	if resourceKey == "" {
		return nil, 0, fmt.Errorf("resource key is required")
	}
	key := p.keys[p.activeVersion]
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, 0, fmt.Errorf("create reference cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, fmt.Errorf("create reference gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, fmt.Errorf("generate reference nonce: %w", err)
	}
	return append(nonce, gcm.Seal(nil, nonce, []byte(resourceKey), referenceAAD(appID, attachmentID, sourceMessageID))...), p.activeVersion, nil
}

func (p *ReferenceProtector) Open(appID, attachmentID, sourceMessageID string, ciphertext []byte, version int) (string, error) {
	key := p.keys[version]
	if len(key) != 32 {
		return "", fmt.Errorf("attachment reference key version %d is unavailable", version)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("create reference cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("create reference gcm: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize() {
		return "", fmt.Errorf("attachment reference ciphertext is malformed")
	}
	plaintext, err := gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], referenceAAD(appID, attachmentID, sourceMessageID))
	if err != nil {
		return "", fmt.Errorf("decrypt attachment reference: %w", err)
	}
	return string(plaintext), nil
}

func referenceAAD(appID, attachmentID, sourceMessageID string) []byte {
	return []byte(appID + "|" + attachmentID + "|" + sourceMessageID)
}
