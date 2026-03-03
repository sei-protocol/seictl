package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sei-protocol/seictl/sei-sidecar/engine"
)

func newTestEngine(handlers map[engine.TaskType]engine.TaskHandler) *engine.Engine {
	return engine.NewEngine(handlers)
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

func TestHealthzReturns503BeforeReady(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodGet, "/healthz", "")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHealthzReturns200AfterMarkReady(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	eng.Submit(engine.Task{Type: engine.TaskMarkReady})
	waitForReady(eng)

	rec := serveHTTP(srv, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestStatusResponse(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodGet, "/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp engine.StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.Ready {
		t.Fatal("expected not ready initially")
	}

	eng.Submit(engine.Task{Type: engine.TaskMarkReady})
	waitForReady(eng)

	rec = serveHTTP(srv, http.MethodGet, "/status", "")
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if !resp.Ready {
		t.Fatal("expected ready after mark-ready")
	}
}

func TestPostTaskImmediate(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	body := `{"type":"config-patch","params":{"peers":["a@1.2.3.4:26656"]}}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var resp map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp["status"] != "submitted" {
		t.Fatalf("expected status 'submitted', got %q", resp["status"])
	}
}

func TestPostTaskSchedule(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskUpdatePeers: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	body := `{"type":"update-peers","schedule":"*/5 * * * *"}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var sched engine.Schedule
	if err := json.NewDecoder(rec.Body).Decode(&sched); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if sched.ID == "" {
		t.Fatal("expected non-empty schedule ID")
	}
	if !sched.Enabled {
		t.Fatal("expected enabled")
	}
}

func TestPostTaskInvalidJSON(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodPost, "/task", `{not json}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskMissingType(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodPost, "/task", `{"params":{}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskUnknownType(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodPost, "/task", `{"type":"nonexistent"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestPostTaskInvalidSchedule(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodPost, "/task", `{"type":"update-peers","schedule":"not a cron"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestListSchedulesEmpty(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodGet, "/schedules", "")

	var schedules []engine.Schedule
	if err := json.NewDecoder(rec.Body).Decode(&schedules); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(schedules) != 0 {
		t.Fatalf("expected empty array, got %d", len(schedules))
	}
}

func TestListSchedulesAfterCreate(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	eng.AddSchedule(engine.TaskUpdatePeers, nil, "*/5 * * * *")

	rec := serveHTTP(srv, http.MethodGet, "/schedules", "")
	var schedules []engine.Schedule
	if err := json.NewDecoder(rec.Body).Decode(&schedules); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
}

func TestDeleteSchedule(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	sched, _ := eng.AddSchedule(engine.TaskUpdatePeers, nil, "*/5 * * * *")

	rec := serveHTTP(srv, http.MethodDelete, "/schedules/"+sched.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	rec = serveHTTP(srv, http.MethodDelete, "/schedules/"+sched.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestDeleteScheduleNotFound(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodDelete, "/schedules/nonexistent", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}
