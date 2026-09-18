package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cyber-foreman/internal/agent"
	"cyber-foreman/internal/app"
	"cyber-foreman/internal/domain"
	"cyber-foreman/internal/event"
)

type Server struct {
	service *app.Service
	bus     *event.Bus
	mux     *http.ServeMux
}

func NewServer(service *app.Service, bus *event.Bus) *Server {
	s := &Server{service: service, bus: bus, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", s.health)
	s.mux.HandleFunc("GET /api/v1/adapters", s.listAdapters)
	s.mux.HandleFunc("GET /api/v1/tasks", s.listTasks)
	s.mux.HandleFunc("POST /api/v1/tasks", s.createTask)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}", s.getTask)
	s.mux.HandleFunc("DELETE /api/v1/tasks/{id}", s.stopTask)
	s.mux.HandleFunc("POST /api/v1/tasks/{id}/actions", s.taskAction)
	s.mux.HandleFunc("GET /api/v1/tasks/{id}/events", s.taskEvents)
	s.mux.HandleFunc("GET /api/v1/events", s.events)
	return s
}

func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "api_version": "v1"})
}

func (s *Server) listAdapters(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"adapters": s.service.ListAdapters()})
}

func (s *Server) listTasks(w http.ResponseWriter, _ *http.Request) {
	tasks := s.service.ListTasks()
	result := make([]taskResponse, len(tasks))
	for i, task := range tasks {
		result[i] = newTaskResponse(task)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": result})
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var request createTaskRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	startRequest, err := request.appRequest()
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	task, err := s.service.SubmitTask(startRequest)
	if err != nil {
		status, code := classifyError(err)
		writeAPIError(w, status, code, err)
		return
	}
	w.Header().Set("Location", "/api/v1/tasks/"+task.ID)
	writeJSON(w, http.StatusAccepted, newTaskResponse(task))
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.service.GetTask(r.PathValue("id"))
	if err != nil {
		status, code := classifyError(err)
		writeAPIError(w, status, code, err)
		return
	}
	writeJSON(w, http.StatusOK, newTaskResponse(task))
}

func (s *Server) stopTask(w http.ResponseWriter, r *http.Request) {
	if err := s.service.StopTask(r.PathValue("id")); err != nil {
		status, code := classifyError(err)
		writeAPIError(w, status, code, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) taskAction(w http.ResponseWriter, r *http.Request) {
	var request taskActionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_request", err)
		return
	}
	var err error
	switch request.Type {
	case "interrupt":
		actionCtx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		err = s.service.InterruptTask(actionCtx, r.PathValue("id"), strings.TrimSpace(request.Message))
	case "cancel":
		err = s.service.StopTask(r.PathValue("id"))
	default:
		writeAPIError(w, http.StatusBadRequest, "invalid_action", errors.New("action type must be interrupt or cancel"))
		return
	}
	if err != nil {
		status, code := classifyError(err)
		writeAPIError(w, status, code, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "type": request.Type})
}

func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	if _, err := s.service.GetTask(r.PathValue("id")); err != nil {
		status, code := classifyError(err)
		writeAPIError(w, status, code, err)
		return
	}
	s.streamEvents(w, r, r.PathValue("id"))
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	s.streamEvents(w, r, strings.TrimSpace(r.URL.Query().Get("task_id")))
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request, taskID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "streaming_unsupported", errors.New("streaming is unsupported"))
		return
	}
	after, err := eventCursor(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	events, gap := s.bus.SubscribeSince(r.Context(), after, 256)
	if gap {
		writeSSE(w, "", "stream.gap", map[string]string{
			"code": "event_history_gap", "message": "requested events are no longer retained; reload the task snapshot",
		})
		flusher.Flush()
	}
	keepAlive := time.NewTicker(15 * time.Second)
	defer keepAlive.Stop()
	for {
		select {
		case evt, open := <-events:
			if !open {
				return
			}
			if taskID != "" && evt.TaskID != taskID {
				continue
			}
			writeSSE(w, evt.ID, string(evt.Type), eventResponse(evt))
			flusher.Flush()
		case <-keepAlive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func eventResponse(evt domain.Event) map[string]any {
	data := boundedEventData(evt.Data)
	if evt.Type == domain.EventTaskCreated {
		if task, ok := evt.Data.(domain.Task); ok {
			data = newTaskResponse(task)
		}
	}
	return map[string]any{
		"id": evt.ID, "version": evt.Version, "sequence": evt.Sequence,
		"task_id": evt.TaskID, "session_id": evt.SessionID, "type": evt.Type,
		"occurred_at": evt.Timestamp, "data": data,
	}
}

const maxEventDataBytes = 64 * 1024

var secretAssignment = regexp.MustCompile(`(?i)((?:GOOGLE_GENERATIVE_AI_API_KEY|GEMINI_API_KEY|OPENAI_API_KEY|ANTHROPIC_API_KEY|API_KEY|TOKEN|SECRET|PASSWORD)\s*=\s*)[^\s"']+`)

func boundedEventData(value any) any {
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]string{"error": "event payload could not be encoded"}
	}
	var normalized any
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	if err := decoder.Decode(&normalized); err != nil {
		return map[string]string{"error": "event payload could not be normalized"}
	}
	normalized = redactEventValue(normalized)
	payload, err = json.Marshal(normalized)
	if err != nil {
		return map[string]string{"error": "event payload could not be encoded"}
	}
	if len(payload) > maxEventDataBytes {
		return map[string]any{"truncated": true, "original_bytes": len(payload)}
	}
	return json.RawMessage(payload)
}

func redactEventValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if sensitiveEventKey(key) {
				typed[key] = "[REDACTED]"
				continue
			}
			typed[key] = redactEventValue(item)
		}
		return typed
	case []any:
		for i, item := range typed {
			typed[i] = redactEventValue(item)
		}
		return typed
	case string:
		return secretAssignment.ReplaceAllString(typed, `${1}[REDACTED]`)
	default:
		return value
	}
}

func sensitiveEventKey(key string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, marker := range []string{"api_key", "token", "secret", "password", "authorization", "credential"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func eventCursor(r *http.Request) (uint64, error) {
	value := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if value == "" {
		value = strings.TrimSpace(r.URL.Query().Get("after"))
	}
	value = strings.TrimPrefix(value, "evt-")
	if value == "" {
		return 0, nil
	}
	sequence, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, errors.New("Last-Event-ID or after must be an event ID such as evt-42")
	}
	return sequence, nil
}

func writeSSE(w io.Writer, id, eventType string, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	if id != "" {
		_, _ = fmt.Fprintf(w, "id: %s\n", id)
	}
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON object")
		}
		return err
	}
	return nil
}

func classifyError(err error) (int, string) {
	switch {
	case errors.Is(err, app.ErrTaskNotFound):
		return http.StatusNotFound, "task_not_found"
	case errors.Is(err, agent.ErrAdapterNotFound):
		return http.StatusBadRequest, "adapter_not_found"
	case errors.Is(err, app.ErrTaskNotRunning), errors.Is(err, app.ErrActionUnavailable), errors.Is(err, app.ErrInvalidTransition):
		return http.StatusConflict, "action_conflict"
	default:
		return http.StatusBadRequest, "invalid_request"
	}
}

func writeAPIError(w http.ResponseWriter, status int, code string, err error) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": err.Error()}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
