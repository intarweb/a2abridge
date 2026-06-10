package a2a

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestTaskStateWireValues(t *testing.T) {
	cases := map[TaskState]string{
		TaskStateUnspecified:   "unknown",
		TaskStateSubmitted:     "submitted",
		TaskStateWorking:       "working",
		TaskStateCompleted:     "completed",
		TaskStateFailed:        "failed",
		TaskStateCanceled:      "canceled",
		TaskStateInputRequired: "input-required",
		TaskStateRejected:      "rejected",
		TaskStateAuthRequired:  "auth-required",
	}
	for state, want := range cases {
		if string(state) != want {
			t.Errorf("TaskState = %q, want %q", state, want)
		}
	}
}

func TestRoleWireValues(t *testing.T) {
	if RoleUser != "user" {
		t.Errorf("RoleUser = %q, want user", RoleUser)
	}
	if RoleAgent != "agent" {
		t.Errorf("RoleAgent = %q, want agent", RoleAgent)
	}
}

func TestMethodNamesMatchSpec(t *testing.T) {
	cases := map[string]string{
		MethodSendMessage:          "message/send",
		MethodSendStreamingMessage: "message/stream",
		MethodGetTask:              "tasks/get",
		MethodCancelTask:           "tasks/cancel",
		MethodSubscribeToTask:      "tasks/resubscribe",
		MethodGetExtendedCard:      "agent/getAuthenticatedExtendedCard",
		MethodListTasks:            "tasks/list", // non-spec private extension
		MethodCreatePushConfig:     "tasks/pushNotificationConfig/set",
		MethodGetPushConfig:        "tasks/pushNotificationConfig/get",
		MethodListPushConfig:       "tasks/pushNotificationConfig/list",
		MethodDeletePushConfig:     "tasks/pushNotificationConfig/delete",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
	}
	for legacy, canonical := range legacyMethodAliases {
		if !strings.HasPrefix(legacy, "a2a.") {
			t.Errorf("legacy alias %q must keep the old a2a. prefix", legacy)
		}
		if _, ok := cases[canonical]; !ok {
			t.Errorf("alias %q maps to unknown method %q", legacy, canonical)
		}
	}
}

func TestWellKnownPaths(t *testing.T) {
	if WellKnownPath != "/.well-known/agent-card.json" {
		t.Errorf("WellKnownPath = %q", WellKnownPath)
	}
	if WellKnownPathLegacy != "/.well-known/a2a" {
		t.Errorf("WellKnownPathLegacy = %q", WellKnownPathLegacy)
	}
}

func TestPartRoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		part     Part
		wantKind string
	}{
		{"text", Part{Text: "hello"}, "text"},
		{"empty text", Part{}, "text"},
		{"text with metadata", Part{Text: "hi", Metadata: map[string]any{"k": "v"}}, "text"},
		{"file bytes", Part{Raw: []byte{0x01, 0x02}, MediaType: "application/octet-stream", Filename: "blob.bin"}, "file"},
		{"file uri", Part{URL: "https://example.test/doc.pdf", MediaType: "application/pdf"}, "file"},
		{"data", Part{Data: json.RawMessage(`{"a":1}`)}, "data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.part)
			if err != nil {
				t.Fatal(err)
			}
			var disc struct {
				Kind string `json:"kind"`
			}
			if err := json.Unmarshal(b, &disc); err != nil {
				t.Fatal(err)
			}
			if disc.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q (wire: %s)", disc.Kind, tc.wantKind, b)
			}
			var back Part
			if err := json.Unmarshal(b, &back); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.part, back) {
				t.Errorf("round-trip mismatch:\n  in:  %+v\n  out: %+v\n  wire: %s", tc.part, back, b)
			}
		})
	}
}

// TestPartUnmarshalSpecFixtures feeds raw JSON exactly as a spec-compliant
// peer would send it (A2A 1.0 §6.5 discriminated union).
func TestPartUnmarshalSpecFixtures(t *testing.T) {
	cases := []struct {
		name string
		json string
		want Part
	}{
		{
			"text part",
			`{"kind":"text","text":"hello world"}`,
			Part{Text: "hello world"},
		},
		{
			"file part with bytes",
			`{"kind":"file","file":{"bytes":"aGk=","mimeType":"text/plain","name":"hi.txt"}}`,
			Part{Raw: []byte("hi"), MediaType: "text/plain", Filename: "hi.txt"},
		},
		{
			"file part with uri",
			`{"kind":"file","file":{"uri":"https://example.test/report.pdf","mimeType":"application/pdf"}}`,
			Part{URL: "https://example.test/report.pdf", MediaType: "application/pdf"},
		},
		{
			"data part",
			`{"kind":"data","data":{"answer":42}}`,
			Part{Data: json.RawMessage(`{"answer":42}`)},
		},
		{
			"text part with metadata",
			`{"kind":"text","text":"x","metadata":{"lang":"en"}}`,
			Part{Text: "x", Metadata: map[string]any{"lang": "en"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Part
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.want, got) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestPartUnmarshalLegacyFlat keeps the pre-1.0 flat shape decodable so
// peers can be upgraded one at a time.
func TestPartUnmarshalLegacyFlat(t *testing.T) {
	cases := []struct {
		name string
		json string
		want Part
	}{
		{"legacy text", `{"text":"hi"}`, Part{Text: "hi"}},
		{
			"legacy raw",
			`{"raw":"aGk=","mediaType":"application/octet-stream","filename":"x.bin"}`,
			Part{Raw: []byte("hi"), MediaType: "application/octet-stream", Filename: "x.bin"},
		},
		{"legacy url", `{"url":"https://example.test/a"}`, Part{URL: "https://example.test/a"}},
		{"legacy data", `{"data":[1,2]}`, Part{Data: json.RawMessage(`[1,2]`)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got Part
			if err := json.Unmarshal([]byte(tc.json), &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(tc.want, got) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestPartUnmarshalUnknownKindFails(t *testing.T) {
	var p Part
	if err := json.Unmarshal([]byte(`{"kind":"video","video":{}}`), &p); err == nil {
		t.Error("expected error for unknown part kind")
	}
}

func TestMessageMarshalSetsKind(t *testing.T) {
	b, err := json.Marshal(Message{MessageID: "m1", Role: RoleAgent, Parts: []Part{{Text: "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"kind":"message"`)) {
		t.Errorf(`missing "kind":"message": %s`, b)
	}
	if !bytes.Contains(b, []byte(`"role":"agent"`)) {
		t.Errorf(`missing "role":"agent": %s`, b)
	}
}

func TestTaskMarshalSetsKind(t *testing.T) {
	b, err := json.Marshal(&Task{ID: "t1", Status: TaskStatus{State: TaskStateWorking}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"kind":"task"`)) {
		t.Errorf(`missing "kind":"task": %s`, b)
	}
	if !bytes.Contains(b, []byte(`"state":"working"`)) {
		t.Errorf(`missing wire state: %s`, b)
	}
}

func TestJSONRPCResponseNilIDEncodesNull(t *testing.T) {
	b, err := json.Marshal(JSONRPCResponse{JSONRPC: "2.0", Error: &JSONRPCError{Code: ErrCodeParse, Message: "parse error"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"id":null`)) {
		t.Errorf(`nil ID must encode as null: %s`, b)
	}
	// And a real id is still echoed verbatim.
	b, err = json.Marshal(JSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage(`42`), Result: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"id":42`)) {
		t.Errorf(`request id must be echoed: %s`, b)
	}
}
