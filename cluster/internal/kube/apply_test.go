package kube

import "testing"

func TestActionFor(t *testing.T) {
	tests := map[string]struct {
		existed        bool
		oldGen, newGen int64
		want           string
	}{
		"new":       {existed: false, want: "create"},
		"updated":   {existed: true, oldGen: 4, newGen: 5, want: "update"},
		"unchanged": {existed: true, oldGen: 4, newGen: 4, want: "unchanged"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := actionFor(tc.existed, tc.oldGen, tc.newGen); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
