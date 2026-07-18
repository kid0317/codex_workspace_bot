package schedule

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

func TestRepositoryCreateResultDeliverySuppressesSilentRunWithoutExternalClaim(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 2, 3, 4, 0, time.UTC)
	repo := Repository{DB: db, Now: func() time.Time { return now }, NewID: func() string { return "delivery-1" }}
	mock.ExpectExec("INSERT INTO scheduled_task_deliveries .* SELECT").
		WithArgs("delivery-1", now, "run-1").
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery("SELECT id,run_id,delivery_kind,attempt,stage,outcome FROM scheduled_task_deliveries").
		WithArgs("run-1", "result_card", 1).
		WillReturnRows(sqlmock.NewRows([]string{"id", "run_id", "delivery_kind", "attempt", "stage", "outcome"}).AddRow("delivery-1", "run-1", "result_card", 1, nil, "suppressed"))

	intent, err := repo.CreateResultDelivery(context.Background(), "run-1", true)
	if err != nil {
		t.Fatalf("CreateResultDelivery() error = %v", err)
	}
	if intent.ID != "delivery-1" || intent.Stage != "" || intent.Outcome != DeliverySuppressed || intent.Kind != DeliveryResultCard || intent.Attempt != 1 {
		t.Fatalf("CreateResultDelivery() = %#v", intent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCreateResultDeliveryRejectsNonTerminalRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := Repository{DB: db, NewID: func() string { return "delivery-1" }}
	mock.ExpectExec("INSERT INTO scheduled_task_deliveries .* SELECT").
		WithArgs("delivery-1", sqlmock.AnyArg(), "run-claimed").
		WillReturnResult(sqlmock.NewResult(0, 0))
	_, err = repo.CreateResultDelivery(context.Background(), "run-claimed", false)
	if !errors.Is(err, ErrRunClaimLost) {
		t.Fatalf("CreateResultDelivery() error=%v want ErrRunClaimLost", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryClaimDeliveryTransitionsOnlyPendingIntent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectExec("UPDATE scheduled_task_deliveries SET stage='in_flight'").
		WithArgs("delivery-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	claimed, err := (&Repository{DB: db}).ClaimDelivery(context.Background(), "delivery-1")
	if err != nil || !claimed {
		t.Fatalf("ClaimDelivery() claimed=%t err=%v", claimed, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRejectedPrimaryCreatesExactlyOneFallbackIntent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 2, 3, 4, 0, time.UTC)
	repo := Repository{DB: db, Now: func() time.Time { return now }, NewID: func() string { return "fallback-1" }}
	mock.ExpectExec("UPDATE scheduled_task_deliveries SET stage=NULL,outcome=\\?").
		WithArgs("rejected", nil, now, "primary-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO scheduled_task_deliveries .* SELECT").
		WithArgs("fallback-1", "run-1").
		WillReturnResult(sqlmock.NewResult(1, 1))

	completed, err := repo.CompleteDelivery(context.Background(), "primary-1", DeliveryRejected, "")
	if err != nil || !completed {
		t.Fatalf("CompleteDelivery() completed=%t err=%v", completed, err)
	}
	fallback, err := repo.CreateFallbackAfterRejected(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("CreateFallbackAfterRejected() error=%v", err)
	}
	if fallback.ID != "fallback-1" || fallback.Kind != DeliveryFallbackText || fallback.Attempt != 1 || fallback.Stage != DeliveryPending || fallback.Outcome != "" {
		t.Fatalf("fallback=%#v", fallback)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryReconcileInFlightDeliveriesMarksUnknownWithoutRetry(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Date(2026, time.July, 13, 2, 3, 4, 0, time.UTC)
	mock.ExpectExec("UPDATE scheduled_task_deliveries SET stage=NULL,outcome='unknown',completed_at=\\?").WithArgs(now).WillReturnResult(sqlmock.NewResult(0, 2))
	count, err := (&Repository{DB: db}).ReconcileInFlightDeliveries(context.Background(), now)
	if err != nil || count != 2 {
		t.Fatalf("ReconcileInFlightDeliveries() count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResultDeliveryDispatcherClaimsAndSendsOneStaticPromptCard(t *testing.T) {
	store := &fakeResultDeliveryStore{route: DeliveryRoute{RunID: "run-1", Kind: TaskPrompt, Silent: false, ReplyID: "ou-1", ReplyType: "open_id"}, intent: DeliveryIntent{ID: "delivery-1", RunID: "run-1", Kind: DeliveryResultCard, Attempt: 1, Stage: DeliveryPending}}
	sender := &fakeResultSender{cardID: "om-1"}
	dispatcher := ResultDeliveryDispatcher{Store: store, Sender: sender}
	if err := dispatcher.Deliver(context.Background(), "run-1", ResultPresentation{Succeeded: true, FinalText: "done"}); err != nil {
		t.Fatalf("Deliver() error=%v", err)
	}
	if sender.cardText == "" || sender.textCalls != 0 || store.claimedID != "delivery-1" || store.completedOutcome != DeliverySent || store.completedHMAC != "hmac:om-1" {
		t.Fatalf("sender=%#v store=%#v", sender, store)
	}
}

func TestResultDeliveryDispatcherDoesNotFallbackAfterUnknownCardSend(t *testing.T) {
	store := &fakeResultDeliveryStore{route: DeliveryRoute{RunID: "run-1", Kind: TaskPrompt, Silent: false, ReplyID: "ou-1", ReplyType: "open_id"}, intent: DeliveryIntent{ID: "delivery-1", RunID: "run-1", Kind: DeliveryResultCard, Attempt: 1, Stage: DeliveryPending}}
	sender := &fakeResultSender{cardErr: errors.New("transport interrupted")}
	if err := (ResultDeliveryDispatcher{Store: store, Sender: sender}).Deliver(context.Background(), "run-1", ResultPresentation{Succeeded: false, ErrorCode: "failed_turn"}); err != nil {
		t.Fatalf("Deliver() error=%v", err)
	}
	if sender.textCalls != 0 || store.completedOutcome != DeliveryUnknown || store.fallbackCalls != 0 {
		t.Fatalf("sender=%#v store=%#v", sender, store)
	}
}

func TestResultDeliveryDispatcherCreatesSuppressedIntentBeforeLoadingRoute(t *testing.T) {
	store := &fakeResultDeliveryStore{
		intent:   DeliveryIntent{ID: "delivery-silent", RunID: "run-silent", Kind: DeliveryResultCard, Attempt: 1, Outcome: DeliverySuppressed},
		routeErr: errors.New("route revoked"),
	}
	if err := (ResultDeliveryDispatcher{Store: store}).Deliver(context.Background(), "run-silent", ResultPresentation{}); err != nil {
		t.Fatalf("Deliver() error=%v", err)
	}
	if store.createCalls != 1 || store.routeCalls != 0 || store.claimedID != "" {
		t.Fatalf("store=%#v", store)
	}
}

func TestResultDeliveryDispatcherRejectsIncompletePrimaryCompletion(t *testing.T) {
	completed := false
	store := &fakeResultDeliveryStore{
		route:          DeliveryRoute{RunID: "run-1", Kind: TaskPrompt, ReplyID: "ou-1", ReplyType: "open_id"},
		intent:         DeliveryIntent{ID: "delivery-1", RunID: "run-1", Kind: DeliveryResultCard, Attempt: 1, Stage: DeliveryPending},
		completeResult: &completed,
	}
	err := (ResultDeliveryDispatcher{Store: store, Sender: &fakeResultSender{cardID: "om-1"}}).Deliver(context.Background(), "run-1", ResultPresentation{})
	if !errors.Is(err, ErrDeliveryCompletionLost) {
		t.Fatalf("Deliver() error=%v want ErrDeliveryCompletionLost", err)
	}
}

func TestResultDeliveryDispatcherRejectsIncompleteFallbackCompletion(t *testing.T) {
	completed := false
	store := &fakeResultDeliveryStore{
		route:          DeliveryRoute{RunID: "run-1", Kind: TaskPrompt, ReplyID: "ou-1", ReplyType: "open_id"},
		intent:         DeliveryIntent{ID: "delivery-1", RunID: "run-1", Kind: DeliveryResultCard, Attempt: 1, Stage: DeliveryPending},
		fallback:       DeliveryIntent{ID: "fallback-1", RunID: "run-1", Kind: DeliveryFallbackText, Attempt: 1, Stage: DeliveryPending},
		completeResult: &completed,
	}
	err := (ResultDeliveryDispatcher{Store: store, Sender: &fakeResultSender{cardErr: worker.ErrCommandDeliveryRejected}}).Deliver(context.Background(), "run-1", ResultPresentation{})
	if !errors.Is(err, ErrDeliveryCompletionLost) {
		t.Fatalf("Deliver() error=%v want ErrDeliveryCompletionLost", err)
	}
}

func TestRepositoryLoadDeliveryRouteUsesPlaintextOwnerForTerminalRun(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("SELECT t.app_id,t.chat_group_id,t.kind,t.creator_open_id,cg.chat_type,cg.chat_id,sr.silent FROM scheduled_task_runs").
		WithArgs("run-1").
		WillReturnRows(sqlmock.NewRows([]string{"app_id", "chat_group_id", "kind", "creator_open_id", "chat_type", "chat_id", "silent"}).AddRow("app-1", "group-1", "prompt", "ou-1", "p2p", "oc-unused", false))
	route, err := (&Repository{DB: db}).LoadDeliveryRoute(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("LoadDeliveryRoute() error=%v", err)
	}
	if route.RunID != "run-1" || route.Kind != TaskPrompt || route.ReplyID != "ou-1" || route.ReplyType != "open_id" || route.Silent {
		t.Fatalf("route=%#v", route)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type fakeResultDeliveryStore struct {
	route            DeliveryRoute
	intent           DeliveryIntent
	claimedID        string
	completedOutcome DeliveryOutcome
	completedHMAC    string
	fallback         DeliveryIntent
	fallbackCalls    int
	routeErr         error
	routeCalls       int
	createCalls      int
	completeResult   *bool
}

func (s *fakeResultDeliveryStore) LoadDeliveryRoute(context.Context, string) (DeliveryRoute, error) {
	s.routeCalls++
	return s.route, s.routeErr
}
func (s *fakeResultDeliveryStore) CreateResultDelivery(context.Context, string, bool) (DeliveryIntent, error) {
	s.createCalls++
	return s.intent, nil
}
func (s *fakeResultDeliveryStore) ClaimDelivery(_ context.Context, id string) (bool, error) {
	s.claimedID = id
	return true, nil
}
func (s *fakeResultDeliveryStore) CompleteDelivery(_ context.Context, _ string, outcome DeliveryOutcome, hmac string) (bool, error) {
	s.completedOutcome, s.completedHMAC = outcome, hmac
	if s.completeResult != nil {
		return *s.completeResult, nil
	}
	return true, nil
}
func (s *fakeResultDeliveryStore) CreateFallbackAfterRejected(context.Context, string) (DeliveryIntent, error) {
	s.fallbackCalls++
	return s.fallback, nil
}
func (s *fakeResultDeliveryStore) MessageIDHMAC(id string) (string, error) { return "hmac:" + id, nil }

type fakeResultSender struct {
	cardID, cardText string
	cardErr          error
	textCalls        int
}

func (s *fakeResultSender) SendStaticCard(_ context.Context, _, _ string, text string) (string, error) {
	s.cardText = text
	return s.cardID, s.cardErr
}
func (s *fakeResultSender) SendCommandText(context.Context, string, string, string) (string, error) {
	s.textCalls++
	return "", nil
}
