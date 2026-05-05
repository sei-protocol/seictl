package nodedeployment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func sndWithPhase(phase, failedErr string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "sei.io/v1alpha1",
		"kind":       "SeiNodeDeployment",
		"metadata":   map[string]interface{}{"name": "demo", "namespace": "nightly"},
	}}
	if phase != "" {
		_ = unstructured.SetNestedField(obj.Object, phase, "status", "phase")
	}
	if failedErr != "" {
		_ = unstructured.SetNestedField(obj.Object, failedErr, "status", "plan", "failedTaskDetail", "error")
	}
	return obj
}

func TestMatchPhase(t *testing.T) {
	cases := []struct {
		name      string
		obj       *unstructured.Unstructured
		until     string
		wantDone  bool
		wantError string
	}{
		{"match", sndWithPhase("Ready", ""), "Ready", true, ""},
		{"no phase yet", sndWithPhase("", ""), "Ready", false, ""},
		{"intermediate phase", sndWithPhase("Provisioning", ""), "Ready", false, ""},
		{"failed surfaces error", sndWithPhase("Failed", "task seid-init failed: oom"), "Ready", false, "terminal Failed phase: task seid-init failed: oom"},
		{"failed without detail", sndWithPhase("Failed", ""), "Ready", false, "(no failedTaskDetail.error on status.plan)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			done, err := matchPhase(tc.obj, tc.until)
			if done != tc.wantDone {
				t.Errorf("done = %v; want %v", done, tc.wantDone)
			}
			if tc.wantError == "" && err != nil {
				t.Errorf("err = %v; want nil", err)
			}
			if tc.wantError != "" {
				if err == nil {
					t.Fatalf("err = nil; want containing %q", tc.wantError)
				}
				if !strings.Contains(err.Error(), tc.wantError) {
					t.Errorf("err = %q; want containing %q", err.Error(), tc.wantError)
				}
			}
		})
	}
}

func TestWatchExitError_TimeoutShapedAsStatus(t *testing.T) {
	err := watchExitError(context.DeadlineExceeded, "demo", "nightly", "Ready", 5*time.Minute)
	var apiErr apierrors.APIStatus
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v; want APIStatus", err)
	}
	s := apiErr.Status()
	if s.Reason != metav1.StatusReasonTimeout {
		t.Errorf("reason = %q; want Timeout", s.Reason)
	}
	if s.Code != 504 {
		t.Errorf("code = %d; want 504", s.Code)
	}
	if !strings.Contains(s.Message, "nightly/demo") || !strings.Contains(s.Message, "Ready") {
		t.Errorf("message %q missing namespace/name/until", s.Message)
	}
}

func TestWatchExitError_NonTimeoutPassesThrough(t *testing.T) {
	in := errors.New("some watch error")
	out := watchExitError(in, "demo", "nightly", "Ready", time.Minute)
	if !errors.Is(out, in) {
		t.Errorf("non-timeout err must pass through unchanged; got %v", out)
	}
}
