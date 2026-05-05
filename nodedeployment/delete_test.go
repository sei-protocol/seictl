package nodedeployment

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestParseCascade(t *testing.T) {
	cases := []struct {
		raw  string
		want metav1.DeletionPropagation
	}{
		{"", metav1.DeletePropagationForeground},
		{"foreground", metav1.DeletePropagationForeground},
		{"background", metav1.DeletePropagationBackground},
		{"orphan", metav1.DeletePropagationOrphan},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := parseCascade(tc.raw)
			if err != nil {
				t.Fatalf("parseCascade(%q): %v", tc.raw, err)
			}
			if *got != tc.want {
				t.Errorf("got %q; want %q", *got, tc.want)
			}
		})
	}

	for _, bad := range []string{"async", "FOREGROUND", "delete"} {
		t.Run("bad/"+bad, func(t *testing.T) {
			if _, err := parseCascade(bad); err == nil {
				t.Errorf("expected error for %q", bad)
			}
		})
	}
}
