package nodedeployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestEmitStatus_NilNoop(t *testing.T) {
	var buf bytes.Buffer
	emitStatus(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("nil error wrote %d bytes; want 0", buf.Len())
	}
}

func TestEmitStatus_PreservesAPIStatus(t *testing.T) {
	in := apierrors.NewNotFound(schema.GroupResource{Group: "sei.io", Resource: "seinodedeployments"}, "missing")
	var buf bytes.Buffer
	emitStatus(&buf, in)

	var got metav1.Status
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reason != metav1.StatusReasonNotFound {
		t.Errorf("reason = %q; want NotFound", got.Reason)
	}
	if got.Code != 404 {
		t.Errorf("code = %d; want 404", got.Code)
	}
	if got.Status != metav1.StatusFailure {
		t.Errorf("status = %q; want Failure", got.Status)
	}
}

func TestEmitStatus_GenericErrorWrappedAsInternal(t *testing.T) {
	var buf bytes.Buffer
	emitStatus(&buf, errors.New("boom"))

	var got metav1.Status
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reason != metav1.StatusReasonInternalError {
		t.Errorf("reason = %q; want InternalError", got.Reason)
	}
	if got.Code != 500 {
		t.Errorf("code = %d; want 500", got.Code)
	}
	if got.Message != "boom" {
		t.Errorf("message = %q; want boom", got.Message)
	}
}

func TestUsageError_RoundTripsThroughEmit(t *testing.T) {
	var buf bytes.Buffer
	emitStatus(&buf, usageError("bad flag: %s", "--preset"))

	var got metav1.Status
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Reason != metav1.StatusReasonBadRequest {
		t.Errorf("reason = %q; want BadRequest", got.Reason)
	}
	if got.Code != 400 {
		t.Errorf("code = %d; want 400", got.Code)
	}
}
