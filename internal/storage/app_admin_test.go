package storage_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/storage"
)

func TestSetAppEnabledUpdatesOneApp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE apps SET enabled").WithArgs(false, "health-assistant").WillReturnResult(sqlmock.NewResult(0, 1))
	if err := (&storage.Store{DB: db}).SetAppEnabled(context.Background(), "health-assistant", false); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
