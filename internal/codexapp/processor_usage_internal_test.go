package codexapp

import (
	"context"
	"testing"

	"github.com/kid0317/codex-workspace-bot/internal/observability"
	"github.com/kid0317/codex-workspace-bot/internal/worker"
)

type capturedUsageLedger struct {
	records []observability.TurnUsageRecord
}

func (l *capturedUsageLedger) RecordTurnUsage(_ context.Context, record observability.TurnUsageRecord) (observability.SessionUsageTotal, bool, error) {
	l.records = append(l.records, record)
	return observability.SessionUsageTotal{Usage: record.Usage, CompletedTurnCount: int64(len(l.records))}, true, nil
}

func (l *capturedUsageLedger) RecordThreadUsageSnapshot(_ context.Context, record observability.ThreadUsageSnapshotRecord) (observability.SessionUsageTotal, error) {
	l.records = append(l.records, observability.TurnUsageRecord{TraceID: record.TraceID, ThreadID: record.ThreadID, TurnID: record.TurnID, Session: record.Session, Usage: record.Usage})
	return observability.SessionUsageTotal{Usage: record.Usage, CompletedTurnCount: int64(len(l.records))}, nil
}

func TestProcessorRecordsOnlyAuthoritativeCompletedUsageWithPlaintextSessionKey(t *testing.T) {
	ledger := &capturedUsageLedger{}
	processor := Processor{UsageLedger: ledger}
	usage := observability.Usage{InputTokens: 100, OutputTokens: 30, CachedInputTokens: 60, ReasoningOutputTokens: 10, TotalTokens: 130}
	total, err := processor.recordCompletedUsage(context.Background(), worker.Batch{Runtime: worker.AppRuntime{ID: "app-1"}, Messages: []worker.Message{{TraceID: "0123456789abcdef0123456789abcdef", ChatType: "p2p", ChatID: "oc-real"}}}, []TurnCompleted{{ThreadID: "thread-1", TurnID: "turn-1", Usage: &usage}, {ThreadID: "turn-missing", Status: "completed"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.records) != 1 {
		t.Fatalf("records = %#v", ledger.records)
	}
	got := ledger.records[0]
	if got.TraceID != "0123456789abcdef0123456789abcdef" || got.Session != (observability.SessionUsageKey{AppID: "app-1", ChatType: "p2p", ChatID: "oc-real"}) || got.Usage != usage {
		t.Fatalf("record = %#v", got)
	}
	if total.CompletedTurnCount != 1 || total.TotalTokens != 130 {
		t.Fatalf("total = %#v", total)
	}
}
