package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const timezone = "Asia/Shanghai"

// Cron is a validated five-field Cron expression evaluated only in Shanghai.
// Its Next result is always normalized to UTC for database persistence.
type Cron struct {
	schedule cron.Schedule
	location *time.Location
}

func ParseCron(expression string) (Cron, error) {
	trimmed := strings.TrimSpace(expression)
	if trimmed == "" {
		return Cron{}, fmt.Errorf("schedule cron expression is required")
	}
	if strings.HasPrefix(trimmed, "@") || strings.Contains(trimmed, "TZ=") || strings.Contains(trimmed, "CRON_TZ=") {
		return Cron{}, fmt.Errorf("schedule cron expression must use five fields in %s", timezone)
	}
	if len(strings.Fields(trimmed)) != 5 {
		return Cron{}, fmt.Errorf("schedule cron expression must have five fields")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Cron{}, fmt.Errorf("load %s: %w", timezone, err)
	}
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	parsed, err := parser.Parse(trimmed)
	if err != nil {
		return Cron{}, fmt.Errorf("parse schedule cron: %w", err)
	}
	return Cron{schedule: parsed, location: location}, nil
}

func (c Cron) Timezone() string { return timezone }

func (c Cron) Next(after time.Time) time.Time {
	if c.schedule == nil || c.location == nil {
		return time.Time{}
	}
	return c.schedule.Next(after.In(c.location)).UTC()
}
