package agent

import (
	"io"
	"log/slog"
	"testing"

	"github.com/vbcherepanov/a2abridge/internal/a2a"
)

// TestIsSyntheticReply guards the responder against echo loops: synthetic
// outgoing-reply messages (and any agent-authored message) must be
// skipped, otherwise the responder spawns a paid headless LLM run that
// answers its own peer's answer and then fails CompleteTask.
func TestIsSyntheticReply(t *testing.T) {
	cases := []struct {
		name string
		msg  a2a.Message
		want bool
	}{
		{
			name: "outgoing-reply kind",
			msg: a2a.Message{
				Role:     a2a.RoleUser,
				Metadata: map[string]any{"kind": "outgoing-reply"},
			},
			want: true,
		},
		{
			name: "agent role",
			msg:  a2a.Message{Role: a2a.RoleAgent},
			want: true,
		},
		{
			name: "regular inbound from peer",
			msg: a2a.Message{
				Role:     a2a.RoleUser,
				Metadata: map[string]any{"from": "peer-A"},
			},
			want: false,
		},
		{
			name: "no metadata",
			msg:  a2a.Message{Role: a2a.RoleUser},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isSyntheticReply(&tc.msg); got != tc.want {
				t.Errorf("isSyntheticReply = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResponderHandleSkipsSyntheticReply — Handle must return before
// spawning anything for synthetic replies. We give it a message whose
// task doesn't exist; if the skip is broken the test would spawn a real
// CLI process (caught by the missing-task CompleteTask error never being
// reached in time anyway — the early return keeps this instant).
func TestResponderHandleSkipsSyntheticReply(t *testing.T) {
	s := NewStore()
	defer s.Close()
	r := &Responder{
		Mode:  "claude",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: s,
	}
	r.Handle(a2a.Message{
		MessageID: "reply-x",
		TaskID:    "x",
		Role:      a2a.RoleAgent,
		Parts:     []a2a.Part{{Text: "peer answer"}},
		Metadata:  map[string]any{"kind": "outgoing-reply"},
	})
	// Reaching this point without spawning/blocking is the assertion;
	// the store must also be untouched.
	if len(s.PeekInbox()) != 0 {
		t.Error("inbox changed by skipped message")
	}
}
