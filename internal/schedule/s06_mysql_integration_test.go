package schedule

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// TestS06MySQLClaimRaceAndRestartEvidence is opt-in because it needs the
// temporary schema provisioned by scripts/story06_smoke.sh. It proves the
// unique task-slot claim with two real Repository transactions, then verifies
// a restart scan cannot redispatch the claimed slot.
func TestS06MySQLClaimRaceAndRestartEvidence(t *testing.T) {
	dsn := os.Getenv("S06_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set S06_MYSQL_DSN to run against the isolated Story 06 schema")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	const appID = "00000000-0000-0000-0000-000000000601"
	const groupID = "00000000-0000-0000-0000-000000000602"
	const taskID = "00000000-0000-0000-0000-000000000603"
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, "DELETE FROM scheduled_task_runs WHERE task_id=?", taskID)
		_, _ = db.ExecContext(ctx, "DELETE FROM scheduled_tasks WHERE id=?", taskID)
		_, _ = db.ExecContext(ctx, "DELETE FROM chat_groups WHERE id=?", groupID)
		_, _ = db.ExecContext(ctx, "DELETE FROM apps WHERE id=?", appID)
	})
	_, err = db.ExecContext(ctx, `INSERT INTO apps (id,name,feishu_app_id,feishu_app_secret,workspace_dir,workspace_mode,model,reasoning_effort,enabled) VALUES (?,?,?,?,?,?,?,?,TRUE)`, appID, "s06-race-app", "cli-s06-race", "unused", t.TempDir(), "work", "gpt-5", "medium")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO chat_groups (id,app_id,chat_type,chat_id) VALUES (?,?,?,?)`, groupID, appID, "p2p", "ou-s06-race")
	if err != nil {
		t.Fatal(err)
	}
	slot := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	_, err = db.ExecContext(ctx, `INSERT INTO scheduled_tasks (id,app_id,chat_group_id,creator_open_id_hmac,creator_open_id_enc,creator_key_version,kind,cron_expression,timezone,payload_enc,payload_key_version,payload_hmac,payload_bytes,silent,enabled,version,next_run_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, taskID, appID, groupID, "0000000000000000000000000000000000000000000000000000000000000000", []byte("owner"), 1, "prompt", "*/5 * * * *", "UTC", []byte("payload"), 1, "1111111111111111111111111111111111111111111111111111111111111111", 7, false, true, 1, slot)
	if err != nil {
		t.Fatal(err)
	}
	now := slot.Add(time.Second)
	repo := Repository{DB: db, Now: func() time.Time { return now }}
	claim := DueClaim{TaskID: taskID, ObservedVersion: 1, ObservedSlot: slot, Lease: time.Minute}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, err := repo.ClaimDue(ctx, claim)
			results <- err
		}()
	}
	group.Wait()
	close(results)
	var claimed, conflicts int
	for err := range results {
		switch {
		case err == nil:
			claimed++
		case errors.Is(err, ErrDueClaimConflict):
			conflicts++
		default:
			t.Fatalf("ClaimDue() error=%v", err)
		}
	}
	if claimed != 1 || conflicts != 1 {
		t.Fatalf("claims=%d conflicts=%d", claimed, conflicts)
	}
	var runs int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduled_task_runs WHERE task_id=? AND scheduled_for=?", taskID, slot).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("claimed run count=%d want 1", runs)
	}
	due, err := repo.ListDue(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range due {
		if task.ID == taskID && task.NextRunAt.Equal(slot) {
			t.Fatal("restart scan exposed an already claimed slot")
		}
	}
}
