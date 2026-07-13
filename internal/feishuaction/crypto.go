package feishuaction

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/kid0317/codex-workspace-bot/internal/config"
)

type ResultProtector struct {
	keys   map[int][]byte
	active int
}

func NewResultProtector(entries []config.KeyConfig) (*ResultProtector, error) {
	p := &ResultProtector{keys: make(map[int][]byte)}
	for _, entry := range entries {
		if entry.Version < 1 || len(entry.Key) != 32 {
			return nil, fmt.Errorf("invalid action result key")
		}
		if _, exists := p.keys[entry.Version]; exists {
			return nil, fmt.Errorf("duplicate action result key")
		}
		p.keys[entry.Version] = append([]byte(nil), entry.Key...)
		if entry.Version > p.active {
			p.active = entry.Version
		}
	}
	if p.active == 0 {
		return nil, fmt.Errorf("action result keyring is empty")
	}
	return p, nil
}
func (p *ResultProtector) Seal(app, thread, turn, call, tool string, plaintext []byte) ([]byte, int, error) {
	block, err := aes.NewCipher(p.keys[p.active])
	if err != nil {
		return nil, 0, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, 0, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, 0, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plaintext, resultAAD(app, thread, turn, call, tool))...), p.active, nil
}
func (p *ResultProtector) Open(app, thread, turn, call, tool string, ciphertext []byte, version int) ([]byte, error) {
	block, err := aes.NewCipher(p.keys[version])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("malformed action result")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], resultAAD(app, thread, turn, call, tool))
}
func resultAAD(app, thread, turn, call, tool string) []byte {
	return []byte(app + "|" + thread + "|" + turn + "|" + call + "|" + tool)
}
