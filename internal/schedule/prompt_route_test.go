package schedule

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadPromptRouteUsesPlaintextOwnerForClaimedPromptRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT a.id,a.workspace_dir").WithArgs("run-1", "task-1", uint64(2), "token-1").WillReturnRows(sqlmock.NewRows([]string{"id", "workspace_dir", "workspace_mode", "model", "reasoning_effort", "chat_group_id", "chat_type", "chat_id", "creator_open_id"}).AddRow("app-1", "/workspace", "work", "model", "medium", "group-1", "p2p", "ou-1", "ou-1"))
	route, err := (&Repository{DB: db}).LoadPromptRoute(context.Background(), ClaimedRun{ID: "run-1", TaskID: "task-1", TaskVersion: 2, Kind: TaskPrompt, ClaimToken: "token-1"})
	if err != nil || route.OwnerOpenID != "ou-1" || route.ChatType != "p2p" {
		t.Fatalf("route=%#v err=%v", route, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
