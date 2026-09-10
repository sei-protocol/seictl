package cliutil

import (
	"strings"
	"testing"
)

func TestValidateQuantity_Accepts(t *testing.T) {
	// Empty means "flag unset" — the preset default stands.
	cases := []struct{ flag, value string }{
		{"cpu", ""},
		{"memory", ""},
		{"storage", ""},
		{"cpu", "4"},
		{"cpu", "16"},
		{"cpu", "500m"},
		{"memory", "32Gi"},
		{"memory", "128Gi"},
		{"storage", "500Gi"},
		{"storage", "2000Gi"},
	}
	for _, tc := range cases {
		t.Run(tc.flag+"="+tc.value, func(t *testing.T) {
			if err := ValidateQuantity(tc.flag, tc.value); err != nil {
				t.Errorf("ValidateQuantity(%q, %q) = %v; want nil", tc.flag, tc.value, err)
			}
		})
	}
}

func TestValidateQuantity_RejectsUnparseable(t *testing.T) {
	cases := []struct{ flag, value string }{
		{"cpu", "abc"},
		{"memory", "32GB"},
		{"storage", "500 Gi"},
		{"memory", "thirty-two"},
	}
	for _, tc := range cases {
		t.Run(tc.flag+"="+tc.value, func(t *testing.T) {
			err := ValidateQuantity(tc.flag, tc.value)
			if err == nil {
				t.Fatalf("ValidateQuantity(%q, %q) = nil; want an error", tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), "--"+tc.flag) || !strings.Contains(err.Error(), tc.value) {
				t.Errorf("err = %q; want it to name --%s and %q", err.Error(), tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), "not a valid Kubernetes quantity") {
				t.Errorf("err = %q; want the unparseable wording", err.Error())
			}
		})
	}
}

// Zero and negative parse cleanly, so only the sign check catches them.
// The CRD's CEL requires positive values (seinode_types.go:153 requests,
// :196 storage) and would otherwise reject the CR at admission — long
// after the render was committed and merged.
func TestValidateQuantity_RejectsNonPositive(t *testing.T) {
	cases := []struct{ flag, value string }{
		{"cpu", "-1"},
		{"cpu", "0"},
		{"cpu", "-500m"},
		{"memory", "-5Gi"},
		{"memory", "0Gi"},
		{"storage", "-5Gi"},
		{"storage", "0"},
	}
	for _, tc := range cases {
		t.Run(tc.flag+"="+tc.value, func(t *testing.T) {
			err := ValidateQuantity(tc.flag, tc.value)
			if err == nil {
				t.Fatalf("ValidateQuantity(%q, %q) = nil; want an error", tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), "--"+tc.flag) || !strings.Contains(err.Error(), tc.value) {
				t.Errorf("err = %q; want it to name --%s and %q", err.Error(), tc.flag, tc.value)
			}
			if !strings.Contains(err.Error(), "must be positive") {
				t.Errorf("err = %q; want it rejected for not being positive, not as unparseable", err.Error())
			}
		})
	}
}

func TestRejectCPULimit(t *testing.T) {
	cases := []struct {
		name    string
		root    map[string]interface{}
		wantErr bool
	}{
		{"empty object", map[string]interface{}{}, false},
		{
			"requests only",
			map[string]interface{}{"spec": map[string]interface{}{
				"resources": map[string]interface{}{
					"requests": map[string]interface{}{"cpu": "4", "memory": "32Gi"},
				},
			}},
			false,
		},
		{
			// seinode_types.go:154 permits limits.memory when it equals
			// requests.memory; the guard must not widen to the whole block.
			"memory limit allowed",
			map[string]interface{}{"spec": map[string]interface{}{
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{"memory": "32Gi"},
				},
			}},
			false,
		},
		{
			"cpu limit rejected",
			map[string]interface{}{"spec": map[string]interface{}{
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{"cpu": "100m"},
				},
			}},
			true,
		},
		{
			"cpu limit rejected alongside an allowed memory limit",
			map[string]interface{}{"spec": map[string]interface{}{
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{"cpu": "100m", "memory": "32Gi"},
				},
			}},
			true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RejectCPULimit(tc.root)
			if tc.wantErr {
				if err == nil {
					t.Fatal("RejectCPULimit = nil; want an error")
				}
				if !strings.Contains(err.Error(), "spec.resources.limits.cpu") {
					t.Errorf("err = %q; want it to name the offending path", err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("RejectCPULimit = %v; want nil", err)
			}
		})
	}
}
