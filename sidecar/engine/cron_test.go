package engine

import (
	"testing"
)

func TestValidateCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"empty string", "", true},
		{"invalid expression", "not a cron", true},
		{"too few fields", "* * *", true},
		{"valid every 5 min", "*/5 * * * *", false},
		{"valid daily midnight", "0 0 * * *", false},
		{"valid monthly", "0 0 1 * *", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCron(tc.expr)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
