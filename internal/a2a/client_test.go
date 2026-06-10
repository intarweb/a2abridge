package a2a

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientSendMessageKindDispatch(t *testing.T) {
	cases := []struct {
		name        string
		result      string
		wantTask    bool
		wantMessage bool
	}{
		{"spec task", `{"kind":"task","id":"t1","status":{"state":"working","timestamp":"2026-01-01T00:00:00Z"}}`, true, false},
		{"spec message", `{"kind":"message","messageId":"m1","role":"agent","parts":[{"kind":"text","text":"hi"}]}`, false, true},
		{"legacy task without kind", `{"id":"t1","status":{"state":"working","timestamp":"2026-01-01T00:00:00Z"}}`, true, false},
		{"legacy message without kind", `{"messageId":"m1","role":"agent","parts":[{"text":"hi"}]}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req JSONRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
				}
				if req.Method != MethodSendMessage {
					t.Errorf("method = %q, want %q", req.Method, MethodSendMessage)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(req.ID) + `,"result":` + tc.result + `}`))
			}))
			defer ts.Close()

			res, err := NewClient(ts.URL).SendMessage(context.Background(), MessageSendParams{
				Message: Message{MessageID: "m0", Role: RoleUser, Parts: []Part{{Text: "go"}}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if (res.Task != nil) != tc.wantTask {
				t.Errorf("Task presence = %v, want %v", res.Task != nil, tc.wantTask)
			}
			if (res.Message != nil) != tc.wantMessage {
				t.Errorf("Message presence = %v, want %v", res.Message != nil, tc.wantMessage)
			}
		})
	}
}

// TestClientStreamSurfacesJSONErrorBody covers servers that reject a
// stream request with HTTP 200 + a plain JSON-RPC error body instead of
// an SSE stream.
func TestClientStreamSurfacesJSONErrorBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"task not found"}}`))
	}))
	defer ts.Close()

	out := make(chan StreamResponse, 1)
	err := NewClient(ts.URL).SubscribeToTask(context.Background(), "missing", out)
	if err == nil {
		t.Fatal("expected error from JSON error body, got nil")
	}
	if want := "-32001"; !strings.Contains(err.Error(), want) {
		t.Errorf("err = %v, want it to mention %s", err, want)
	}
}

// TestClientSSEMultiLineData verifies that consecutive data: lines are
// concatenated per the SSE spec before the payload is parsed.
func TestClientSSEMultiLineData(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// One event split across two data lines + a comment line.
		_, _ = w.Write([]byte(": keepalive\n" +
			"data: {\"jsonrpc\":\"2.0\",\"result\":\n" +
			"data: {\"statusUpdate\":{\"taskId\":\"t9\",\"status\":{\"state\":\"completed\",\"timestamp\":\"2026-01-01T00:00:00Z\"},\"final\":true}}}\n" +
			"\n"))
	}))
	defer ts.Close()

	out := make(chan StreamResponse, 4)
	err := NewClient(ts.URL).SubscribeToTask(context.Background(), "t9", out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-out:
		if ev.StatusUpdate == nil || ev.StatusUpdate.TaskID != "t9" || !ev.StatusUpdate.Final {
			t.Errorf("unexpected event: %+v", ev)
		}
	default:
		t.Fatal("no event received from multi-line data payload")
	}
}

func TestClientStructLiteralWithoutHTTPDoesNotPanic(t *testing.T) {
	ts := newTestServer(t, &fakeHandler{getTask: &Task{ID: "z1", Status: TaskStatus{State: TaskStateCompleted}}})
	defer ts.Close()

	c := &Client{BaseURL: ts.URL} // no HTTP client wired up
	task, err := c.GetTask(context.Background(), "z1")
	if err != nil {
		t.Fatal(err)
	}
	if task.ID != "z1" {
		t.Errorf("task.ID = %q", task.ID)
	}
}

func TestFetchAgentCardLegacyFallback(t *testing.T) {
	// Peer that only publishes the card at the pre-1.0 path.
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+WellKnownPathLegacy, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(AgentCard{ProtocolVersion: ProtocolVersion, Name: "legacy-agent", URL: "http://x", Version: "0.0.1"})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	card, err := NewClient(ts.URL).FetchAgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "legacy-agent" {
		t.Errorf("card.Name = %q, want legacy-agent", card.Name)
	}
}

func TestFetchAgentCardNewPath(t *testing.T) {
	ts := newTestServer(t, &fakeHandler{})
	defer ts.Close()

	card, err := NewClient(ts.URL).FetchAgentCard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if card.Name != "test-agent" {
		t.Errorf("card.Name = %q, want test-agent", card.Name)
	}
}

// TestClientStreamEndToEnd drives Client.SendStreamingMessage against the
// real Server SSE implementation, including terminal-status shutdown.
func TestClientStreamEndToEnd(t *testing.T) {
	h := &fakeHandler{
		streamSend: func(_ context.Context, _ MessageSendParams, ch chan<- StreamResponse) error {
			ch <- StreamResponse{Task: &Task{ID: "e2e", Status: TaskStatus{State: TaskStateWorking, Timestamp: time.Now()}}}
			ch <- StreamResponse{StatusUpdate: &TaskStatusUpdateEvent{
				TaskID: "e2e",
				Status: TaskStatus{State: TaskStateCompleted, Timestamp: time.Now()},
				Final:  true,
			}}
			return nil
		},
	}
	ts := newTestServer(t, h)
	defer ts.Close()

	out := make(chan StreamResponse, 8)
	err := NewClient(ts.URL).SendStreamingMessage(context.Background(), MessageSendParams{
		Message: Message{MessageID: "m1", Role: RoleUser, Parts: []Part{{Text: "go"}}},
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	close(out)
	var sawTask, sawFinal bool
	for ev := range out {
		if ev.Task != nil && ev.Task.ID == "e2e" {
			sawTask = true
		}
		if ev.StatusUpdate != nil && ev.StatusUpdate.Final {
			sawFinal = true
		}
	}
	if !sawTask || !sawFinal {
		t.Errorf("sawTask=%v sawFinal=%v", sawTask, sawFinal)
	}
}
