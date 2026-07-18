package schedule

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRepositoryListDueFiltersEnabledRoutesAndOrdersSlots(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT t.id,t.version,t.next_run_at,t.kind,a.enabled,cg.schedule_enabled FROM scheduled_tasks t JOIN apps a").WithArgs(now, 100).
		WillReturnRows(sqlmock.NewRows([]string{"id", "version", "next_run_at", "kind", "app_enabled", "schedule_enabled"}).AddRow("task-1", uint64(2), now, "prompt", true, true))
	due, err := (&Repository{DB: db}).ListDue(context.Background(), now, 100)
	if err != nil || len(due) != 1 || due[0].ID != "task-1" || due[0].Kind != TaskPrompt {
		t.Fatalf("due=%#v err=%v", due, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSchedulerClaimsDueOnceAndBoundsPromptDispatch(t *testing.T) {
	now := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	store := &fakeDueStore{due: []DueTask{{ID: "prompt-1", Version: 1, NextRunAt: now, Kind: TaskPrompt}, {ID: "prompt-2", Version: 1, NextRunAt: now, Kind: TaskPrompt}, {ID: "script-1", Version: 1, NextRunAt: now, Kind: TaskScript}}}
	dispatcher := &fakeDispatcher{}
	scheduler := Scheduler{Store: store, Dispatch: dispatcher, Now: func() time.Time { return now }, MaxPromptDispatch: 1, MisfireGrace: time.Minute}
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.claims) != 2 || store.claims[0].TaskID != "prompt-1" || store.claims[1].TaskID != "script-1" || len(dispatcher.runs) != 2 {
		t.Fatalf("claims=%#v runs=%#v", store.claims, dispatcher.runs)
	}
}

func TestSchedulerSkipsStaleSlotWithoutDispatch(t *testing.T) {
	now := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	store := &fakeDueStore{due: []DueTask{{ID: "stale", Version: 1, NextRunAt: now.Add(-2 * time.Minute), Kind: TaskPrompt}}}
	dispatcher := &fakeDispatcher{}
	scheduler := Scheduler{Store: store, Dispatch: dispatcher, Now: func() time.Time { return now }, MaxPromptDispatch: 1, MisfireGrace: time.Minute}
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.skipped) != 1 || len(store.claims) != 0 || len(dispatcher.runs) != 0 {
		t.Fatalf("skipped=%#v claims=%#v runs=%#v", store.skipped, store.claims, dispatcher.runs)
	}
}

func TestSchedulerRecordsRouteRevokedSlotWithoutDispatch(t *testing.T) {
	now := time.Date(2026, time.July, 13, 4, 0, 0, 0, time.UTC)
	store := &fakeDueStore{due: []DueTask{{ID: "route-revoked", Version: 1, NextRunAt: now, Kind: TaskPrompt, RouteRevoked: true}}}
	dispatcher := &fakeDispatcher{}
	scheduler := Scheduler{Store: store, Dispatch: dispatcher, Now: func() time.Time { return now }, MisfireGrace: time.Minute}
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.routeRevoked) != 1 || store.routeRevoked[0].ID != "route-revoked" || len(store.claims) != 0 || len(dispatcher.runs) != 0 {
		t.Fatalf("routeRevoked=%#v claims=%#v runs=%#v", store.routeRevoked, store.claims, dispatcher.runs)
	}
}

type fakeDueStore struct {
	due          []DueTask
	claims       []DueClaim
	skipped      []DueTask
	routeRevoked []DueTask
}

func (s *fakeDueStore) ListDue(context.Context, time.Time, int) ([]DueTask, error) { return s.due, nil }
func (s *fakeDueStore) ClaimDue(_ context.Context, claim DueClaim) (ClaimedRun, error) {
	s.claims = append(s.claims, claim)
	return ClaimedRun{ID: claim.TaskID + "-run", Kind: TaskPrompt}, nil
}
func (s *fakeDueStore) SkipMisfire(_ context.Context, due DueTask, _ time.Time) error {
	s.skipped = append(s.skipped, due)
	return nil
}
func (s *fakeDueStore) FailRouteRevoked(_ context.Context, due DueTask, _ time.Time) error {
	s.routeRevoked = append(s.routeRevoked, due)
	return nil
}

type fakeDispatcher struct{ runs []ClaimedRun }

func (d *fakeDispatcher) Dispatch(_ context.Context, run ClaimedRun) error {
	d.runs = append(d.runs, run)
	return nil
}
