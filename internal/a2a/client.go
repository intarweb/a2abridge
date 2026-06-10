package a2a

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// nonStreamingTimeout bounds unary RPC calls and agent-card fetches.
// Streaming requests deliberately carry no client-side deadline.
const nonStreamingTimeout = 60 * time.Second

// Client is an A2A JSON-RPC 2.0 client.
type Client struct {
	BaseURL string // e.g. http://127.0.0.1:49152 or https://...
	HTTP    *http.Client
}

// DefaultTransport is consulted by NewClient when callers don't supply
// their own *http.Client. Bridges set it to a TLS-aware transport when
// running with mTLS so every a2a.NewClient() call inherits the right
// certs without each call site re-plumbing tls.Config.
var DefaultTransport http.RoundTripper

func NewClient(baseURL string) *Client {
	c := &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 0}, // streaming needs no default timeout
	}
	if DefaultTransport != nil {
		c.HTTP.Transport = DefaultTransport
	}
	return c
}

// httpClient returns the configured *http.Client, falling back to
// http.DefaultClient so a zero-value / struct-literal Client never panics.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// FetchAgentCard GETs /.well-known/agent-card.json, falling back to the
// legacy /.well-known/a2a path when the peer predates A2A 1.0 paths.
func (c *Client) FetchAgentCard(ctx context.Context) (*AgentCard, error) {
	ctx, cancel := context.WithTimeout(ctx, nonStreamingTimeout)
	defer cancel()
	card, status, err := c.fetchCard(ctx, WellKnownPath)
	if err == nil {
		return card, nil
	}
	if status == http.StatusNotFound {
		if legacy, _, lerr := c.fetchCard(ctx, WellKnownPathLegacy); lerr == nil {
			return legacy, nil
		}
	}
	return nil, err
}

func (c *Client) fetchCard(ctx context.Context, path string) (*AgentCard, int, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+path, http.NoBody)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, fmt.Errorf("agent card: %s", resp.Status)
	}
	var card AgentCard
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodySize)).Decode(&card); err != nil {
		return nil, resp.StatusCode, err
	}
	return &card, resp.StatusCode, nil
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	ctx, cancel := context.WithTimeout(ctx, nonStreamingTimeout)
	defer cancel()

	id, _ := json.Marshal(time.Now().UnixNano())
	rawParams, _ := json.Marshal(params)
	body, _ := json.Marshal(JSONRPCRequest{
		JSONRPC: "2.0", ID: id, Method: method, Params: rawParams,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", ProtocolVersion)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var r struct {
		JSONRPCResponse
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodySize)).Decode(&r); err != nil {
		return err
	}
	if r.Error != nil {
		return fmt.Errorf("rpc %s: %d %s", method, r.Error.Code, r.Error.Message)
	}
	if out != nil && len(r.Result) > 0 {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// SendMessage — message/send. Result is Task-or-Message union.
type SendMessageResult struct {
	Task    *Task
	Message *Message
}

func (c *Client) SendMessage(ctx context.Context, p MessageSendParams) (*SendMessageResult, error) {
	var raw json.RawMessage
	if err := c.call(ctx, MethodSendMessage, p, &raw); err != nil {
		return nil, err
	}
	// Dispatch on the spec "kind" discriminator when present; fall back to
	// field-presence sniffing for legacy peers that don't emit it.
	var probe struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &probe)
	switch probe.Kind {
	case "task":
		var t Task
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("SendMessage: decode task: %w", err)
		}
		return &SendMessageResult{Task: &t}, nil
	case "message":
		var m Message
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("SendMessage: decode message: %w", err)
		}
		return &SendMessageResult{Message: &m}, nil
	}
	var t Task
	if err := json.Unmarshal(raw, &t); err == nil && t.ID != "" {
		return &SendMessageResult{Task: &t}, nil
	}
	var m Message
	if err := json.Unmarshal(raw, &m); err == nil && m.MessageID != "" {
		return &SendMessageResult{Message: &m}, nil
	}
	return nil, errors.New("SendMessage: unknown result shape")
}

func (c *Client) GetTask(ctx context.Context, id string) (*Task, error) {
	var t Task
	if err := c.call(ctx, MethodGetTask, TaskIDParams{ID: id}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func (c *Client) CancelTask(ctx context.Context, id string) (*Task, error) {
	var t Task
	if err := c.call(ctx, MethodCancelTask, TaskIDParams{ID: id}, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// SendStreamingMessage opens an SSE stream and emits events until terminal state or ctx done.
func (c *Client) SendStreamingMessage(ctx context.Context, p MessageSendParams, out chan<- StreamResponse) error {
	return c.openStream(ctx, MethodSendStreamingMessage, p, out)
}

func (c *Client) SubscribeToTask(ctx context.Context, id string, out chan<- StreamResponse) error {
	return c.openStream(ctx, MethodSubscribeToTask, TaskIDParams{ID: id}, out)
}

func (c *Client) openStream(ctx context.Context, method string, params any, out chan<- StreamResponse) error {
	rpcID, _ := json.Marshal(time.Now().UnixNano())
	rawParams, _ := json.Marshal(params)
	body, _ := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", ID: rpcID, Method: method, Params: rawParams})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("A2A-Version", ProtocolVersion)

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
		return fmt.Errorf("stream: %s %s", resp.Status, b)
	}
	// Servers reject streams before the first event with a plain JSON-RPC
	// error body (HTTP 200, application/json) — surface it instead of
	// silently scanning an empty "stream".
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		var env struct {
			Error *JSONRPCError `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodySize)).Decode(&env); err == nil && env.Error != nil {
			return fmt.Errorf("stream rpc: %d %s", env.Error.Code, env.Error.Message)
		}
		return fmt.Errorf("stream: unexpected content-type %q", ct)
	}

	// dispatch handles one accumulated SSE event payload. Malformed
	// payloads are skipped — the Client carries no logger by design.
	dispatch := func(payload string) (done bool, err error) {
		var env struct {
			Result StreamResponse `json:"result"`
			Error  *JSONRPCError  `json:"error"`
		}
		if jsonErr := json.Unmarshal([]byte(payload), &env); jsonErr != nil {
			return false, nil
		}
		if env.Error != nil {
			return true, fmt.Errorf("stream rpc: %d %s", env.Error.Code, env.Error.Message)
		}
		select {
		case out <- env.Result:
		case <-ctx.Done():
			return true, ctx.Err()
		}
		// terminate if statusUpdate.final == true
		if env.Result.StatusUpdate != nil && env.Result.StatusUpdate.Final {
			return true, nil
		}
		return false, nil
	}

	// SSE parsing per the spec: consecutive "data:" lines accumulate and
	// are joined with "\n"; a blank line dispatches the event.
	br := bufio.NewReader(resp.Body)
	var data []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(data) > 0 {
					_, derr := dispatch(strings.Join(data, "\n"))
					return derr
				}
				return nil
			}
			return err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" { // event boundary
			if len(data) > 0 {
				payload := strings.Join(data, "\n")
				data = data[:0]
				if done, derr := dispatch(payload); done {
					return derr
				}
			}
			continue
		}
		if strings.HasPrefix(line, ":") { // comment / keepalive
			continue
		}
		if after, ok := strings.CutPrefix(line, "data:"); ok {
			// Per SSE spec, a single leading space after the colon is
			// stripped; further whitespace is payload.
			data = append(data, strings.TrimPrefix(after, " "))
		}
		// Other SSE fields (event:, id:, retry:) are ignored.
	}
}
