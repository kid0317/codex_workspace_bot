package schedule

import (
	"testing"
	"time"
)

func TestParseCronAcceptsFiveFieldsInShanghai(t *testing.T) {
	schedule, err := ParseCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("ParseCron() error = %v", err)
	}
	if got, want := schedule.Timezone(), "Asia/Shanghai"; got != want {
		t.Fatalf("Timezone() = %q, want %q", got, want)
	}

	from := time.Date(2026, time.July, 13, 1, 2, 3, 0, time.UTC)
	got := schedule.Next(from)
	want := time.Date(2026, time.July, 13, 1, 5, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next(%s) = %s, want %s", from, got, want)
	}
}

func TestParseCronRejectsUnsupportedSyntax(t *testing.T) {
	for _, expression := range []string{
		"",
		"@hourly",
		"TZ=UTC 0 * * * *",
		"CRON_TZ=UTC 0 * * * *",
		"0 0 * * * *",
		"0 * * *",
	} {
		if _, err := ParseCron(expression); err == nil {
			t.Fatalf("ParseCron(%q) error = nil, want rejection", expression)
		}
	}
}
