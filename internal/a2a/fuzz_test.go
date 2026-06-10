package a2a

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzJSONRPCDispatcher feeds the JSON-RPC POST handler with a wide range
// of malformed inputs. Crash-free is the bar; we don't assert any
// behaviour beyond "the handler must always respond with a valid HTTP
// status, never panic".
//
// Run locally:
//
//	go test ./internal/a2a -run=^$ -fuzz=FuzzJSONRPCDispatcher -fuzztime=10s
func FuzzJSONRPCDispatcher(f *testing.F) {
	// Seed corpus — known-shape requests that exercise different branches.
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"messageId":"m","role":"user","parts":[{"kind":"text","text":"hi"}]}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tasks/get","params":{"id":"x"}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tasks/cancel","params":{"id":"x"}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tasks/list"}`,
		`{"jsonrpc":"2.0","id":5,"method":"a2a.SendMessage","params":{"message":{"messageId":"m","role":"user","parts":[{"text":"hi"}]}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"a2a.UnknownMethod"}`,
		`{"jsonrpc":"2.0","id":7,"method":"message/send","params":{"message":{"parts":[{"kind":"file","file":{"bytes":"aGk="}}]}}}`,
		`{}`,
		`null`,
		`[]`,
		`"string"`,
		`123`,
		``,
	} {
		f.Add(seed)
	}

	srv := httptest.NewServer((&Server{
		Card: AgentCard{
			ProtocolVersion: ProtocolVersion,
			Name:            "fuzz-target",
			URL:             "http://fuzz",
			Version:         "0.0.0",
		},
		Handler: &fuzzHandler{},
		Log:     nopLogger(),
	}).Routes())
	defer srv.Close()

	f.Fuzz(func(t *testing.T, body string) {
		req, err := http.NewRequestWithContext(context.Background(),
			http.MethodPost, srv.URL+"/", strings.NewReader(body))
		if err != nil {
			return // request construction shouldn't fail
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			// Network / context errors are fine — the handler is the
			// thing under test, not the transport.
			return
		}
		_ = resp.Body.Close()
		// Crash-free is the only post-condition. Status codes will vary
		// — JSON-RPC spec returns 200 even for application errors, with
		// the error in the body.
		if resp.StatusCode == 0 || resp.StatusCode >= 600 {
			t.Errorf("invalid status code %d for body %q", resp.StatusCode, body)
		}
	})
}

// fuzzHandler returns benign answers for every Handler method so the
// fuzzer doesn't get stuck on synthetic ErrXxx errors. We're testing the
// dispatcher / parser, not the handler.
type fuzzHandler struct{}

func (fuzzHandler) SendMessage(_ context.Context, _ MessageSendParams) (*Task, *Message, error) {
	return &Task{ID: "fuzz", Status: TaskStatus{State: TaskStateSubmitted}}, nil, nil
}
func (fuzzHandler) GetTask(_ context.Context, _ TaskIDParams) (*Task, error) {
	return &Task{ID: "fuzz", Status: TaskStatus{State: TaskStateCompleted}}, nil
}
func (fuzzHandler) CancelTask(_ context.Context, _ TaskIDParams) (*Task, error) {
	return &Task{ID: "fuzz", Status: TaskStatus{State: TaskStateCanceled}}, nil
}
func (fuzzHandler) ListTasks(_ context.Context) ([]Task, error) { return nil, nil }
func (fuzzHandler) Subscribe(_ context.Context, _ string, _ chan<- StreamResponse) error {
	return nil
}
func (fuzzHandler) StreamSend(_ context.Context, _ MessageSendParams, _ chan<- StreamResponse) error {
	return nil
}
