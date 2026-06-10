package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Non-spec convenience REST API. The A2A 1.0 spec binding served by this
// package is JSON-RPC at POST /; these routes are a custom dialect mounted
// alongside it so clients that don't speak JSON-RPC (cURL scripts, browser
// fetch(), webhook callers) can consume the same handler. The bodies are
// direct JSON of the underlying types — no RPC envelope.
//
// Path table:
//
//	POST   /v1/tasks                   → SendMessage
//	POST   /v1/tasks/stream            → SendStreamingMessage (SSE)
//	GET    /v1/tasks                   → ListTasks
//	GET    /v1/tasks/{id}              → GetTask
//	POST   /v1/tasks/{id}/cancel       → CancelTask
//	GET    /v1/tasks/{id}/stream       → SubscribeToTask  (SSE)
//	GET    /v1/agent                   → agent card
//	POST   /v1/tasks/{id}/push         → CreatePushConfig
//	GET    /v1/tasks/{id}/push         → ListPushConfigs
//	DELETE /v1/tasks/{id}/push         → DeletePushConfig (all)
//	DELETE /v1/tasks/{id}/push/{cfg}   → DeletePushConfig (one)

func (s *Server) restRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/tasks", s.restSendMessage)
	mux.HandleFunc("POST /v1/tasks/stream", s.restSendStreaming)
	mux.HandleFunc("GET /v1/tasks", s.restListTasks)
	mux.HandleFunc("GET /v1/tasks/{id}", s.restGetTask)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.restCancelTask)
	mux.HandleFunc("GET /v1/tasks/{id}/stream", s.restSubscribeToTask)
	mux.HandleFunc("GET /v1/agent", s.restAgentCard)
	mux.HandleFunc("POST /v1/tasks/{id}/push", s.restCreatePush)
	mux.HandleFunc("GET /v1/tasks/{id}/push", s.restListPush)
	mux.HandleFunc("DELETE /v1/tasks/{id}/push", s.restDeletePushAll)
	mux.HandleFunc("DELETE /v1/tasks/{id}/push/{cfg}", s.restDeletePushOne)
}

// --- helpers ------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeRESTErr maps the package sentinel errors to HTTP status codes:
// ErrTaskNotFound → 404, ErrTaskNotCancelable → 409, ErrInvalidParams →
// 400, ErrPushNotSupported / ErrUnsupportedOperation → 501, else → 500.
func writeRESTErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrTaskNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrTaskNotCancelable):
		status = http.StatusConflict
	case errors.Is(err, ErrInvalidParams):
		status = http.StatusBadRequest
	case errors.Is(err, ErrPushNotSupported), errors.Is(err, ErrUnsupportedOperation):
		status = http.StatusNotImplemented
	}
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

// decodeBody enforces the body size cap and decodes JSON into dst,
// wrapping decode failures in ErrInvalidParams so writeRESTErr maps them
// to 400.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidParams, err)
	}
	return nil
}

func (s *Server) restAgentCard(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Card)
}

func (s *Server) restSendMessage(w http.ResponseWriter, r *http.Request) {
	var p MessageSendParams
	if err := decodeBody(w, r, &p); err != nil {
		writeRESTErr(w, err)
		return
	}
	task, msg, err := s.Handler.SendMessage(r.Context(), p)
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	switch {
	case task != nil:
		writeJSON(w, http.StatusOK, task)
	case msg != nil:
		writeJSON(w, http.StatusOK, msg)
	default:
		writeRESTErr(w, errors.New("handler returned no result"))
	}
}

func (s *Server) restListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := s.Handler.ListTasks(r.Context())
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *Server) restGetTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.Handler.GetTask(r.Context(), TaskIDParams{ID: id})
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) restCancelTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	t, err := s.Handler.CancelTask(r.Context(), TaskIDParams{ID: id})
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) restSendStreaming(w http.ResponseWriter, r *http.Request) {
	var p MessageSendParams
	if err := decodeBody(w, r, &p); err != nil {
		writeRESTErr(w, err)
		return
	}
	s.streamRESTRPC(w, r, func(ctx context.Context, ch chan<- StreamResponse) error {
		return s.Handler.StreamSend(ctx, p, ch)
	})
}

func (s *Server) restSubscribeToTask(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.streamRESTRPC(w, r, func(ctx context.Context, ch chan<- StreamResponse) error {
		return s.Handler.Subscribe(ctx, id, ch)
	})
}

// streamRESTRPC is the REST flavour of streamRPC — same SSE machinery
// (streamSSE), just without the JSON-RPC envelope around each event.
func (s *Server) streamRESTRPC(
	w http.ResponseWriter, r *http.Request,
	run func(ctx context.Context, ch chan<- StreamResponse) error,
) {
	s.streamSSE(w, r, run,
		func(ev StreamResponse) []byte {
			b, _ := json.Marshal(ev)
			return b
		},
		func(err error) []byte {
			b, _ := json.Marshal(map[string]*JSONRPCError{
				"error": {Code: taskErrCode(err), Message: err.Error()},
			})
			return b
		},
		writeRESTErr,
	)
}

func (s *Server) restCreatePush(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.Handler.(PushHandler)
	if !ok {
		writeRESTErr(w, ErrPushNotSupported)
		return
	}
	var cfg PushNotificationConfig
	if err := decodeBody(w, r, &cfg); err != nil {
		writeRESTErr(w, err)
		return
	}
	out, err := ph.CreatePushConfig(r.Context(), TaskPushNotificationConfig{
		TaskID: r.PathValue("id"), Config: cfg,
	})
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) restListPush(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.Handler.(PushHandler)
	if !ok {
		writeRESTErr(w, ErrPushNotSupported)
		return
	}
	out, err := ph.ListPushConfigs(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRESTErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) restDeletePushAll(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.Handler.(PushHandler)
	if !ok {
		writeRESTErr(w, ErrPushNotSupported)
		return
	}
	if err := ph.DeletePushConfig(r.Context(), PushNotificationConfigParams{TaskID: r.PathValue("id")}); err != nil {
		writeRESTErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) restDeletePushOne(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.Handler.(PushHandler)
	if !ok {
		writeRESTErr(w, ErrPushNotSupported)
		return
	}
	if err := ph.DeletePushConfig(r.Context(), PushNotificationConfigParams{
		TaskID:       r.PathValue("id"),
		PushConfigID: r.PathValue("cfg"),
	}); err != nil {
		writeRESTErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
