package storage

import (
	"strings"
	"testing"
)

func TestPersistIncomingMessageSQLMatchesBoundArgumentCount(t *testing.T) {
	if got := strings.Count(persistIncomingMessageSQL, "?"); got != 13 {
		t.Fatalf("persist message placeholders = %d, want 13 bound arguments plus literal status", got)
	}
}
