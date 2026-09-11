package cliutil

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyNodeIsolation(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{"exact", "Dedicated", "Dedicated", ""},
		{"case-insensitive", "shared", "Shared", ""},
		{"unset leaves field absent", "", "", ""},
		{"unknown", "Isolated", "", "not one of Shared, Dedicated"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := map[string]interface{}{"spec": map[string]interface{}{}}
			err := ApplyNodeIsolation(root, tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got, found, _ := unstructured.NestedString(root, "spec", "scheduling", "nodeIsolation")
			if tc.want == "" {
				if found {
					t.Fatalf("field must stay unset, got %q", got)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
