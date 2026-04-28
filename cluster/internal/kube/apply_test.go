package kube

import "testing"

func TestActionFor(t *testing.T) {
	tests := map[string]struct {
		existed      bool
		oldRV, newRV string
		want         string
	}{
		"new":       {existed: false, want: "create"},
		"updated":   {existed: true, oldRV: "10", newRV: "11", want: "update"},
		"unchanged": {existed: true, oldRV: "10", newRV: "10", want: "unchanged"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := actionFor(tc.existed, tc.oldRV, tc.newRV); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
