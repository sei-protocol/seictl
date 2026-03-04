package engine

import (
	"testing"
)

func TestValidateCronRejectsEmpty(t *testing.T) {
	if err := ValidateCron(""); err == nil {
		t.Fatal("expected error when cron is empty")
	}
}

func TestValidateCronRejectsInvalid(t *testing.T) {
	if err := ValidateCron("not a cron"); err == nil {
		t.Fatal("expected error for invalid cron")
	}
}

func TestValidateCronAcceptsValid(t *testing.T) {
	if err := ValidateCron("*/5 * * * *"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
