package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/sei-protocol/seictl/sidecar/engine"
)

// Server is the HTTP API for the sidecar.
type Server struct {
	addr   string
	engine *engine.Engine
	mux    *http.ServeMux
}

// TaskRequest is the JSON body for POST /task. If Schedule is set, a cron
// schedule is created instead of running the task immediately.
type TaskRequest struct {
	Type     string         `json:"type"`
	Params   map[string]any `json:"params,omitempty"`
	Schedule string         `json:"schedule,omitempty"`
}

// ErrorResponse is a standard JSON error envelope.
type ErrorResponse struct {
	Error string `json:"error"`
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
	s.mux.HandleFunc("GET /schedules", s.handleListSchedules)
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
	writeJSON(w, http.StatusOK, s.engine.Status())
}

func (s *Server) handlePostTask(w http.ResponseWriter, r *http.Request) {
	var req TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "type is required")
		return
	}

	if req.Schedule != "" {
		if err := engine.ValidateCron(req.Schedule); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		sched := s.engine.AddSchedule(engine.TaskType(req.Type), req.Params, req.Schedule)
		writeJSON(w, http.StatusCreated, sched)
		return
	}

	if err := s.engine.Submit(engine.Task{
		Type:   engine.TaskType(req.Type),
		Params: req.Params,
	}); err != nil {
		if errors.Is(err, engine.ErrBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "submitted"})
}

func (s *Server) handleListSchedules(w http.ResponseWriter, _ *http.Request) {
	schedules := s.engine.ListSchedules()
	if schedules == nil {
		schedules = []engine.Schedule{}
	}
	writeJSON(w, http.StatusOK, schedules)
}

func (s *Server) handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	scheduleID := r.PathValue("scheduleId")
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
		Addr:              s.addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
