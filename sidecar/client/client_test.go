package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func newTestClient(t *testing.T, handler http.Handler) *SidecarClient {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := NewSidecarClient(srv.URL)
	if err != nil {
		t.Fatalf("NewSidecarClient: %v", err)
	}
	return c
}

func TestStatus_OK(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/status" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(StatusResponse{Status: Ready})
	}))

	resp, err := c.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if resp.Status != Ready {
		t.Errorf("Status = %q, want %q", resp.Status, Ready)
	}
}

func TestStatus_ServerError(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))

	_, err := c.Status(context.Background())
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestSubmitTask_Accepted(t *testing.T) {
	taskID := uuid.New()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/tasks" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Type != TaskTypeSnapshotRestore {
			t.Errorf("Type = %q, want %q", req.Type, TaskTypeSnapshotRestore)
		}
		if req.Params == nil {
			t.Fatal("Params is nil")
		}
		params := *req.Params
		if params["bucket"] != "my-bucket" {
			t.Errorf("bucket = %v, want my-bucket", params["bucket"])
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(TaskSubmitResponse{Id: taskID})
	}))

	task := SnapshotRestoreTask{
		Bucket:  "my-bucket",
		Prefix:  "snapshots",
		Region:  "us-east-1",
		ChainID: "sei-chain",
	}
	id, err := c.SubmitTask(context.Background(), task)
	if err != nil {
		t.Fatalf("SubmitTask() error = %v", err)
	}
	if id != taskID {
		t.Errorf("returned id = %s, want %s", id, taskID)
	}
}

func TestSubmitTask_Scheduled(t *testing.T) {
	taskID := uuid.New()
	cron := "*/5 * * * *"
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.Schedule == nil || req.Schedule.Cron == nil {
			t.Fatal("expected schedule.cron to be set")
		}
		if *req.Schedule.Cron != cron {
			t.Errorf("schedule.cron = %q, want %q", *req.Schedule.Cron, cron)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(TaskSubmitResponse{Id: taskID})
	}))

	id, err := c.SubmitRawTask(context.Background(), TaskRequest{
		Type:     TaskTypeDiscoverPeers,
		Schedule: &Schedule{Cron: &cron},
	})
	if err != nil {
		t.Fatalf("SubmitRawTask() error = %v", err)
	}
	if id != taskID {
		t.Errorf("returned id = %s, want %s", id, taskID)
	}
}

func TestSubmitTask_Busy(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "task already running"})
	}))

	_, err := c.SubmitTask(context.Background(), MarkReadyTask{})
	if !errors.Is(err, ErrBusy) {
		t.Errorf("error = %v, want ErrBusy", err)
	}
}

func TestSubmitTask_BadRequest(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "unknown task type"})
	}))

	_, err := c.SubmitRawTask(context.Background(), TaskRequest{Type: "invalid"})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	if !strings.Contains(err.Error(), "unknown task type") {
		t.Errorf("error = %v, expected to contain 'unknown task type'", err)
	}
}

func TestSubmitTask_ValidationFailure(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("server should not be called when validation fails")
	}))

	_, err := c.SubmitTask(context.Background(), SnapshotRestoreTask{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("error = %v, expected to contain 'validation failed'", err)
	}
}

func TestListTasks_OK(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/tasks" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]TaskResult{
			{Id: uuid.New(), Type: "mark-ready"},
		})
	}))

	results, err := c.ListTasks(context.Background())
	if err != nil {
		t.Fatalf("ListTasks() error = %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Type != "mark-ready" {
		t.Errorf("Type = %q, want mark-ready", results[0].Type)
	}
}

func TestGetTask_OK(t *testing.T) {
	taskID := uuid.New()
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(TaskResult{Id: taskID, Type: "config-patch"})
	}))

	result, err := c.GetTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetTask() error = %v", err)
	}
	if result.Type != "config-patch" {
		t.Errorf("Type = %q, want config-patch", result.Type)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "not found"})
	}))

	_, err := c.GetTask(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestDeleteTask_OK(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	if err := c.DeleteTask(context.Background(), uuid.New()); err != nil {
		t.Fatalf("DeleteTask() error = %v", err)
	}
}

func TestDeleteTask_NotFound(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(ErrorResponse{Error: "not found"})
	}))

	err := c.DeleteTask(context.Background(), uuid.New())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestHealthz_OK(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/healthz" || r.Method != http.MethodGet {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))

	healthy, err := c.Healthz(context.Background())
	if err != nil {
		t.Fatalf("Healthz() error = %v", err)
	}
	if !healthy {
		t.Error("Healthz() = false, want true")
	}
}

func TestHealthz_ServiceUnavailable(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	healthy, err := c.Healthz(context.Background())
	if err != nil {
		t.Fatalf("Healthz() error = %v", err)
	}
	if healthy {
		t.Error("Healthz() = true, want false")
	}
}

func TestHealthz_UnexpectedStatus(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	_, err := c.Healthz(context.Background())
	if err == nil {
		t.Fatal("expected error for 502 response")
	}
}

func TestNewSidecarClientFromPodDNS_URLFormat(t *testing.T) {
	c, err := NewSidecarClientFromPodDNS("sei-node", "default", 7777)
	if err != nil {
		t.Fatalf("NewSidecarClientFromPodDNS: %v", err)
	}
	inner := c.inner.ClientInterface.(*Client)
	want := "http://sei-node-0.sei-node.default.svc.cluster.local:7777/"
	if inner.Server != want {
		t.Errorf("Server = %q, want %q", inner.Server, want)
	}
}

func TestNewSidecarClientFromPodDNS_DefaultPort(t *testing.T) {
	c, err := NewSidecarClientFromPodDNS("mynode", "prod", 0)
	if err != nil {
		t.Fatalf("NewSidecarClientFromPodDNS: %v", err)
	}
	inner := c.inner.ClientInterface.(*Client)
	want := "http://mynode-0.mynode.prod.svc.cluster.local:7777/"
	if inner.Server != want {
		t.Errorf("Server = %q, want %q", inner.Server, want)
	}
}
