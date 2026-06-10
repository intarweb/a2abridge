package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"

	"github.com/vbcherepanov/a2abridge/internal/a2a"
)

const testSecret = "ghp_0123456789012345678901234567890123456789"

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// callTool drives a registered MCP tool through the real JSON-RPC
// dispatcher so the test exercises the exact code path the IDE uses.
func callTool(t *testing.T, s *server.MCPServer, name string, args map[string]any) string {
	t.Helper()
	params, err := json.Marshal(map[string]any{"name": name, "arguments": args})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":%s}`, params))
	resp := s.HandleMessage(context.Background(), raw)
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newTestMCP(t *testing.T, store *Store) *server.MCPServer {
	t.Helper()
	mcpSrv := server.NewMCPServer("test", "0.0.0")
	RegisterTools(mcpSrv, &MCPDeps{
		Store:        store,
		OwnCard:      a2a.AgentCard{Name: "self", URL: "http://self.invalid"},
		DirectoryURL: "http://127.0.0.1:1", // unused by the tools under test
		Log:          discardLog(),
	})
	return mcpSrv
}

// TestCompleteTaskToolScreensSecrets — a2a_complete_task is an outbound
// path (the reply leaves via SSE / webhooks), so it must run through the
// same secret screen as a2a_send_message.
func TestCompleteTaskToolScreensSecrets(t *testing.T) {
	store := NewStore()
	defer store.Close()
	task, _, err := store.SendMessage(context.Background(), a2a.MessageSendParams{
		Message: a2a.Message{Role: a2a.RoleUser, Parts: []a2a.Part{{Text: "what's the token?"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	mcpSrv := newTestMCP(t, store)
	resp := callTool(t, mcpSrv, "a2a_complete_task", map[string]any{
		"task_id": task.ID,
		"text":    "use " + testSecret + " for auth",
	})
	if strings.Contains(resp, testSecret) {
		t.Error("tool result leaked the raw secret")
	}

	got, err := store.GetTask(context.Background(), a2a.TaskIDParams{ID: task.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Artifacts) == 0 || len(got.Artifacts[0].Parts) == 0 {
		t.Fatal("no reply artifact recorded")
	}
	reply := got.Artifacts[0].Parts[0].Text
	if strings.Contains(reply, testSecret) {
		t.Error("raw secret reached the task artifact")
	}
	if !strings.Contains(reply, "[REDACTED:github-token]") {
		t.Errorf("reply not redacted: %q", reply)
	}
}

// TestSendStreamingToolScreensSecrets — a2a_send_streaming must not be a
// side door for unredacted text: the peer's inbox must only ever see the
// redacted form.
func TestSendStreamingToolScreensSecrets(t *testing.T) {
	peerStore := NewStore()
	defer peerStore.Close()
	// Auto-complete incoming tasks so the streaming call terminates fast.
	peerStore.OnIncoming = func(m a2a.Message) { _ = peerStore.CompleteTask(m.TaskID, "ack") }
	peer := httptest.NewServer((&a2a.Server{
		Card:    a2a.AgentCard{Name: "peer"},
		Handler: peerStore,
		Log:     discardLog(),
	}).Routes())
	defer peer.Close()

	localStore := NewStore()
	defer localStore.Close()
	mcpSrv := newTestMCP(t, localStore)

	resp := callTool(t, mcpSrv, "a2a_send_streaming", map[string]any{
		"peer_url":  peer.URL,
		"text":      "deploy key: " + testSecret,
		"timeout_s": 10,
	})
	if strings.Contains(resp, testSecret) {
		t.Error("streaming tool result leaked the raw secret")
	}

	// The peer must have received only the redacted text.
	deadline := time.Now().Add(2 * time.Second)
	var inboundText string
	for time.Now().Before(deadline) {
		tasks, _ := peerStore.ListTasks(context.Background())
		if len(tasks) == 1 && len(tasks[0].History) > 0 {
			inboundText = tasks[0].History[0].Parts[0].Text
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if inboundText == "" {
		t.Fatal("peer never received the streamed message")
	}
	if strings.Contains(inboundText, testSecret) {
		t.Error("raw secret crossed the wire via send_streaming")
	}
	if !strings.Contains(inboundText, "[REDACTED:github-token]") {
		t.Errorf("peer received unredacted text: %q", inboundText)
	}
}
