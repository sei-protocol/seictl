package engine

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// cronParser is a standard cron parser with optional seconds field.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// nextCronTime computes the next firing time for a cron expression after the
// given time. Returns an error if the expression is invalid.
func nextCronTime(expr string, after time.Time) (time.Time, error) {
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return sched.Next(after), nil
}

// ValidateCron checks whether a cron expression is syntactically valid.
func ValidateCron(expr string) error {
	_, err := cronParser.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", expr, err)
	}
	return nil
}
