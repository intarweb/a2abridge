// Package a2a contains types per the A2A protocol specification
// (https://a2a-protocol.org/latest/specification/).
package a2a

import (
	"encoding/json"
	"fmt"
	"time"
)

// WellKnownPath is the URL where the Agent Card is published per A2A 1.0.
const WellKnownPath = "/.well-known/agent-card.json"

// WellKnownPathLegacy is the pre-1.0 card location still served (and
// fetched as a fallback) for rolling upgrades of older bridges.
const WellKnownPathLegacy = "/.well-known/a2a"

// TaskState per A2A spec §6.4 (JSON wire values).
type TaskState string

const (
	TaskStateUnspecified   TaskState = "unknown"
	TaskStateSubmitted     TaskState = "submitted"
	TaskStateWorking       TaskState = "working"
	TaskStateCompleted     TaskState = "completed"
	TaskStateFailed        TaskState = "failed"
	TaskStateCanceled      TaskState = "canceled"
	TaskStateInputRequired TaskState = "input-required"
	TaskStateRejected      TaskState = "rejected"
	TaskStateAuthRequired  TaskState = "auth-required"
)

// Role per A2A spec §6.2 (JSON wire values).
type Role string

const (
	RoleUser  Role = "user"
	RoleAgent Role = "agent"
)

// AgentCard — the metadata document published at /.well-known/agent-card.json.
type AgentCard struct {
	ProtocolVersion    string                `json:"protocolVersion"`
	Name               string                `json:"name"`
	Description        string                `json:"description,omitempty"`
	URL                string                `json:"url"` // base endpoint for JSON-RPC
	PreferredTransport string                `json:"preferredTransport,omitempty"`
	Provider           *AgentProvider        `json:"provider,omitempty"`
	Version            string                `json:"version"`
	Capabilities       AgentCapabilities     `json:"capabilities"`
	SecuritySchemes    map[string]any        `json:"securitySchemes,omitempty"`
	Security           []map[string][]string `json:"security,omitempty"`
	DefaultInputModes  []string              `json:"defaultInputModes,omitempty"`
	DefaultOutputModes []string              `json:"defaultOutputModes,omitempty"`
	Skills             []AgentSkill          `json:"skills,omitempty"`
}

type AgentProvider struct {
	Organization string `json:"organization"`
	URL          string `json:"url,omitempty"`
}

type AgentCapabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
	ExtendedAgentCard bool `json:"extendedAgentCard,omitempty"`
}

type AgentSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// Part — one-of text/raw/url/data. Exactly one content field is set.
// The Go shape is kept flat for API stability; on the wire it follows the
// spec discriminated union (§6.5): {"kind":"text"|"file"|"data", ...}.
// See MarshalJSON / UnmarshalJSON below.
type Part struct {
	Text      string          `json:"text,omitempty"`
	Raw       []byte          `json:"raw,omitempty"`
	URL       string          `json:"url,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	MediaType string          `json:"mediaType,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
}

// filePart is the spec §6.5 FilePart payload.
type filePart struct {
	Bytes    []byte `json:"bytes,omitempty"` // base64 on the wire
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Name     string `json:"name,omitempty"`
}

// MarshalJSON emits the spec discriminated-union shape:
// text  → {"kind":"text","text":...}
// file  → {"kind":"file","file":{"bytes":...,"uri":...,"mimeType":...,"name":...}}
// data  → {"kind":"data","data":...}
// The value receiver is deliberate: json.Marshal must find the method on
// non-addressable values too (struct fields, map values, slice elements).
func (p Part) MarshalJSON() ([]byte, error) {
	switch {
	case p.Raw != nil || p.URL != "" || p.Filename != "":
		return json.Marshal(struct {
			Kind     string         `json:"kind"`
			File     filePart       `json:"file"`
			Metadata map[string]any `json:"metadata,omitempty"`
		}{"file", filePart{Bytes: p.Raw, URI: p.URL, MimeType: p.MediaType, Name: p.Filename}, p.Metadata})
	case len(p.Data) > 0:
		return json.Marshal(struct {
			Kind     string          `json:"kind"`
			Data     json.RawMessage `json:"data"`
			Metadata map[string]any  `json:"metadata,omitempty"`
		}{"data", p.Data, p.Metadata})
	default:
		return json.Marshal(struct {
			Kind     string         `json:"kind"`
			Text     string         `json:"text"`
			Metadata map[string]any `json:"metadata,omitempty"`
		}{"text", p.Text, p.Metadata})
	}
}

// UnmarshalJSON accepts both the spec union shape (discriminated by
// "kind") and the legacy flat shape emitted by pre-1.0 bridges, so peers
// can roll out the upgrade independently.
func (p *Part) UnmarshalJSON(b []byte) error {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return err
	}
	if probe.Kind == "" {
		// Legacy flat shape (no discriminator).
		type flat Part
		var f flat
		if err := json.Unmarshal(b, &f); err != nil {
			return err
		}
		*p = Part(f)
		return nil
	}
	var spec struct {
		Text     string          `json:"text"`
		File     filePart        `json:"file"`
		Data     json.RawMessage `json:"data"`
		Metadata map[string]any  `json:"metadata"`
	}
	if err := json.Unmarshal(b, &spec); err != nil {
		return err
	}
	*p = Part{Metadata: spec.Metadata}
	switch probe.Kind {
	case "text":
		p.Text = spec.Text
	case "file":
		p.Raw, p.URL, p.MediaType, p.Filename = spec.File.Bytes, spec.File.URI, spec.File.MimeType, spec.File.Name
	case "data":
		p.Data = spec.Data
	default:
		return fmt.Errorf("part: unknown kind %q", probe.Kind)
	}
	return nil
}

// Message per A2A spec §6.2.
type Message struct {
	MessageID        string         `json:"messageId"`
	ContextID        string         `json:"contextId,omitempty"`
	TaskID           string         `json:"taskId,omitempty"`
	Role             Role           `json:"role"`
	Parts            []Part         `json:"parts"`
	Metadata         map[string]any `json:"metadata,omitempty"`
	Extensions       []string       `json:"extensions,omitempty"`
	ReferenceTaskIDs []string       `json:"referenceTaskIds,omitempty"`
	Kind             string         `json:"kind"` // always "message" on the wire
}

// MarshalJSON forces the spec discriminator kind="message". Value
// receiver is deliberate — see Part.MarshalJSON.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message
	m.Kind = "message"
	return json.Marshal(alias(m))
}

// Artifact per A2A spec §6.6.
type Artifact struct {
	ArtifactID  string         `json:"artifactId"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parts       []Part         `json:"parts"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Extensions  []string       `json:"extensions,omitempty"`
}

// TaskStatus per A2A spec §6.3.
type TaskStatus struct {
	State     TaskState `json:"state"`
	Message   *Message  `json:"message,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// Task per A2A spec §6.1.
type Task struct {
	ID        string         `json:"id"`
	ContextID string         `json:"contextId,omitempty"`
	Status    TaskStatus     `json:"status"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
	History   []Message      `json:"history,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	Kind      string         `json:"kind"` // always "task" on the wire
}

// MarshalJSON forces the spec discriminator kind="task". Value receiver
// is deliberate — see Part.MarshalJSON.
func (t Task) MarshalJSON() ([]byte, error) {
	type alias Task
	t.Kind = "task"
	return json.Marshal(alias(t))
}

// --- JSON-RPC params & results ---

// MessageSendConfiguration per §7.1.
type MessageSendConfiguration struct {
	AcceptedOutputModes []string `json:"acceptedOutputModes,omitempty"`
	Blocking            bool     `json:"blocking,omitempty"`
	HistoryLength       int      `json:"historyLength,omitempty"`
}

// MessageSendParams — params for a2a.SendMessage / a2a.SendStreamingMessage.
type MessageSendParams struct {
	Message       Message                   `json:"message"`
	Configuration *MessageSendConfiguration `json:"configuration,omitempty"`
	Metadata      map[string]any            `json:"metadata,omitempty"`
}

// TaskIDParams — params for a2a.GetTask / a2a.CancelTask / a2a.SubscribeToTask.
type TaskIDParams struct {
	ID            string `json:"id"`
	HistoryLength int    `json:"historyLength,omitempty"`
}

// StreamResponse — union event emitted on SSE channel.
type StreamResponse struct {
	Task           *Task                    `json:"task,omitempty"`
	Message        *Message                 `json:"message,omitempty"`
	StatusUpdate   *TaskStatusUpdateEvent   `json:"statusUpdate,omitempty"`
	ArtifactUpdate *TaskArtifactUpdateEvent `json:"artifactUpdate,omitempty"`
}

type TaskStatusUpdateEvent struct {
	TaskID    string     `json:"taskId"`
	ContextID string     `json:"contextId,omitempty"`
	Status    TaskStatus `json:"status"`
	Final     bool       `json:"final"`
}

type TaskArtifactUpdateEvent struct {
	TaskID    string   `json:"taskId"`
	ContextID string   `json:"contextId,omitempty"`
	Artifact  Artifact `json:"artifact"`
	Append    bool     `json:"append,omitempty"`
	LastChunk bool     `json:"lastChunk,omitempty"`
}

// --- JSON-RPC envelope ---

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse — note ID has no omitempty: per JSON-RPC 2.0 a response
// to an unparseable request MUST carry "id": null, which is exactly what a
// nil json.RawMessage encodes to.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Method names per A2A spec §7 (JSON-RPC 2.0 binding).
const (
	MethodSendMessage          = "message/send"
	MethodSendStreamingMessage = "message/stream"
	MethodGetTask              = "tasks/get"
	MethodCancelTask           = "tasks/cancel"
	MethodSubscribeToTask      = "tasks/resubscribe"
	MethodGetExtendedCard      = "agent/getAuthenticatedExtendedCard"

	// MethodListTasks is a private non-spec extension of this bridge
	// (the A2A spec defines no task enumeration method).
	MethodListTasks = "tasks/list"

	// Push Notification configuration (§9.5).
	MethodCreatePushConfig = "tasks/pushNotificationConfig/set"
	MethodGetPushConfig    = "tasks/pushNotificationConfig/get"
	MethodListPushConfig   = "tasks/pushNotificationConfig/list"
	MethodDeletePushConfig = "tasks/pushNotificationConfig/delete"
)

// legacyMethodAliases maps the deprecated proto-style method names used
// by pre-1.0 bridges to the spec JSON-RPC names, so peers can be upgraded
// one at a time without breaking in-flight traffic.
var legacyMethodAliases = map[string]string{
	"a2a.SendMessage":                      MethodSendMessage,
	"a2a.SendStreamingMessage":             MethodSendStreamingMessage,
	"a2a.GetTask":                          MethodGetTask,
	"a2a.ListTasks":                        MethodListTasks,
	"a2a.CancelTask":                       MethodCancelTask,
	"a2a.SubscribeToTask":                  MethodSubscribeToTask,
	"a2a.GetExtendedAgentCard":             MethodGetExtendedCard,
	"a2a.CreateTaskPushNotificationConfig": MethodCreatePushConfig,
	"a2a.GetTaskPushNotificationConfig":    MethodGetPushConfig,
	"a2a.ListTaskPushNotificationConfig":   MethodListPushConfig,
	"a2a.DeleteTaskPushNotificationConfig": MethodDeletePushConfig,
}

// PushNotificationConfig — peer-supplied webhook for task state updates (§9.5).
type PushNotificationConfig struct {
	ID             string                       `json:"id,omitempty"` // server-assigned
	URL            string                       `json:"url"`
	Token          string                       `json:"token,omitempty"` // shared secret echoed in X-A2A-Token
	Authentication *PushNotificationAuthDetails `json:"authentication,omitempty"`
}

// PushNotificationAuthDetails — optional authentication mode per A2A 1.0.
// Schemes is a list of supported auth scheme names (e.g. "Bearer", "Basic")
// as defined in the agent card's securitySchemes map.
type PushNotificationAuthDetails struct {
	Schemes     []string `json:"schemes"`
	Credentials string   `json:"credentials,omitempty"` // free-form per scheme
}

// TaskPushNotificationConfig wraps a config with its taskId for the
// Create/Get/List endpoints.
type TaskPushNotificationConfig struct {
	TaskID string                 `json:"taskId"`
	Config PushNotificationConfig `json:"pushNotificationConfig"`
}

// PushNotificationConfigParams for Get/Delete by config id.
type PushNotificationConfigParams struct {
	TaskID       string `json:"taskId"`
	PushConfigID string `json:"pushNotificationConfigId,omitempty"`
}

// Error codes per A2A spec §8.
const (
	ErrCodeParse          = -32700
	ErrCodeInvalidRequest = -32600
	ErrCodeMethodNotFound = -32601
	ErrCodeInvalidParams  = -32602
	ErrCodeInternal       = -32603

	ErrCodeTaskNotFound                 = -32001
	ErrCodeTaskNotCancelable            = -32002
	ErrCodePushNotificationNotSupported = -32003
	ErrCodeUnsupportedOperation         = -32004
	ErrCodeContentTypeNotSupported      = -32005
	ErrCodeInvalidAgentResponse         = -32006
	ErrCodeVersionNotSupported          = -32007
)

const ProtocolVersion = "1.0"

// maxBodySize caps request bodies accepted by the server and response
// bodies the client reads in full, protecting both sides from unbounded
// memory use on a hostile or buggy peer.
const maxBodySize = 8 << 20 // 8 MiB
