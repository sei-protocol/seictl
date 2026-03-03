package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/sei-protocol/platform/sei-sidecar/engine"
)

// Server is the HTTP API for sei-sidecar.
type Server struct {
	addr   string
	engine *engine.Engine
	mux    *http.ServeMux
}

// StatusResponse is the JSON shape returned by GET /status.
type StatusResponse struct {
	Phase           string `json:"phase"`
	CurrentTask     string `json:"currentTask,omitempty"`
	LastTask        string `json:"lastTask,omitempty"`
	LastTaskResult  string `json:"lastTaskResult,omitempty"`
	LastTaskError   string `json:"lastTaskError,omitempty"`
	ActiveTasks     int    `json:"activeTasks,omitempty"`
	BlockHeight     int64  `json:"blockHeight,omitempty"`
	CatchingUp      bool   `json:"catchingUp,omitempty"`
	PeerCount       int    `json:"peerCount,omitempty"`
	NodeID          string `json:"nodeID,omitempty"`
	UpgradeHeight   int64  `json:"upgradeHeight,omitempty"`
	UpgradeImage    string `json:"upgradeImage,omitempty"`
	PendingUpgrades int    `json:"pendingUpgrades,omitempty"`
}

// TaskRequest is the JSON body for POST /task.
type TaskRequest struct {
	Type   string         `json:"type"`
	Params map[string]any `json:"params,omitempty"`
}

// ErrorResponse is a standard JSON error envelope.
type ErrorResponse struct {
	Error string `json:"error"`
}

// CreateScheduleRequest is the JSON body for POST /schedules.
type CreateScheduleRequest struct {
	TaskType string         `json:"taskType"`
	Params   map[string]any `json:"params,omitempty"`
	Cron     string         `json:"cron,omitempty"`
	RunAt    *time.Time     `json:"runAt,omitempty"`
}

// NewServer creates a Server wired to the given engine.
func NewServer(addr string, eng *engine.Engine) *Server {
	s := &Server{
		addr:   addr,
		engine: eng,
		mux:    http.NewServeMux(),
	}
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /status", s.handleStatus)
	s.mux.HandleFunc("POST /task", s.handlePostTask)
	s.mux.HandleFunc("GET /tasks", s.handleListTasks)
	s.mux.HandleFunc("GET /tasks/{taskId}", s.handleGetTask)
	s.mux.HandleFunc("POST /schedules", s.handleCreateSchedule)
	s.mux.HandleFunc("GET /schedules", s.handleListSchedules)
	s.mux.HandleFunc("GET /schedules/{scheduleId}", s.handleGetSchedule)
	s.mux.HandleFunc("DELETE /schedules/{scheduleId}", s.handleDeleteSchedule)
	return s
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck // best-effort response
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	if !s.engine.Healthz() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	es := s.engine.Status()

	resp := StatusResponse{
		Phase:           string(es.Phase),
		ActiveTasks:     es.ActiveTasks,
		UpgradeHeight:   es.UpgradeHeight,
		UpgradeImage:    es.UpgradeImage,
		PendingUpgrades: es.PendingUpgrades,
	}
	if es.CurrentTask != nil {
		resp.CurrentTask = string(es.CurrentTask.Type)
	}
	if es.LastResult != nil {
		resp.LastTask = string(es.LastResult.Task)
		if es.LastResult.Success {
			resp.LastTaskResult = "success"
		} else {
			resp.LastTaskResult = "error"
			resp.LastTaskError = es.LastResult.Error
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePostTask(w http.ResponseWriter, r *http.Request) {
	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// schedule-upgrade is stored as a pending item, not executed as a normal task.
	if engine.TaskType(req.Type) == engine.TaskScheduleUpgrade {
		height, _ := req.Params["height"].(float64) // JSON numbers decode as float64
		image, _ := req.Params["image"].(string)
		if height <= 0 || image == "" {
			writeError(w, http.StatusBadRequest, "schedule-upgrade requires positive height and non-empty image")
			return
		}
		s.engine.ScheduleUpgrade(engine.UpgradeTarget{
			Height: int64(height),
			Image:  image,
		})
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scheduled"})
		return
	}

	id, err := s.engine.Submit(engine.Task{
		Type:   engine.TaskType(req.Type),
		Params: req.Params,
	})
	if err != nil {
		msg := err.Error()
		if errors.Is(err, engine.ErrResourceConflict) {
			msg = "resource conflict: " + msg
		}
		writeError(w, http.StatusConflict, msg)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"id":     id,
		"status": "running",
	})
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	stateFilter := engine.TaskState(r.URL.Query().Get("state"))
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 {
			limit = n
		}
	}

	tasks := s.engine.ListTasks(stateFilter, limit)
	if tasks == nil {
		tasks = []engine.TrackedTask{}
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	taskID := strings.TrimPrefix(r.URL.Path, "/tasks/")
	if taskID == "" {
		writeError(w, http.StatusBadRequest, "missing task ID")
		return
	}

	task := s.engine.GetTask(taskID)
	if task == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleCreateSchedule(w http.ResponseWriter, r *http.Request) {
	var req CreateScheduleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.TaskType == "" {
		writeError(w, http.StatusBadRequest, "taskType is required")
		return
	}

	hasCron := req.Cron != ""
	hasRunAt := req.RunAt != nil
	if hasCron == hasRunAt {
		writeError(w, http.StatusBadRequest, "exactly one of cron or runAt must be provided")
		return
	}

	sched, err := s.engine.AddSchedule(engine.TaskType(req.TaskType), req.Params, req.Cron, req.RunAt)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, sched)
}

func (s *Server) handleListSchedules(w http.ResponseWriter, _ *http.Request) {
	schedules := s.engine.ListSchedules()
	if schedules == nil {
		schedules = []engine.Schedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

func (s *Server) handleGetSchedule(w http.ResponseWriter, r *http.Request) {
	scheduleID := strings.TrimPrefix(r.URL.Path, "/schedules/")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "missing schedule ID")
		return
	}

	sched := s.engine.GetSchedule(scheduleID)
	if sched == nil {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	writeJSON(w, http.StatusOK, sched)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	scheduleID := strings.TrimPrefix(r.URL.Path, "/schedules/")
	if scheduleID == "" {
		writeError(w, http.StatusBadRequest, "missing schedule ID")
		return
	}

	if !s.engine.RemoveSchedule(scheduleID) {
		writeError(w, http.StatusNotFound, "schedule not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListenAndServe starts the HTTP server and blocks until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background()) //nolint:errcheck // best-effort shutdown
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
