package schedule

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

// MigrateLegacyProtectedTaskData performs the one-way application migration
// after migration 006 has added plaintext columns. It deliberately does not
// log values. A failure aborts startup, avoiding a mixed persistence mode.
func (r Repository) MigrateLegacyProtectedTaskData(ctx context.Context) (int, error) {
	if r.DB == nil {
		return 0, fmt.Errorf("schedule store database is required")
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin legacy schedule plaintext migration: %w", err)
	}
	defer tx.Rollback()

	type taskRow struct {
		id, appID, groupID, ownerHMAC, kind string
		version                             uint64
		owner                               Sealed
		payload                             Sealed
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,app_id,chat_group_id,creator_open_id_hmac,kind,version,creator_open_id_enc,creator_key_version,payload_enc,payload_key_version,payload_hmac,payload_bytes FROM scheduled_tasks WHERE creator_open_id IS NULL OR payload_text IS NULL FOR UPDATE`)
	if err != nil {
		return 0, fmt.Errorf("read legacy scheduled tasks: %w", err)
	}
	var tasks []taskRow
	for rows.Next() {
		var row taskRow
		if err := rows.Scan(&row.id, &row.appID, &row.groupID, &row.ownerHMAC, &row.kind, &row.version, &row.owner.Ciphertext, &row.owner.KeyVersion, &row.payload.Ciphertext, &row.payload.KeyVersion, &row.payload.HMAC, &row.payload.Bytes); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan legacy scheduled task: %w", err)
		}
		tasks = append(tasks, row)
	}
	if err := rows.Close(); err != nil {
		return 0, fmt.Errorf("close legacy scheduled tasks: %w", err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate legacy scheduled tasks: %w", err)
	}

	for _, row := range tasks {
		owner, err := r.Protector.Open(PayloadBinding{AppID: row.appID, ChatGroupID: row.groupID, OwnerHMAC: row.ownerHMAC, TaskID: row.id, Version: 1, Kind: row.kind, Field: "creator_open_id"}, row.owner)
		if err != nil || len(owner) == 0 {
			return 0, fmt.Errorf("open legacy schedule owner for task %s", row.id)
		}
		payload, err := r.Protector.Open(PayloadBinding{AppID: row.appID, ChatGroupID: row.groupID, OwnerHMAC: row.ownerHMAC, TaskID: row.id, Version: row.version, Kind: row.kind, Field: "payload"}, row.payload)
		if err != nil || len(payload) == 0 {
			return 0, fmt.Errorf("open legacy schedule payload for task %s", row.id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET creator_open_id=?,creator_open_id_enc=NULL,creator_key_version=NULL,payload_text=?,payload_enc=NULL,payload_key_version=NULL,payload_hmac=NULL,payload_bytes=NULL WHERE id=?`, string(owner), string(payload), row.id); err != nil {
			return 0, fmt.Errorf("write plaintext scheduled task: %w", err)
		}
	}

	type runRow struct {
		id, taskID, appID, groupID, ownerHMAC, kind string
		version                                     uint64
		payload                                     Sealed
	}
	runRows, err := tx.QueryContext(ctx, `SELECT sr.id,sr.task_id,t.app_id,t.chat_group_id,t.creator_open_id_hmac,sr.kind,sr.task_version,sr.payload_enc,sr.payload_key_version,sr.payload_hmac,sr.payload_bytes FROM scheduled_task_runs sr JOIN scheduled_tasks t ON t.id=sr.task_id WHERE sr.payload_text IS NULL FOR UPDATE`)
	if err != nil {
		return 0, fmt.Errorf("read legacy scheduled runs: %w", err)
	}
	var runs []runRow
	for runRows.Next() {
		var row runRow
		if err := runRows.Scan(&row.id, &row.taskID, &row.appID, &row.groupID, &row.ownerHMAC, &row.kind, &row.version, &row.payload.Ciphertext, &row.payload.KeyVersion, &row.payload.HMAC, &row.payload.Bytes); err != nil {
			_ = runRows.Close()
			return 0, fmt.Errorf("scan legacy scheduled run: %w", err)
		}
		runs = append(runs, row)
	}
	if err := runRows.Close(); err != nil {
		return 0, fmt.Errorf("close legacy scheduled runs: %w", err)
	}
	if err := runRows.Err(); err != nil {
		return 0, fmt.Errorf("iterate legacy scheduled runs: %w", err)
	}
	for _, row := range runs {
		payload, err := r.Protector.Open(PayloadBinding{AppID: row.appID, ChatGroupID: row.groupID, OwnerHMAC: row.ownerHMAC, TaskID: row.taskID, Version: row.version, Kind: row.kind, Field: "payload"}, row.payload)
		if err != nil || len(payload) == 0 {
			return 0, fmt.Errorf("open legacy schedule payload for run %s", row.id)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_task_runs SET payload_text=?,payload_enc=NULL,payload_key_version=NULL,payload_hmac=NULL,payload_bytes=NULL WHERE id=?`, string(payload), row.id); err != nil {
			return 0, fmt.Errorf("write plaintext scheduled run: %w", err)
		}
	}

	type toolRow struct {
		appID, channelKey, groupID, threadID, turnID, callID, tool string
		result                                                     Sealed
	}
	toolRows, err := tx.QueryContext(ctx, `SELECT app_id,channel_key,chat_group_id,thread_id,turn_id,call_id,tool,result_enc,result_key_version FROM scheduled_task_tool_calls WHERE state='succeeded' AND result_text IS NULL AND result_enc IS NOT NULL FOR UPDATE`)
	if err != nil {
		return 0, fmt.Errorf("read legacy schedule tool results: %w", err)
	}
	var tools []toolRow
	for toolRows.Next() {
		var row toolRow
		if err := toolRows.Scan(&row.appID, &row.channelKey, &row.groupID, &row.threadID, &row.turnID, &row.callID, &row.tool, &row.result.Ciphertext, &row.result.KeyVersion); err != nil {
			_ = toolRows.Close()
			return 0, fmt.Errorf("scan legacy schedule tool result: %w", err)
		}
		tools = append(tools, row)
	}
	if err := toolRows.Close(); err != nil {
		return 0, fmt.Errorf("close legacy schedule tool results: %w", err)
	}
	for _, row := range tools {
		identity := ToolCallIdentity{AppID: row.appID, ChannelKey: row.channelKey, ChatGroupID: row.groupID, ThreadID: row.threadID, TurnID: row.turnID, CallID: row.callID, Tool: row.tool}
		payload, err := r.openLegacyToolResult(identity, row.result)
		if err != nil {
			return 0, fmt.Errorf("open legacy schedule tool result for call %s", row.callID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_task_tool_calls SET result_text=?,result_enc=NULL,result_key_version=NULL WHERE app_id=? AND thread_id=? AND turn_id=? AND call_id=?`, string(payload), row.appID, row.threadID, row.turnID, row.callID); err != nil {
			return 0, fmt.Errorf("write plaintext schedule tool result: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit legacy schedule plaintext migration: %w", err)
	}
	return len(tasks) + len(runs) + len(tools), nil
}

func (r Repository) openLegacyToolResult(identity ToolCallIdentity, sealed Sealed) ([]byte, error) {
	key, err := r.Protector.Payloads.key(sealed.KeyVersion)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(sealed.Ciphertext) < gcm.NonceSize() {
		return nil, fmt.Errorf("legacy schedule tool result is malformed")
	}
	return gcm.Open(nil, sealed.Ciphertext[:gcm.NonceSize()], sealed.Ciphertext[gcm.NonceSize():], []byte(identity.AppID+"\x00"+identity.ChannelKey+"\x00"+identity.ChatGroupID+"\x00"+identity.ThreadID+"\x00"+identity.TurnID+"\x00"+identity.CallID+"\x00"+identity.Tool))
}
