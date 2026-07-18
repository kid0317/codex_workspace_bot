package schedule

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// Key is a versioned, 32-byte secret loaded from local configuration.
type Key struct {
	Version  int
	Material []byte
}

// Keyring retains old keys for decrypting existing records while selecting the
// first configured version for new encryption and signatures. Configuration
// order is the explicit key-rotation control plane for S06.
type Keyring struct {
	keys   map[int][]byte
	active int
}

func NewKeyring(entries []Key) (Keyring, error) {
	keyring := Keyring{keys: make(map[int][]byte, len(entries))}
	for _, entry := range entries {
		if entry.Version < 1 || len(entry.Material) != 32 {
			return Keyring{}, fmt.Errorf("schedule key is invalid")
		}
		if _, exists := keyring.keys[entry.Version]; exists {
			return Keyring{}, fmt.Errorf("schedule key version %d is duplicated", entry.Version)
		}
		keyring.keys[entry.Version] = append([]byte(nil), entry.Material...)
		if keyring.active == 0 {
			keyring.active = entry.Version
		}
	}
	if keyring.active == 0 {
		return Keyring{}, fmt.Errorf("schedule keyring is empty")
	}
	return keyring, nil
}

func (k Keyring) activeKey() ([]byte, int, error) {
	key := k.keys[k.active]
	if len(key) != 32 {
		return nil, 0, fmt.Errorf("schedule active key is unavailable")
	}
	return key, k.active, nil
}

func (k Keyring) key(version int) ([]byte, error) {
	key := k.keys[version]
	if len(key) != 32 {
		return nil, fmt.Errorf("schedule key version %d is unavailable", version)
	}
	return key, nil
}

func (k Keyring) HMAC(plaintext []byte) (string, int, error) {
	key, version, err := k.activeKey()
	if err != nil {
		return "", 0, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(plaintext)
	return hex.EncodeToString(mac.Sum(nil)), version, nil
}

func (k Keyring) VerifyHMAC(version int, plaintext []byte, expected string) error {
	key, err := k.key(version)
	if err != nil {
		return err
	}
	provided, err := hex.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("schedule HMAC is malformed")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(plaintext)
	if subtle.ConstantTimeCompare(provided, mac.Sum(nil)) != 1 {
		return fmt.Errorf("schedule HMAC verification failed")
	}
	return nil
}

// PayloadBinding is the stable AAD for a protected schedule value.
type PayloadBinding struct {
	AppID       string
	ChatGroupID string
	OwnerHMAC   string
	TaskID      string
	Version     uint64
	Kind        string
	Field       string
}

func (b PayloadBinding) validate() error {
	if strings.TrimSpace(b.AppID) == "" || strings.TrimSpace(b.ChatGroupID) == "" || strings.TrimSpace(b.OwnerHMAC) == "" || strings.TrimSpace(b.TaskID) == "" || b.Version == 0 || strings.TrimSpace(b.Kind) == "" || strings.TrimSpace(b.Field) == "" {
		return fmt.Errorf("schedule payload binding is incomplete")
	}
	return nil
}

func (b PayloadBinding) aad() []byte {
	// JSON avoids ambiguous delimiter concatenation when upstream IDs contain
	// punctuation; it is never persisted or logged as a sensitive payload.
	encoded, _ := json.Marshal(struct {
		AppID, ChatGroupID, OwnerHMAC, TaskID, Kind, Field string
		Version                                            uint64
	}{b.AppID, b.ChatGroupID, b.OwnerHMAC, b.TaskID, b.Kind, b.Field, b.Version})
	return encoded
}

type Sealed struct {
	Ciphertext []byte
	KeyVersion int
	HMAC       string
	Bytes      int
}

// Protector intentionally separates payload encryption from owner indexing.
// Neither original open ID nor plaintext is returned by repository metadata.
type Protector struct {
	Payloads Keyring
	Owners   Keyring
}

func (p Protector) OwnerHMAC(owner Owner) (string, error) {
	if err := owner.Validate(); err != nil {
		return "", err
	}
	key, _, err := p.Owners.activeKey()
	if err != nil {
		return "", err
	}
	encoded, _ := json.Marshal(owner)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(encoded)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (p Protector) Seal(binding PayloadBinding, plaintext []byte) (Sealed, error) {
	if err := binding.validate(); err != nil {
		return Sealed{}, err
	}
	key, version, err := p.Payloads.activeKey()
	if err != nil {
		return Sealed{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return Sealed{}, fmt.Errorf("create schedule cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return Sealed{}, fmt.Errorf("create schedule gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Sealed{}, fmt.Errorf("generate schedule nonce: %w", err)
	}
	ciphertext := append(nonce, gcm.Seal(nil, nonce, plaintext, binding.aad())...)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(plaintext)
	return Sealed{Ciphertext: ciphertext, KeyVersion: version, HMAC: hex.EncodeToString(mac.Sum(nil)), Bytes: len(plaintext)}, nil
}

func (p Protector) Open(binding PayloadBinding, sealed Sealed) ([]byte, error) {
	if err := binding.validate(); err != nil {
		return nil, err
	}
	key, err := p.Payloads.key(sealed.KeyVersion)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create schedule cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create schedule gcm: %w", err)
	}
	if len(sealed.Ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("schedule ciphertext is malformed")
	}
	plaintext, err := gcm.Open(nil, sealed.Ciphertext[:gcm.NonceSize()], sealed.Ciphertext[gcm.NonceSize():], binding.aad())
	if err != nil {
		return nil, fmt.Errorf("decrypt schedule payload: %w", err)
	}
	if sealed.HMAC != "" {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(plaintext)
		expected, decodeErr := hex.DecodeString(sealed.HMAC)
		if decodeErr != nil || subtle.ConstantTimeCompare(expected, mac.Sum(nil)) != 1 {
			return nil, fmt.Errorf("schedule payload integrity check failed")
		}
	}
	return plaintext, nil
}

type CursorPosition struct {
	UpdatedAt time.Time
	TaskID    string
}

type CursorCodec struct {
	Keys Keyring
	Now  func() time.Time
}

type signedCursor struct {
	OwnerHMAC string `json:"o"`
	UpdatedMS int64  `json:"u"`
	TaskID    string `json:"i"`
	IssuedMS  int64  `json:"t"`
	Version   int    `json:"v"`
}

func (c CursorCodec) Encode(ownerHMAC string, position CursorPosition) (string, error) {
	if strings.TrimSpace(ownerHMAC) == "" || strings.TrimSpace(position.TaskID) == "" || position.UpdatedAt.IsZero() {
		return "", fmt.Errorf("schedule cursor position is invalid")
	}
	key, version, err := c.Keys.activeKey()
	if err != nil {
		return "", err
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	payload, err := json.Marshal(signedCursor{OwnerHMAC: ownerHMAC, UpdatedMS: position.UpdatedAt.UTC().UnixMilli(), TaskID: position.TaskID, IssuedMS: now.UTC().UnixMilli(), Version: version})
	if err != nil {
		return "", fmt.Errorf("encode schedule cursor: %w", err)
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (c CursorCodec) Decode(ownerHMAC, token string) (CursorPosition, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 || strings.TrimSpace(ownerHMAC) == "" {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	var cursor signedCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	key, err := c.Keys.key(cursor.Version)
	if err != nil {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	provided, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(payload)
	if subtle.ConstantTimeCompare(provided, mac.Sum(nil)) != 1 || subtle.ConstantTimeCompare([]byte(cursor.OwnerHMAC), []byte(ownerHMAC)) != 1 || cursor.TaskID == "" {
		return CursorPosition{}, fmt.Errorf("schedule cursor is invalid")
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	if time.UnixMilli(cursor.IssuedMS).Add(15 * time.Minute).Before(now) {
		return CursorPosition{}, fmt.Errorf("schedule cursor is expired")
	}
	return CursorPosition{UpdatedAt: time.UnixMilli(cursor.UpdatedMS).UTC(), TaskID: cursor.TaskID}, nil
}
