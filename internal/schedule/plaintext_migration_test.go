package schedule

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMigrateLegacyProtectedTaskDataWritesPlaintextAndClearsLegacyColumns(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	protector := testProtector(t)
	owner := Owner{AppID: "app-1", ChatGroupID: "group-1", OpenID: "user-1"}
	ownerHMAC, err := protector.OwnerHMAC(owner)
	if err != nil {
		t.Fatal(err)
	}
	ownerSealed, err := protector.Seal(PayloadBinding{AppID: owner.AppID, ChatGroupID: owner.ChatGroupID, OwnerHMAC: ownerHMAC, TaskID: "task-1", Version: 1, Kind: "prompt", Field: "creator_open_id"}, []byte(owner.OpenID))
	if err != nil {
		t.Fatal(err)
	}
	payloadSealed, err := protector.Seal(PayloadBinding{AppID: owner.AppID, ChatGroupID: owner.ChatGroupID, OwnerHMAC: ownerHMAC, TaskID: "task-1", Version: 2, Kind: "prompt", Field: "payload"}, []byte("independent prompt"))
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id,app_id,chat_group_id,creator_open_id_hmac,kind,version,creator_open_id_enc,creator_key_version,payload_enc,payload_key_version,payload_hmac,payload_bytes FROM scheduled_tasks").
		WillReturnRows(sqlmock.NewRows([]string{"id", "app_id", "chat_group_id", "creator_open_id_hmac", "kind", "version", "creator_open_id_enc", "creator_key_version", "payload_enc", "payload_key_version", "payload_hmac", "payload_bytes"}).
			AddRow("task-1", owner.AppID, owner.ChatGroupID, ownerHMAC, "prompt", uint64(2), ownerSealed.Ciphertext, ownerSealed.KeyVersion, payloadSealed.Ciphertext, payloadSealed.KeyVersion, payloadSealed.HMAC, payloadSealed.Bytes))
	mock.ExpectExec(`UPDATE scheduled_tasks SET creator_open_id=\?,creator_open_id_enc=NULL,creator_key_version=NULL,payload_text=\?,payload_enc=NULL,payload_key_version=NULL,payload_hmac=NULL,payload_bytes=NULL WHERE id=\?`).
		WithArgs("user-1", "independent prompt", "task-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT sr.id,sr.task_id,t.app_id,t.chat_group_id,t.creator_open_id_hmac,sr.kind,sr.task_version,sr.payload_enc,sr.payload_key_version,sr.payload_hmac,sr.payload_bytes FROM scheduled_task_runs").
		WillReturnRows(sqlmock.NewRows([]string{"id", "task_id", "app_id", "chat_group_id", "creator_open_id_hmac", "kind", "task_version", "payload_enc", "payload_key_version", "payload_hmac", "payload_bytes"}))
	mock.ExpectQuery("SELECT app_id,channel_key,chat_group_id,thread_id,turn_id,call_id,tool,result_enc,result_key_version FROM scheduled_task_tool_calls").
		WillReturnRows(sqlmock.NewRows([]string{"app_id", "channel_key", "chat_group_id", "thread_id", "turn_id", "call_id", "tool", "result_enc", "result_key_version"}))
	mock.ExpectCommit()

	migrated, err := (&Repository{DB: db, Protector: protector}).MigrateLegacyProtectedTaskData(context.Background())
	if err != nil || migrated != 1 {
		t.Fatalf("migrated=%d err=%v", migrated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
