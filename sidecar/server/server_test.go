package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

func newTestEngine(t *testing.T, handlers map[engine.TaskType]engine.TaskHandler) *engine.Engine {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return engine.NewEngine(ctx, handlers)
}

func serveHTTP(srv *Server, method, path string, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

func waitForReady(eng *engine.Engine) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if eng.Healthz() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForTaskResult(eng *engine.Engine, id string) *engine.TaskResult {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r := eng.GetResult(id); r != nil && r.CompletedAt != nil {
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	return nil
}

func TestHealthzReturns503BeforeReady(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodGet, "/v0/healthz", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHealthzReturns200AfterMarkReady(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	_, _ = eng.Submit(engine.Task{Type: engine.TaskMarkReady})
	waitForReady(eng)

	rec := serveHTTP(srv, http.MethodGet, "/v0/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestStatusResponse(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodGet, "/v0/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp engine.StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "Initializing" {
		t.Fatalf("expected Initializing initially, got %q", resp.Status)
	}

	_, _ = eng.Submit(engine.Task{Type: engine.TaskMarkReady})
	waitForReady(eng)

	rec = serveHTTP(srv, http.MethodGet, "/v0/status", "")
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Status != "Ready" {
		t.Fatalf("expected Ready after mark-ready, got %q", resp.Status)
	}
}

func TestPostTaskReturnsID(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	body := `{"type":"config-patch","params":{"peers":["a@1.2.3.4:26656"]}}`
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["id"] == "" {
		t.Fatal("expected non-empty id")
	}
}

func TestPostTaskBusy(t *testing.T) {
	blocked := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	eng := engine.NewEngine(ctx, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocked
			return nil
		},
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}
	time.Sleep(20 * time.Millisecond)

	rec = serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}

	close(blocked)
}

func TestPostTaskScheduled(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	body := `{"type":"config-patch","schedule":{"cron":"*/5 * * * *"}}`
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["id"] == "" {
		t.Fatal("expected non-empty id")
	}
}

func TestPostTaskInvalidJSON(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{not json}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskMissingType(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"params":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskUnknownType(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"nonexistent"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskInvalidSchedule(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch","schedule":{"cron":"not a cron"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestListTasksEmpty(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodGet, "/v0/tasks", "")

	var results []engine.TaskResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected empty array, got %d", len(results))
	}
}

func TestListTasksAfterSubmit(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch"}`)
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	waitForTaskResult(eng, resp["id"])

	rec = serveHTTP(srv, http.MethodGet, "/v0/tasks", "")
	var results []engine.TaskResult
	if err := json.NewDecoder(rec.Body).Decode(&results); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestGetTask(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch"}`)
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	id := resp["id"]
	waitForTaskResult(eng, id)

	rec = serveHTTP(srv, http.MethodGet, "/v0/tasks/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result engine.TaskResult
	_ = json.NewDecoder(rec.Body).Decode(&result)
	if result.ID != id {
		t.Fatalf("expected ID %q, got %q", id, result.ID)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	eng := newTestEngine(t, nil)
	srv := NewServer(":0", eng)
	rec := serveHTTP(srv, http.MethodGet, "/v0/tasks/nonexistent", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteTask(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch"}`)
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	id := resp["id"]
	waitForTaskResult(eng, id)

	rec = serveHTTP(srv, http.MethodDelete, "/v0/tasks/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	rec = serveHTTP(srv, http.MethodDelete, "/v0/tasks/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestDeleteScheduledTask(t *testing.T) {
	eng := newTestEngine(t, map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/v0/tasks", `{"type":"config-patch","schedule":{"cron":"*/5 * * * *"}}`)
	var resp map[string]string
	_ = json.NewDecoder(rec.Body).Decode(&resp)
	id := resp["id"]

	rec = serveHTTP(srv, http.MethodDelete, "/v0/tasks/"+id, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	rec = serveHTTP(srv, http.MethodGet, "/v0/tasks/"+id, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}
