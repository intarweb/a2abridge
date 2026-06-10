package agent

// SSE fast-path for outgoing-reply delivery. See subscribeOutgoingReply
// at the bottom of this file.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/vbcherepanov/a2abridge/internal/a2a"
	"github.com/vbcherepanov/a2abridge/internal/metrics"
	"github.com/vbcherepanov/a2abridge/internal/security"
)

// directoryRequestTimeout bounds every directory HTTP call (register,
// heartbeat, /agents listing) and each peer agent-card fetch.
const directoryRequestTimeout = 5 * time.Second

// MCPDeps ties the local store, own card and directory URL so MCP tools can act.
type MCPDeps struct {
	Store        *Store
	OwnCard      a2a.AgentCard
	DirectoryURL string       // e.g. http://127.0.0.1:7777
	Log          *slog.Logger // optional; nil falls back to slog.Default()
}

func (d *MCPDeps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// screenOutbound applies the PII / secret screen to text that is about to
// leave this agent. Every outbound path (send_message, send_streaming,
// complete_task) MUST go through this single choke point so a new tool
// can't silently bypass the screen.
func screenOutbound(text string, log *slog.Logger) (string, []security.Match) {
	redacted, hits := security.Screen(text)
	if len(hits) > 0 {
		log.Warn("outbound text redacted", "summary", security.FormatMatches(hits), "count", len(hits))
	}
	return redacted, hits
}

// RegisterTools attaches a2a_* MCP tools that use the A2A protocol as transport.
func RegisterTools(s *server.MCPServer, d *MCPDeps) {
	s.AddTool(
		mcp.NewTool("a2a_whoami",
			mcp.WithDescription("Return this agent's own A2A Agent Card."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			b, _ := json.MarshalIndent(d.OwnCard, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_list_agents",
			mcp.WithDescription("Discover peer A2A agents via the directory. Returns each peer's Agent Card."),
		),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peers, err := listPeers(ctx, d.DirectoryURL, d.OwnCard.URL)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(peers, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_send_message",
			mcp.WithDescription("Call a2a.SendMessage on a peer agent (fire-and-forget or blocking per `blocking`). Returns the resulting Task or Message."),
			mcp.WithString("peer_url", mcp.Required(), mcp.Description("Base URL of the peer A2A agent (e.g. http://127.0.0.1:49152)")),
			mcp.WithString("text", mcp.Required(), mcp.Description("Text content of the message")),
			mcp.WithBoolean("blocking", mcp.Description("Ask the peer to block until terminal state (default false)")),
			mcp.WithString("context_id", mcp.Description("Existing contextId to continue a conversation")),
			mcp.WithString("task_id", mcp.Description("Existing taskId to continue a task")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peerURL, _ := req.RequireString("peer_url")
			text, _ := req.RequireString("text")
			blocking, _ := req.RequireBool("blocking")
			ctxID, _ := req.RequireString("context_id")
			taskID, _ := req.RequireString("task_id")

			// Screen for AWS keys, GitHub tokens, JWTs, PEM private keys
			// etc. The screener replaces matches with [REDACTED:<name>] so
			// the message still goes through with usable context — only
			// the secret is stripped. The MCP tool result mentions any
			// redaction so the model can warn the user.
			redacted, hits := screenOutbound(text, d.logger())
			text = redacted

			client := a2a.NewClient(peerURL)
			meta := map[string]any{"from": d.OwnCard.Name, "fromUrl": d.OwnCard.URL}
			if len(hits) > 0 {
				meta["redactions"] = security.FormatMatches(hits)
			}
			msg := a2a.Message{
				MessageID: uuid.NewString(),
				ContextID: ctxID,
				TaskID:    taskID,
				Role:      a2a.RoleUser,
				Parts:     []a2a.Part{{Text: text}},
				Metadata:  meta,
			}
			params := a2a.MessageSendParams{
				Message: msg,
				Configuration: &a2a.MessageSendConfiguration{
					Blocking:            blocking,
					AcceptedOutputModes: []string{"text/plain"},
				},
			}
			res, err := client.SendMessage(ctx, params)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			metrics.IncMessagesSent()
			// Регистрируем исходящую задачу для фонового опроса — чтобы
			// когда пир ответит, ответ автоматически попал в inbox и hook
			// подсунул его пользователю в следующий turn.
			if res != nil && res.Task != nil {
				peerName := ""
				if c, err := a2a.NewClient(peerURL).FetchAgentCard(ctx); err == nil {
					peerName = c.Name
				}
				d.Store.TrackOutgoing(res.Task.ID, peerURL, peerName, text)
				// Open an SSE subscription on the peer so the reply lands
				// the moment the peer reaches a terminal state — no need to
				// wait for the 5-second polling tick. The poll loop is still
				// running as a safety net in case this stream drops.
				go subscribeOutgoingReply(peerURL, res.Task.ID, d.Store)
			}
			b, _ := json.MarshalIndent(res, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_send_streaming",
			mcp.WithDescription("Call a2a.SendStreamingMessage and wait until the task reaches a terminal state. Returns aggregated artifacts + final status."),
			mcp.WithString("peer_url", mcp.Required()),
			mcp.WithString("text", mcp.Required()),
			mcp.WithNumber("timeout_s", mcp.Description("Max seconds to wait (default 300)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peerURL, _ := req.RequireString("peer_url")
			text, _ := req.RequireString("text")
			timeout := 300 * time.Second
			if v, err := req.RequireFloat("timeout_s"); err == nil && v > 0 {
				timeout = time.Duration(v * float64(time.Second))
			}

			// Same secret screen as a2a_send_message — streaming must not
			// be a side door for unredacted text.
			redacted, hits := screenOutbound(text, d.logger())
			text = redacted
			meta := map[string]any{"from": d.OwnCard.Name, "fromUrl": d.OwnCard.URL}
			if len(hits) > 0 {
				meta["redactions"] = security.FormatMatches(hits)
			}

			client := a2a.NewClient(peerURL)
			params := a2a.MessageSendParams{
				Message: a2a.Message{
					MessageID: uuid.NewString(),
					Role:      a2a.RoleUser,
					Parts:     []a2a.Part{{Text: text}},
					Metadata:  meta,
				},
			}

			sctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			ch := make(chan a2a.StreamResponse, 16)
			errCh := make(chan error, 1)
			go func() { errCh <- client.SendStreamingMessage(sctx, params, ch); close(ch) }()

			var collected []a2a.StreamResponse
			for ev := range ch {
				collected = append(collected, ev)
			}
			if err := <-errCh; err != nil && !errors.Is(err, context.Canceled) {
				if errors.Is(err, context.DeadlineExceeded) {
					// The peer accepted the message and is still working —
					// don't discard what we already streamed; surface it
					// with an explicit note.
					metrics.IncMessagesSent()
					b, _ := json.MarshalIndent(collected, "", "  ")
					return mcp.NewToolResultText(fmt.Sprintf(
						"note: timed out after %s before the task reached a terminal state; events collected so far:\n%s",
						timeout, b)), nil
				}
				return mcp.NewToolResultError(err.Error()), nil
			}
			metrics.IncMessagesSent()
			b, _ := json.MarshalIndent(collected, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_get_task",
			mcp.WithDescription("Call a2a.GetTask on a peer."),
			mcp.WithString("peer_url", mcp.Required()),
			mcp.WithString("task_id", mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peerURL, _ := req.RequireString("peer_url")
			taskID, _ := req.RequireString("task_id")
			t, err := a2a.NewClient(peerURL).GetTask(ctx, taskID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(t, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_cancel_task",
			mcp.WithDescription("Call a2a.CancelTask on a peer."),
			mcp.WithString("peer_url", mcp.Required()),
			mcp.WithString("task_id", mcp.Required()),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peerURL, _ := req.RequireString("peer_url")
			taskID, _ := req.RequireString("task_id")
			t, err := a2a.NewClient(peerURL).CancelTask(ctx, taskID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			b, _ := json.MarshalIndent(t, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_inbox",
			mcp.WithDescription("Drain or peek pending messages that peers have sent to this agent. Each entry includes taskId so you can a2a_complete_task after answering."),
			mcp.WithBoolean("peek", mcp.Description("If true, read without clearing")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			peek, _ := req.RequireBool("peek")
			var msgs []a2a.Message
			if peek {
				msgs = d.Store.PeekInbox()
			} else {
				msgs = d.Store.DrainInbox()
			}
			if len(msgs) == 0 {
				return mcp.NewToolResultText("[]"), nil
			}
			b, _ := json.MarshalIndent(msgs, "", "  ")
			return mcp.NewToolResultText(string(b)), nil
		},
	)

	s.AddTool(
		mcp.NewTool("a2a_complete_task",
			mcp.WithDescription("Attach a reply as an Artifact and mark the local task COMPLETED. Use this to answer a peer's incoming message after processing it."),
			mcp.WithString("task_id", mcp.Required()),
			mcp.WithString("text", mcp.Required(), mcp.Description("Reply body")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			taskID, _ := req.RequireString("task_id")
			text, _ := req.RequireString("text")
			// The reply leaves this agent via SSE / push webhooks, so it
			// goes through the same secret screen as direct sends.
			redacted, hits := screenOutbound(text, d.logger())
			if err := d.Store.CompleteTask(taskID, redacted); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			metrics.IncMessagesSent()
			if len(hits) > 0 {
				return mcp.NewToolResultText("completed (" + security.FormatMatches(hits) + ")"), nil
			}
			return mcp.NewToolResultText("completed"), nil
		},
	)
}

// PeerInfo combines directory entry + fetched Agent Card.
type PeerInfo struct {
	URL  string         `json:"url"`
	Card *a2a.AgentCard `json:"card,omitempty"`
	Err  string         `json:"error,omitempty"`
}

func listPeers(ctx context.Context, directoryURL, selfURL string) ([]PeerInfo, error) {
	if directoryURL == "" {
		return nil, fmt.Errorf("A2A_DIRECTORY not set")
	}
	dirCtx, cancel := context.WithTimeout(ctx, directoryRequestTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(dirCtx, http.MethodGet, strings.TrimRight(directoryURL, "/")+"/agents", http.NoBody)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var entries []struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, err
	}
	peers := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.URL == selfURL {
			continue
		}
		peers = append(peers, e.URL)
	}
	// Fetch agent cards concurrently with a bounded fan-out — sequential
	// fetches make one hung peer stall the whole discovery call.
	out := make([]PeerInfo, len(peers))
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, peerURL := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info := PeerInfo{URL: peerURL}
			cardCtx, ccancel := context.WithTimeout(ctx, directoryRequestTimeout)
			defer ccancel()
			card, err := a2a.NewClient(peerURL).FetchAgentCard(cardCtx)
			if err != nil {
				info.Err = err.Error()
			} else {
				info.Card = card
			}
			out[i] = info
		}()
	}
	wg.Wait()
	return out, nil
}

// Heartbeat periodically re-registers this agent with the directory.
// Every POST carries its own timeout so a wedged directory can't park
// this goroutine on a response that never comes.
func Heartbeat(ctx context.Context, directoryURL, selfURL string) {
	body, _ := json.Marshal(map[string]string{"url": selfURL})
	do := func(reqCtx context.Context, path string, timeout time.Duration) {
		rctx, cancel := context.WithTimeout(reqCtx, timeout)
		defer cancel()
		req, _ := http.NewRequestWithContext(rctx, http.MethodPost,
			strings.TrimRight(directoryURL, "/")+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}
	do(ctx, "/register", directoryRequestTimeout)
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			// The outer ctx is already canceled — a request built on it
			// would abort instantly and the directory would keep listing a
			// dead bridge for a full TTL. Use a short fresh context.
			do(context.Background(), "/unregister", 2*time.Second)
			return
		case <-t.C:
			do(ctx, "/heartbeat", directoryRequestTimeout)
		}
	}
}

// subscribeOutgoingReply opens an a2a.SubscribeToTask SSE stream on the
// peer for the just-created outbound task. As soon as the peer's task
// reaches a terminal state we resolve the full Task via GetTask and hand
// it to Store.IngestOutgoingTerminal — that drops a synthetic reply into
// our inbox without waiting for the 5-second polling fallback.
//
// Network jitter is fine: if the SSE connection drops mid-stream the
// polling loop in cmd/a2abridge/bridge.go will catch the same task on
// its next tick. We spend at most an HTTP keep-alive's worth of memory
// per outstanding outbound task.
func subscribeOutgoingReply(peerURL, taskID string, store *Store) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	client := a2a.NewClient(peerURL)
	ch := make(chan a2a.StreamResponse, 4)

	go func() {
		defer close(ch)
		_ = client.SubscribeToTask(ctx, taskID, ch)
	}()

	terminal := false
	for ev := range ch {
		// Two paths reach a terminal: (a) statusUpdate with final=true and a
		// terminal state, (b) a final 'task' message after the peer aggregated.
		if ev.StatusUpdate != nil && ev.StatusUpdate.Final {
			if isTerminal(ev.StatusUpdate.Status.State) {
				terminal = true
				break
			}
		}
		if ev.Task != nil && isTerminal(ev.Task.Status.State) {
			terminal = true
			break
		}
	}
	if !terminal {
		return
	}
	// Resolve the full task — statusUpdate carries a state but not artifacts;
	// we want the artefact text in the synthesized reply.
	resolveCtx, rcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer rcancel()
	t, err := client.GetTask(resolveCtx, taskID)
	if err != nil || t == nil {
		return
	}
	store.IngestOutgoingTerminal(t)
}
