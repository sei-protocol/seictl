package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sei-protocol/platform/sei-sidecar/engine"
)

func noopBlockHeight() (int64, error) { return 0, nil }

func newTestEngine(handlers map[engine.TaskType]engine.TaskHandler) *engine.Engine {
	return engine.NewEngine("/tmp", handlers, noopBlockHeight)
}

func drainEngine(t *testing.T, eng *engine.Engine) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		eng.DrainUpdates()
		if eng.Status().Phase != engine.PhaseTaskRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for task to complete")
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
	drainEngine(t, eng)

	rec := serveHTTP(srv, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestHealthzTransition503To200(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskMarkReady: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("before mark-ready: expected 503, got %d", rec.Code)
	}

	eng.Submit(engine.Task{Type: engine.TaskMarkReady})
	drainEngine(t, eng)

	rec = serveHTTP(srv, http.MethodGet, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("after mark-ready: expected 200, got %d", rec.Code)
	}
}

func TestStatusResponseShape(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodGet, "/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var resp StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Phase != string(engine.PhaseInitialized) {
		t.Fatalf("expected Initialized phase, got %s", resp.Phase)
	}

	eng.Submit(engine.Task{Type: engine.TaskConfigPatch})
	drainEngine(t, eng)

	rec = serveHTTP(srv, http.MethodGet, "/status", "")
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode status response: %v", err)
	}
	if resp.Phase != string(engine.PhaseTaskComplete) {
		t.Fatalf("expected TaskComplete phase, got %s", resp.Phase)
	}
	if resp.LastTask != string(engine.TaskConfigPatch) {
		t.Fatalf("expected lastTask config-patch, got %s", resp.LastTask)
	}
	if resp.LastTaskResult != "success" {
		t.Fatalf("expected lastTaskResult success, got %s", resp.LastTaskResult)
	}
}

func TestStatusResponseAfterFailure(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			return context.DeadlineExceeded
		},
	})
	srv := NewServer(":0", eng)

	eng.Submit(engine.Task{Type: engine.TaskConfigPatch})
	drainEngine(t, eng)

	rec := serveHTTP(srv, http.MethodGet, "/status", "")
	var resp StatusResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if resp.LastTaskResult != "error" {
		t.Fatalf("expected error, got %s", resp.LastTaskResult)
	}
}

func TestPostTaskAccepts202WithJSON(t *testing.T) {
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
		t.Fatalf("failed to decode response: %v", err)
	}
	if resp["id"] == "" {
		t.Fatal("expected non-empty task ID in response")
	}
	if resp["status"] != "running" {
		t.Fatalf("expected status 'running', got %q", resp["status"])
	}
}

func TestPostTaskRejects409WhenBusy(t *testing.T) {
	blocker := make(chan struct{})
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error {
			<-blocker
			return nil
		},
	})
	srv := NewServer(":0", eng)

	rec := serveHTTP(srv, http.MethodPost, "/task", `{"type":"config-patch"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first submit: expected 202, got %d", rec.Code)
	}

	rec = serveHTTP(srv, http.MethodPost, "/task", `{"type":"config-patch"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second submit: expected 409, got %d", rec.Code)
	}
	close(blocker)
}

func TestPostTaskInvalidJSON(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodPost, "/task", `{not json}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestScheduleUpgradeRouting(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	body := `{"type":"schedule-upgrade","params":{"height":196000000,"image":"sei/seid:v5.0.0"}}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	status := eng.Status()
	if status.PendingUpgrades != 1 {
		t.Fatalf("expected 1 pending upgrade, got %d", status.PendingUpgrades)
	}
}

func TestScheduleUpgradeValidationBadHeight(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))

	body := `{"type":"schedule-upgrade","params":{"height":0,"image":"sei/seid:v5.0.0"}}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for zero height, got %d", rec.Code)
	}

	body = `{"type":"schedule-upgrade","params":{"height":-1,"image":"sei/seid:v5.0.0"}}`
	rec = serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative height, got %d", rec.Code)
	}
}

func TestScheduleUpgradeValidationEmptyImage(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))

	body := `{"type":"schedule-upgrade","params":{"height":100,"image":""}}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty image, got %d", rec.Code)
	}
}

func TestScheduleUpgradeValidationMissingParams(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))

	body := `{"type":"schedule-upgrade","params":{}}`
	rec := serveHTTP(srv, http.MethodPost, "/task", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing params, got %d", rec.Code)
	}
}

func TestListTasksReturnsEmptyArray(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodGet, "/tasks", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var tasks []engine.TrackedTask
	if err := json.NewDecoder(rec.Body).Decode(&tasks); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(tasks) != 0 {
		t.Fatalf("expected empty array, got %d tasks", len(tasks))
	}
}

func TestListTasksAfterSubmit(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	eng.Submit(engine.Task{Type: engine.TaskConfigPatch})
	drainEngine(t, eng)

	rec := serveHTTP(srv, http.MethodGet, "/tasks", "")
	var tasks []engine.TrackedTask
	if err := json.NewDecoder(rec.Body).Decode(&tasks); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(tasks))
	}
	if tasks[0].Type != engine.TaskConfigPatch {
		t.Fatalf("expected config-patch, got %s", tasks[0].Type)
	}
}

func TestGetTaskByID(t *testing.T) {
	eng := newTestEngine(map[engine.TaskType]engine.TaskHandler{
		engine.TaskConfigPatch: func(_ context.Context, _ map[string]any) error { return nil },
	})
	srv := NewServer(":0", eng)

	id, _ := eng.Submit(engine.Task{Type: engine.TaskConfigPatch})
	drainEngine(t, eng)

	rec := serveHTTP(srv, http.MethodGet, "/tasks/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var task engine.TrackedTask
	if err := json.NewDecoder(rec.Body).Decode(&task); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if task.ID != id {
		t.Fatalf("expected ID %s, got %s", id, task.ID)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodGet, "/tasks/nonexistent", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestCreateScheduleCron(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	body := `{"taskType":"update-peers","cron":"*/5 * * * *"}`
	rec := serveHTTP(srv, http.MethodPost, "/schedules", body)

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

func TestCreateScheduleRunAt(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	body := `{"taskType":"snapshot-upload","runAt":"2030-01-01T00:00:00Z"}`
	rec := serveHTTP(srv, http.MethodPost, "/schedules", body)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestCreateScheduleValidationMissingTaskType(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	body := `{"cron":"* * * * *"}`
	rec := serveHTTP(srv, http.MethodPost, "/schedules", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateScheduleValidationBothCronAndRunAt(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	body := `{"taskType":"update-peers","cron":"* * * * *","runAt":"2030-01-01T00:00:00Z"}`
	rec := serveHTTP(srv, http.MethodPost, "/schedules", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestCreateScheduleValidationNeitherCronNorRunAt(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	body := `{"taskType":"update-peers"}`
	rec := serveHTTP(srv, http.MethodPost, "/schedules", body)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestListSchedules(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	eng.AddSchedule(engine.TaskUpdatePeers, nil, "*/5 * * * *", nil)

	rec := serveHTTP(srv, http.MethodGet, "/schedules", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var schedules []engine.Schedule
	if err := json.NewDecoder(rec.Body).Decode(&schedules); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
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

func TestGetSchedule(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	sched, _ := eng.AddSchedule(engine.TaskUpdatePeers, nil, "*/5 * * * *", nil)

	rec := serveHTTP(srv, http.MethodGet, "/schedules/"+sched.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var got engine.Schedule
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}
	if got.ID != sched.ID {
		t.Fatalf("expected ID %s, got %s", sched.ID, got.ID)
	}
}

func TestGetScheduleNotFound(t *testing.T) {
	srv := NewServer(":0", newTestEngine(nil))
	rec := serveHTTP(srv, http.MethodGet, "/schedules/nonexistent", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestDeleteSchedule(t *testing.T) {
	eng := newTestEngine(nil)
	srv := NewServer(":0", eng)

	sched, _ := eng.AddSchedule(engine.TaskUpdatePeers, nil, "*/5 * * * *", nil)

	rec := serveHTTP(srv, http.MethodDelete, "/schedules/"+sched.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rec.Code)
	}

	// Verify it's gone.
	rec = serveHTTP(srv, http.MethodGet, "/schedules/"+sched.ID, "")
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
