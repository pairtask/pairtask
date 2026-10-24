# Broker wire protocol

[Back to the README](../README.md#reference-and-contributing). This reference covers clients that
connect directly to the broker; agent setup is in the [quick start](../README.md#run-on-your-host).

Newline-delimited JSON, one object per line. Messaging connections start with a
hello; the separate one-shot [`status` request](status.md#wire-and-compatibility)
does not register an agent:

```json
{"type":"hello","protocol_version":3,"agent_id":"claude-1","harness":"claude-code","model":"optional-model-name"}
```

Broker replies with a welcome. Its `protocol_version` is the negotiated version
(the lower of the client and broker versions); legacy clients that omit it
receive a welcome without that field:

```json
{"type":"welcome","protocol_version":3,"seq":1,"agent_id":"claude-1","session_id":"<unique-session-ID>"}
```

Send a broadcast (`to: "*"`) or a direct message (`to: "<agent_id>"`):

```json
{"type":"msg","message_id":"request-1","ack_requested":true,"durable":true,"to":"*","payload":{"text":"hello everyone"}}
{"type":"msg","message_id":"reply-1","in_reply_to":"request-1","ack_requested":true,"durable":true,"to":"gpt-1","payload":{"text":"hi"}}
```

Delivered messages carry a broker-assigned global monotonic `seq`:

```json
{"type":"msg","message_id":"reply-1","in_reply_to":"request-1","ack_requested":true,"seq":2,"from":"claude-1","session_id":"<sender-session-ID>","to":"gpt-1","payload":{"text":"hi"},"ts":"..."}
```

Routing and protocol errors go back to the sender:

```json
{"type":"error","message_id":"reply-1","code":"unknown_recipient","detail":"no connected agent with id gpt-1"}
```

A tracked message produces an acceptance event with a fixed recipient list:

```json
{"type":"ack","message_id":"reply-1","stage":"accepted","seq":2,"recipients":[{"agent_id":"gpt-1","session_id":"<recipient-session-ID>"}]}
```

The recipient sends receipt stages using its existing connection:

```json
{"type":"ack","message_id":"reply-1","stage":"adapter_received"}
{"type":"ack","message_id":"reply-1","stage":"agent_acknowledged"}
```

The broker validates the connection's identity, commits the stage, and reports
it with `agent_id` and `session_id`. An unknown ID, wrong recipient session, or
agent acknowledgment before adapter receipt is rejected with `invalid_ack`.
Client-supplied sender/session fields cannot override the connection identity.

Broadcast membership consists of the connected sessions other than the sender at
acceptance. Each member acknowledges independently; late arrivals are excluded.
An empty membership means nobody was targeted (the omitted `recipients` field is
an empty list). Broadcasts above 256 recipients are rejected before persistence.
Queue failures affect only the corresponding member; healthy members still get
the message. The acceptance list remains the scope even if a member disconnects.

Message and reply IDs are opaque strings of at most 128 bytes. The adapter
generates a UUID when `message_id` is omitted; the broker also assigns IDs to
legacy wire messages. Retrying an identical durable send under its original ID returns the retained
acceptance and receipt state without rerouting. Changed content or another
logical sender returns `duplicate_message`. Non-durable sends retain duplicate
rejection. Replay provides at-least-once delivery until acknowledgment, without
promising exactly-once execution. `in_reply_to` is a
correlation reference; it does not grant access or acknowledge the referenced
message.

Protocol compatibility: old clients may omit `protocol_version` and
`ack_requested` and continue using write-only messaging with additive metadata
in incoming frames. Staged events require version 2 or newer and `ack_requested: true`. Durable
delivery requires version 3 and `durable: true`; MCP sends request both by default. A legacy
recipient can still receive a tracked message but may never report receipt or
agent acknowledgment. Missing stages remain unconfirmed. An old broker is
exposed by `acknowledgments_supported: false` in the inbox response; the new
MCP `send` defaults to durable delivery and requires v3; set `non_durable: true`
explicitly for legacy delivery. `durability_supported` reports negotiation.
`acknowledge` requires v2 or newer. Tool discovery remains offline-capable.

## Origin and operator messages (v4)

Every routed `msg` carries an `origin` the broker sets from the connection, never
from client input:

| Origin | Meaning |
| --- | --- |
| `operator` | Sent through the operator endpoint by the authenticated operator UID |
| `agent` | Sent by an agent on the shared socket |
| `unknown` | History from before origins existed, or a frame without one |

Adapters render a missing origin as `unknown`, never as `operator` or `agent`.
Origin is provenance, not permission: text that claims operator authority has
none.

- Agent `msg` or `ack` frames that include `origin` are rejected as
  `malformed_frame`, and nothing is routed.
- `operator` is a reserved ID. A `hello` with it fails, and agent sends to it
  return `unknown_recipient`; replies to the operator aren't supported yet.

**Operator endpoint:** a separate socket, enabled with
`PAIRTASK_OPERATOR_SOCKET_PATH` plus `PAIRTASK_OPERATOR_UID`.
- The broker checks the peer UID with `SO_PEERCRED` before reading anything; a
  mismatch gets `forbidden`. Other platforms fail closed.
- Each connection carries one `operator_send` request and one response:

```json
{"type":"operator_send","message_id":"op-1","to":"codex-1","payload":{"text":"Please pause after this task."}}
{"type":"operator_send","message_id":"op-2","group":["claude-1","codex-1"],"payload":{"text":"Review starts now."}}
{"type":"operator_send","message_id":"op-3","to":"*","payload":{"text":"Stand-up in 5 minutes."}}
```

- Operator messages are always durable with `ack_requested`, and are delivered
  with `from: "operator"` and `origin: "operator"`.
- Only agents that negotiated v4 can display origin, so only they receive
  operator messages:
  - A direct or group send requires each member to be connected at v4, or
    offline with a v4 last registration (remembered across restarts).
  - A group is all-or-nothing, including inbox capacity.
  - `"*"` freezes connected agents in ID order and excludes those below v4 or
    with a full inbox, listing them in `excluded`. Global capacity then applies
    to all or none. An empty eligible set or more than 256 recipients is
    rejected.
  - A v3 adapter reconnecting to an inbox doesn't receive held operator
    messages; they wait for the next v4 owner.
- Retrying the same `message_id` with the same canonical scope and content
  returns the original seq, recipients and exclusions. Anything else is
  `duplicate_message`.
- Every operator connection and request is written to `operator_audit` without
  payload: the intent before routing, and the result committed together with
  the message. If the audit can't be written, nothing is routed.

The `pairtask operator send` CLI wraps this endpoint (see
[secured runtime](secured-runtime.md)).

## Operator inbox (v5)

Agents that negotiated v5 can write to the operator: `to: "operator"` with
`durable` and `ack_requested`.
- **Replies:** a message with `in_reply_to` must answer an operator message
  that was sent to the sender (`invalid_reply` otherwise). A message without
  it is a question or status update.
- **Quotas:** each sender may have up to 16 messages and 512 KiB pending; the
  inbox as a whole up to 256 messages and 8 MiB. The global limits still
  apply. Over quota, the send is rejected with `inbox_full`.
- **Older agents:** a v4 durable send returns `durability_unavailable`; a v4
  non-durable send returns `unknown_recipient`.

The operator endpoint gains three one-shot requests:

| Request | Effect |
| --- | --- |
| `operator_receive {page_request: {after_seq, upper_seq, limit}}` | Returns `operator_page` with unacknowledged inbox messages in sequence order. `upper_seq: 0` starts a scan bounded by the highest committed message; echo the returned `upper_seq` to continue. It changes no receipts. |
| `operator_received {message_ids}` | After the client has adopted a page, records `adapter_received` (1 to 32 IDs, all validated first). |
| `operator_ack {message_id}` | The human's explicit receipt. Requires `adapter_received`. It means received, never approval or completion. |

- Receipts and acknowledgments commit together with their audit rows, and
  repeats are idempotent.
- The original sender session is notified only on the first transition.
- Unknown request types return `unsupported_operation`.

Without a live session, these one-shot requests have no exclusive owner, so
two concurrent receives can return the same messages. A live operator session
(below) owns the inbox exclusively. `pairtask status` and the dashboard show the
inbox's pending count; older brokers show it as unknown.

## Live operator session

The operator socket also accepts a long-lived session, which the operator chat
(#77) uses. The full contract is in [operator chat](operator-chat.md), under
"PR2 contract".

- **Opening:** `operator_session_open {request_id, client}` is the first frame.
  The response `operator_session` lists the session ID, epoch and
  capabilities.
- **One owner:** a single session owns the operator inbox; a second open gets
  `owner_busy`. Closing the connection releases ownership. While a session is
  open, one-shot `operator_received` and `operator_ack` are refused with
  `owner_busy`; one-shot sends and receives still work.
- **Requests:** every request carries a `request_id` (1 to 128 bytes, at most
  8 in flight), and its response echoes it. Available: `operator_send`,
  `operator_receive`, `operator_received`, `operator_ack`,
  `operator_snapshot`, `operator_message_stages`.
- **Events:** `{"type": "event"}` notifications, never payloads:
  - `conversation_changed`
  - `inbox_changed`
  - `stage_changed`, which carries the recipient's session, including when a
    reconnect moves a pending delivery back to `accepted`
  - `presence_changed`
- **Resync:** every event carries `{epoch, revision}`, and the revision
  increases by one per event. Events start only after the first
  `operator_snapshot`, a bounded view taken in the same hub step as the
  baseline. A gap, a new epoch or `resync_required` (sent on queue overflow)
  means: take a new snapshot.
- **Flow control:** at most 256 queued events. A session that can't be written
  within the write deadline is closed and releases ownership. An idle session
  never expires.

`pairtask operator chat` is the interactive client of this session; see
[secured runtime](secured-runtime.md#interactive-chat).

## Operator history

`operator_history {page_request: {before_seq, upper_seq, limit}}` returns the
operator's conversation newest first: messages the operator sent, and messages
in the operator inbox, including acknowledged ones. It works one-shot and
inside a session, and it's read-only: no claim, no receipts, no
acknowledgment.

- **Paging:** `upper_seq: 0` starts a scan bounded by the highest committed
  message; echo the returned `upper_seq` and pass `next_before_seq` as
  `before_seq` to read older entries. `limit` is 1 to 32 (default 16).
- **Entries:** each carries the full message, its direction (`sent` or
  `received`) and the original `sender_session` as provenance.
  - Sent entries add current recipient stages (at most 16, then
    `receipts_truncated`; page more with `operator_message_stages`, which also
    works one-shot).
  - Received entries add the operator's own `operator_stage`.
- **Payloads are never truncated.** New operator sends are capped at 512 KiB
  of encoded payload. Older oversized operator messages appear with
  `body_omitted: true`, and their text isn't available through history. A
  message stored without any payload shows payload `null`.
- **Audit:** every history read is audited without payloads; if the audit
  fails, nothing is returned.

`pairtask operator history [--before SEQ] [--upper SEQ] [--limit N] [--json]`
prints one page and the continuation flags.

Test interactively with `nc -U /tmp/collab-ai.sock`.

Frames are limited to 1 MiB including the newline. A reader whose 64-frame outgoing
queue fills is disconnected so it cannot block other agents. Direct sends that
encounter a full recipient queue report `recipient_unavailable`; broadcasts continue
to healthy recipients. Socket writes time out after five seconds. Disconnecting a
slow reader can discard in-memory frames; unacknowledged durable messages remain
pending for replay. Other frames are not recoverable.

## Agent status (#133)

An agent tells the operator what it is doing with one small request at each
change, never as a heartbeat. Status is informational: it is not a message,
not an acknowledgment, not evidence of completion and never an authorization.

- `agent_status_set` `{request_id, agent_status: {state, task, step,
  waiting_on, ref}}` from a registered agent session. `state` is one of
  `active`, `waiting`, `blocked`, `idle`, `done`; `waiting_on` is empty or one
  of `operator`, `peer`, `ci`, `tools`; `task`, `step` and `ref` are at most 120
  UTF-8 bytes each, without control characters. The broker answers
  `agent_status_result` `{request_id, agent_status: record}` or an error with
  the same `request_id`: `invalid_status` (nothing stored), `status_rate_limited`
  (retryable; a token bucket per agent, burst 10, one token per 2 s; a status
  is never dropped silently), or `not_owner` for a superseded session. No
  sequence number, inbox entry or acknowledgment is created.
- `agent_status_get` `{request_id, agent_id?}` (default: the caller) answers
  `agent_status_result` with the latest record, or with a null record for an
  agent that never declared one or is unknown; no state is inferred.
- `agent_activity` `{source}` comes from the agent's launcher or launch
  hooks, not the model: `source` names where it was observed
  (`codex-launcher`, `claude-adapter`; any other source is rejected). It is accepted only from the session that
  owns the agent ID; `activity.at` is the broker's arrival time (a client time
  is ignored); the first report is applied at once, later ones are coalesced
  to one update per 30 s. Activity belongs to the session that reported it:
  a new session's activity never makes an older session's declaration look
  current, and presence still shows the agent connected while its retained
  record says `connected: false`. Where no
  exact-session source exists the activity is unavailable; the broker never
  infers activity from other sessions' files.
- A record is `{agent_id, session_id, state, task, step, waiting_on, ref,
  declared_at, activity: {at, source} | null, connected}`. Identity, session and
  timestamps are set by the broker. The latest declared record is kept per
  agent ID (SQLite `agent_status`) with its originating session; after a
  restart or reconnect it is served with `connected: false` until the owning
  session declares again, so an old status is never shown as fresh.
- Operator sessions get the event `status_changed` `{agent_id}` on the
  existing watermark, and `operator_snapshot` carries `agent_status`
  (connected agents first, then retained offline ones, at most 64) with
  `agent_status_total` and `agent_status_truncated`. Viewers render ages from
  `declared_at` and `activity.at`; no activity for a while is shown as an age,
  never turned into `blocked`.

## State

SQLite retains `agents` (session lifecycle and harness/model) and `messages`
(history), plus `message_metadata` (stable IDs, reply references, sender session,
and acknowledgment request) and `message_receipts` (recipient/session membership
and receipt timestamps). Opening an existing database adds the new tables and
index without rewriting history. Pre-upgrade messages have no new metadata or
receipts and cannot be acknowledged retroactively. The additive v3 tables `durable_agents`, `durable_messages`, and `durable_inbox`
retain explicitly enrolled pending work and idempotent retry state. Version 4
adds the nullable columns `message_metadata.origin` and
`durable_agents.protocol_version` to existing databases, and the tables
`operator_requests` (canonical scope per operator message) and
`operator_audit`. Legacy rows keep NULL, which reads as unknown. Pending and
retained durable storage have [bounded capacity](durable-inboxes.md); no
automatic expiry or eviction is performed.
Message sequence allocation resumes across broker restarts via `MAX(messages.seq)`.
Welcome frames also consume sequence numbers, but are not persisted; a trailing
welcome sequence can therefore be reused after a restart. Do not use welcome
sequences as durable replay cursors.
