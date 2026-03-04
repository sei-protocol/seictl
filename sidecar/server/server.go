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

// TaskRequest is the JSON body for POST /v0/tasks. If Schedule is set the
// task becomes a recurring cron-triggered task; otherwise it runs immediately.
type TaskRequest struct {
	Type     string           `json:"type"`
	Params   map[string]any   `json:"params,omitempty"`
	Schedule *engine.Schedule `json:"schedule,omitempty"`
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
	s.mux.HandleFunc("GET /v0/healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /v0/status", s.handleStatus)
	s.mux.HandleFunc("POST /v0/tasks", s.handlePostTask)
	s.mux.HandleFunc("GET /v0/tasks", s.handleListTasks)
	s.mux.HandleFunc("GET /v0/tasks/{id}", s.handleGetTask)
	s.mux.HandleFunc("DELETE /v0/tasks/{id}", s.handleDeleteTask)
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

	task := engine.Task{Type: engine.TaskType(req.Type), Params: req.Params}

	if req.Schedule != nil {
		if req.Schedule.Cron != "" {
			if err := engine.ValidateCron(req.Schedule.Cron); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		id, err := s.engine.SubmitScheduled(task, *req.Schedule)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"id": id})
		return
	}

	id, err := s.engine.Submit(task)
	if err != nil {
		if errors.Is(err, engine.ErrBusy) {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
}

func (s *Server) handleListTasks(w http.ResponseWriter, _ *http.Request) {
	results := s.engine.RecentResults()
	if results == nil {
		results = []engine.TaskResult{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing task ID")
		return
	}
	result := s.engine.GetResult(id)
	if result == nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing task ID")
		return
	}
	if !s.engine.RemoveResult(id) {
		writeError(w, http.StatusNotFound, "task not found")
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
