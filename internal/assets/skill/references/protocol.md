# A2A 1.0 — protocol cheatsheet

a2abridge speaks the [Agent2Agent 1.0 specification](https://a2a-protocol.org/latest/specification/) under the Linux Foundation. This file is a quick reference; the spec is authoritative.

## Transport

JSON-RPC 2.0 over HTTP at `POST /`, with `Content-Type: application/json` and the header `A2A-Version: 1.0`. Streaming methods use Server-Sent Events. A REST convenience API mirrors the verbs under `/v1/...` (non-spec extension — the spec binding is the JSON-RPC one).

## Methods (§7)

| Method | Purpose |
|---|---|
| `message/send` | Send a message; create a task |
| `message/stream` | Send + receive task stream via SSE |
| `tasks/get` | Poll task state |
| `tasks/list` | List tasks (non-spec extension) |
| `tasks/cancel` | Cancel a running task |
| `tasks/resubscribe` | SSE stream for an existing task |
| `tasks/pushNotificationConfig/set` / `get` / `list` / `delete` | Manage push-notification webhooks (§9.5) |
| `agent/getAuthenticatedExtendedCard` | Authenticated card with extra fields |

Legacy proto-style names (`a2a.SendMessage`, `a2a.GetTask`, ...) from pre-3.0 are still accepted by the server as deprecated aliases — always emit the spec names above.

## Task states (§6.4)

`submitted` → `working` → `completed`
Other terminal: `failed`, `canceled`, `rejected`, `input-required`, `auth-required`.

## Message / Part / Artifact (§6.1–6.6)

A `Message` carries `parts` (text / file / data) and a `role` (`user` or `agent`). The Part union is spec-shaped on the wire:

```json
{"kind": "text", "text": "..."}
{"kind": "file", "file": {"name": "...", "mimeType": "...", "bytes": "..."}}
{"kind": "data", "data": {"...": "..."}}
```

The peer replies with one or more `Artifact`s when the task reaches `completed`.

## Error codes (§8)

`-32001` TaskNotFound · `-32002` TaskNotCancelable · `-32003` PushNotificationsNotSupported · `-32004` UnsupportedOperation · `-32005` ContentTypeNotSupported · `-32006` InvalidAgentResponse · `-32099..-32000` JSON-RPC reserved.

## What a2abridge implements today

- Agent Card on `/.well-known/agent-card.json` (§5) — the legacy path `/.well-known/a2a` is still served as an alias
- All JSON-RPC methods above (§7), including Push Notifications (§9.5) with webhook delivery
- TaskState enum, Message/Part/Artifact, StreamResponse one-of
- Header `A2A-Version: 1.0`
- REST convenience API at `/v1/...` (non-spec extension)
- Loopback by default; opt-in cross-machine federation via mTLS + ed25519 (`A2A_TLS_CERT` / `A2A_TLS_KEY` / `A2A_TRUST_ROOTS`)

Not yet implemented: gRPC binding (§7.2).
