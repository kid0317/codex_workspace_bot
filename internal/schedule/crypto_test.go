package schedule

import (
	"bytes"
	"testing"
	"time"
)

func TestProtectorBindsCiphertextToTaskOwnerAndField(t *testing.T) {
	payloads, err := NewKeyring([]Key{{Version: 2, Material: bytes.Repeat([]byte{1}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	owners, err := NewKeyring([]Key{{Version: 3, Material: bytes.Repeat([]byte{2}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	protector := Protector{Payloads: payloads, Owners: owners}
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	binding := PayloadBinding{AppID: owner.AppID, ChatGroupID: owner.ChatGroupID, OwnerHMAC: ownerHMAC, TaskID: "task-1", Version: 1, Kind: "prompt", Field: "payload"}
	sealed, err := protector.Seal(binding, []byte("private prompt"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := protector.Open(binding, sealed)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if string(got) != "private prompt" {
		t.Fatalf("Open() = %q", got)
	}
	binding.Field = "tool_result"
	if _, err := protector.Open(binding, sealed); err == nil {
		t.Fatal("Open() accepted ciphertext with changed AAD field")
	}
}

func TestOwnerHMACIsScopeSpecific(t *testing.T) {
	keys, err := NewKeyring([]Key{{Version: 1, Material: bytes.Repeat([]byte{3}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	protector := Protector{Owners: keys}
	first, err := protector.OwnerHMAC(Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := protector.OwnerHMAC(Owner{AppID: "app-1", ChatGroupID: "group-2", OpenID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("OwnerHMAC() did not bind chat group")
	}
}

func TestCursorRejectsAnotherOwnerAndTampering(t *testing.T) {
	keys, err := NewKeyring([]Key{{Version: 1, Material: bytes.Repeat([]byte{4}, 32)}})
	if err != nil {
		t.Fatal(err)
	}
	codec := CursorCodec{Keys: keys, Now: func() time.Time { return time.Date(2026, time.July, 13, 0, 0, 0, 0, time.UTC) }}
	cursor, err := codec.Encode("owner-a", CursorPosition{UpdatedAt: time.Date(2026, time.July, 12, 0, 0, 0, 0, time.UTC), TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := codec.Decode("owner-b", cursor); err == nil {
		t.Fatal("Decode() accepted cursor for another owner")
	}
	if _, err := codec.Decode("owner-a", cursor+"x"); err == nil {
		t.Fatal("Decode() accepted tampered cursor")
	}
}

func TestKeyringUsesFirstConfiguredKeyForNewValues(t *testing.T) {
	keyring, err := NewKeyring([]Key{
		{Version: 1, Material: bytes.Repeat([]byte{5}, 32)},
		{Version: 2, Material: bytes.Repeat([]byte{6}, 32)},
	})
	if err != nil {
		t.Fatal(err)
	}
	key, version, err := keyring.activeKey()
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 || !bytes.Equal(key, bytes.Repeat([]byte{5}, 32)) {
		t.Fatalf("active key = version %d %x, want first configured key", version, key)
	}
}
