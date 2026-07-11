package task

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	sidecar "github.com/sei-protocol/seictl/sidecar/client"
)

func rawResult(t *testing.T, r snapshotUploadResult) *json.RawMessage {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	msg := json.RawMessage(b)
	return &msg
}

func TestClassifyUpload(t *testing.T) {
	errMsg := "s3 put failed"
	tests := []struct {
		name       string
		res        *sidecar.TaskResult
		wantErr    bool
		wantReason metav1.StatusReason
		wantMsg    string // substring, only checked when wantErr is false
	}{
		{
			name:    "uploaded is success",
			res:     &sidecar.TaskResult{Status: sidecar.Completed, Result: rawResult(t, snapshotUploadResult{Outcome: sidecar.OutcomeUploaded, Height: 42, Key: "snap/42.tar"})},
			wantMsg: "height 42",
		},
		{
			name:    "noop is healthy success",
			res:     &sidecar.TaskResult{Status: sidecar.Completed, Result: rawResult(t, snapshotUploadResult{Outcome: sidecar.OutcomeNoop, NoopReason: sidecar.NoopAlreadyUploaded})},
			wantMsg: "noop (already-uploaded)",
		},
		{
			name:       "failed carries the error",
			res:        &sidecar.TaskResult{Status: sidecar.Failed, Error: &errMsg},
			wantErr:    true,
			wantReason: metav1.StatusReasonInternalError,
		},
		{
			name:       "completed without recognized outcome is an error",
			res:        &sidecar.TaskResult{Status: sidecar.Completed, Result: rawResult(t, snapshotUploadResult{Outcome: "weird"})},
			wantErr:    true,
			wantReason: metav1.StatusReasonInternalError,
		},
		{
			name:       "completed with no result body is an error",
			res:        &sidecar.TaskResult{Status: sidecar.Completed},
			wantErr:    true,
			wantReason: metav1.StatusReasonInternalError,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := classifyUpload(tc.res)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got msg %q", msg)
				}
				var se *apierrors.StatusError
				if !errors.As(err, &se) {
					t.Fatalf("error is not a StatusError: %v", err)
				}
				if se.ErrStatus.Reason != tc.wantReason {
					t.Errorf("reason = %q, want %q", se.ErrStatus.Reason, tc.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("msg = %q, want substring %q", msg, tc.wantMsg)
			}
		})
	}
}

// fakeSidecar serves POST /v0/tasks and GET /v0/tasks/{id}. It echoes the
// caller-supplied task ID and returns `running` until the GET count reaches
// completeAfter, then the terminal result. GET calls at or below errorGetsUntil
// return 503 first, standing in for a transient rbac-proxy blip mid-poll.
type fakeSidecar struct {
	completeAfter  int32
	errorGetsUntil int32
	terminal       sidecar.TaskResult
	submittedID    atomic.Value // uuid.UUID observed on POST
	gets           atomic.Int32
}

func (f *fakeSidecar) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v0/tasks", func(w http.ResponseWriter, r *http.Request) {
		var req sidecar.TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode submit body: %v", err)
		}
		if req.Type != sidecar.TaskTypeSnapshotUploadOnce {
			t.Errorf("Type = %q, want %q", req.Type, sidecar.TaskTypeSnapshotUploadOnce)
		}
		if req.Id == nil {
			t.Error("submit request carried no task ID; snapshot-upload must send a fresh ID")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.submittedID.Store(*req.Id)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(sidecar.TaskSubmitResponse{Id: *req.Id})
	})
	mux.HandleFunc("GET /v0/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		n := f.gets.Add(1)
		if n <= f.errorGetsUntil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		res := sidecar.TaskResult{Id: f.terminal.Id, Type: sidecar.TaskTypeSnapshotUploadOnce}
		if n >= f.completeAfter {
			res = f.terminal
		} else {
			res.Status = sidecar.Running
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(res)
	})
	return mux
}

func TestRunSnapshotUpload_PollsToTerminal(t *testing.T) {
	id := uuid.New()
	fake := &fakeSidecar{
		completeAfter: 3,
		terminal: sidecar.TaskResult{
			Id:     id,
			Status: sidecar.Completed,
			Result: rawResult(t, snapshotUploadResult{Outcome: sidecar.OutcomeUploaded, Height: 100, Key: "snap/100.tar"}),
		},
	}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	sc, err := sidecar.NewSidecarClient(srv.URL)
	if err != nil {
		t.Fatalf("NewSidecarClient: %v", err)
	}

	res, err := runSnapshotUpload(context.Background(), sc, id, time.Millisecond)
	if err != nil {
		t.Fatalf("runSnapshotUpload: %v", err)
	}
	if res.Status != sidecar.Completed {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if got := fake.submittedID.Load(); got != id {
		t.Errorf("submitted ID = %v, want %v (fresh caller ID must reach the sidecar)", got, id)
	}
	if _, cerr := classifyUpload(res); cerr != nil {
		t.Errorf("classifyUpload on uploaded result errored: %v", cerr)
	}
}

// A transient GET failure (e.g. an rbac-proxy restart) must not abort the run:
// the sidecar keeps uploading, so the poll has to survive the blip and read the
// eventual terminal rather than fail and trigger a redundant resubmit.
func TestPollUntilTerminal_SurvivesTransientGetError(t *testing.T) {
	id := uuid.New()
	fake := &fakeSidecar{
		completeAfter:  3,
		errorGetsUntil: 1, // first GET 503s, then running, then terminal
		terminal: sidecar.TaskResult{
			Id:     id,
			Status: sidecar.Completed,
			Result: rawResult(t, snapshotUploadResult{Outcome: sidecar.OutcomeUploaded, Height: 7, Key: "snap/7.tar"}),
		},
	}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	sc, err := sidecar.NewSidecarClient(srv.URL)
	if err != nil {
		t.Fatalf("NewSidecarClient: %v", err)
	}

	res, err := runSnapshotUpload(context.Background(), sc, id, time.Millisecond)
	if err != nil {
		t.Fatalf("runSnapshotUpload should have survived one transient GET error: %v", err)
	}
	if res.Status != sidecar.Completed {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if got := fake.gets.Load(); got < 3 {
		t.Errorf("GET count = %d, want >= 3 (the run must poll past the 503)", got)
	}
}

func TestRunSnapshotUpload_Timeout(t *testing.T) {
	id := uuid.New()
	// completeAfter far beyond what the short deadline allows: always running.
	fake := &fakeSidecar{completeAfter: 1 << 30, terminal: sidecar.TaskResult{Id: id}}
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)
	sc, err := sidecar.NewSidecarClient(srv.URL)
	if err != nil {
		t.Fatalf("NewSidecarClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = runSnapshotUpload(ctx, sc, id, 5*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if ctx.Err() == nil {
		t.Errorf("context should have expired; err = %v", err)
	}
}
