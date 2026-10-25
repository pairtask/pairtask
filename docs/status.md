# Broker status

For a live terminal view using this same read-only API, run `pairtask dashboard`.
See [dashboard setup and controls](dashboard.md).

Build the operator CLI alongside the broker:

```sh
go build -o pairtask ./cmd/collab
./pairtask status
./pairtask status --json
./pairtask status --socket /path/to/broker.sock --timeout 5s
```

The socket defaults to `PAIRTASK_SOCKET_PATH`, then `/tmp/collab-ai.sock`. The
client timeout defaults to three seconds and must be greater than zero and at
most 30 seconds. The broker allows up to 30 seconds for hub processing,
including waiting for the hub to handle the request. The client's deadline
still bounds its entire connection and response wait. No agent ID, model, MCP
adapter, or database path is needed.
The command sends one status request through the existing Unix socket and exits.
It never registers an agent, allocates a message sequence, consumes an inbox,
claims replay ownership, or acknowledges a message.

Exit status is **0** for a complete snapshot, **1** for unavailable, unsupported,
or degraded status (or an output failure), and **2** for invalid arguments.
`status --help` exits successfully. `--json` produces one JSON document on stdout,
including for operational failures; usage and output errors go to stderr.

## Observations and limits

`reachable` means the local socket connection succeeded. `health` distinguishes
`ready` (registry and storage queried), `degraded` (live registry available but
session history/pending counts unavailable), `unsupported` (older broker or
unsupported status schema), and `unavailable` (connection, timeout, shutdown, or
invalid response). A reachable socket alone does not establish broker readiness.

`snapshot_at` is the broker's UTC observation time, and `broker_started_at` is
the start of this hub instance. Registration, routing, acknowledgments, and
snapshots are serialized by the hub, so the snapshot reflects one point in its
event processing. Socket arrivals still waiting to be processed are not included.
The response is a snapshot, not a heartbeat or a live subscription. Status
database reads have a one-second budget so a failed query cannot indefinitely
block routing. Storage failures retain live registry information and expose
unavailable counts, with an explanatory error. Requests that cannot reach a
snapshot report the hub failure cause, such as timeout or shutdown.

`connected_sessions` is the total number of current transport owners, including
ones omitted by the display limit. Session rows use these states:

| State | Meaning |
| --- | --- |
| `transport_connected` | This session currently owns the logical inbox in this broker's registry. |
| `disconnected` | A historical session with a recorded disconnect timestamp. |
| `stale` | No current owner matches this historical session, and no disconnect timestamp was recorded, for example after a crash. |

`connected_at` and `disconnected_at` come from the session lifecycle. Live
`last_seen_at` is registration or the most recent inbound frame processed by the
hub, including adapter receipts and rejected messages. It does not track model
activity or prove the peer read anything. Historical last-seen times and
disconnect reasons are not persisted, so they are `null`/`unavailable`; a stale
session's disconnect time is not guessed. Inspection never edits stale records.

The response contains at most **100 sessions**: live owners first, sorted by
logical ID, then newest retained historical sessions. `sessions_truncated`
indicates omitted rows; `history_available` indicates whether storage could be
read. Session records have no automatic expiry and remain until operator-managed
database maintenance. The display limit is not a history-retention guarantee.
Do not infer that an omitted identity never connected or is currently offline;
consult the truncation indicator and connected total.

## Pending delivery

`durable_pending.total` counts retained **recipient deliveries** whose explicit
agent acknowledgment has not committed. A broadcast pending for three recipients
counts as three. Offline recipients are included. Adapter receipt, delegated
listening, and successful host notification do not reduce this count. Only a
committed `agent_acknowledged` does. These counts use the same
[durable inbox contract](durable-inboxes.md) as replay and idempotent retries.

`durable_pending.recipients` lists logical IDs with nonzero pending counts,
sorted by ID, with at most **100 recipients**. `recipients_truncated` marks
omissions; the total always covers all durable pending deliveries. Pending work
belongs to the logical inbox, so it is not duplicated across that agent's
historical session rows. `legacy_unacknowledged` is always `null`: this command
does not report legacy/non-durable delivery counts, and pre-upgrade untracked
history cannot establish explicit acknowledgment state. A failed durable query
returns `durable_pending: null`, never a fabricated zero.

## JSON contract

The JSON schema has `schema_version: 3`, separate from the broker messaging
`protocol_version`. New optional fields may be added; consumers should ignore
unknown fields. Unknown metrics/timestamps are `null`, available zero counts are
numeric zero, and empty lists are `[]`. On a failed connection `protocol_version`
is zero because no version was observed. A successful empty broker returns:

```json
{
  "schema_version": 3,
  "reachable": true,
  "health": "ready",
  "snapshot_at": "2026-09-22T12:00:00Z",
  "broker_started_at": "2026-09-22T11:00:00Z",
  "protocol_version": 3,
  "connected_sessions": 0,
  "sessions": [],
  "session_limit": 100,
  "sessions_truncated": false,
  "history_available": true,
  "durable_pending": {
    "total": 0,
    "recipients": [],
    "recipient_limit": 100,
    "recipients_truncated": false
  },
  "legacy_unacknowledged": null,
  "agent_status": [],
  "agent_status_total": 0,
  "agent_status_truncated": false,
  "agent_status_available": true,
  "claims": [],
  "claims_total": 0,
  "claims_truncated": false,
  "claims_available": true
}
```

Session objects contain `agent_id`, `session_id`, `state`, `connected_at`,
`disconnected_at`, `last_seen_at`, and `disconnect_reason`. Pending recipient
objects contain `agent_id` and `pending`. Errors add a human-readable `error`;
scripts should use `health` and exit status rather than matching that prose.
The CLI quotes identifiers in human output so control characters cannot alter
the terminal display.

## Agent status (schema 2)

`agent_status` holds the agents' latest declarations (#133), the same records
and order as the operator chat's snapshot: connected agents' records first,
then retained offline ones by agent ID, at most **64**. `agent_status_total`
counts every retained record, so `agent_status_truncated` marks an omission.
Each record has `agent_id`, `session_id`, `state`, `task`, `step`,
`waiting_on`, `ref`, `declared_at`, `activity` (`{at, source}` observed for the
declaring session, or `null`) and `connected` (the declaring session is the
agent's current one). A record from an earlier session stays as declared: its
state is never inferred from its age or from a disconnect.

`agent_status_available: false` means the declarations are unknown, never that
there are none: a failed read (health `degraded`) or a schema 1 broker. The
client accepts both schemas; a schema 1 response keeps `schema_version: 1` and
reports `agent_status_available: false`. Any other schema is `unsupported`.

The text output adds an **Agent status** table with declared and activity ages,
`current` or `previous` session, and a 12-character short form of a full
commit `ref` (JSON keeps the full value). A connected agent without a record is
listed as `no status declared` only when the list is complete. The dashboard
shows the declaration on the session row that declared it, or as `previous:`
on the agent's live row; other historical rows show `—`, and an agent without
a record shows `no status`, or `unknown` when the list is truncated or
unavailable.

## Claims (schema 3)

`claims` holds the task claims (#141) of the newest claimed operator messages,
as the operator chat's snapshot has them: at most **32** messages, newest claim
first, each a set with `operator_message_id`, `claims` (at most 16, held ones
first), `claims_total` and `claims_truncated`. The top-level `claims_total`
counts every claimed message, so `claims_truncated` marks omitted messages.
`claims_available: false` means unknown, never none: a failed read (health
`degraded`, independently of agent status) or a broker below schema 3. The
client reads schemas 1 to 3 and clears only what an older schema lacks: a
schema 2 broker keeps its declarations and reports claims unavailable. The text
output adds a **Claims** table; the dashboard shows `Active claims N` (with `+`
when either level is truncated, or `Claims unavailable`) and the selected
agent's active and done claims in its details. A claim coordinates agents; it
is never an approval.

## Wire and compatibility

The new broker recognizes this as the first newline-delimited frame:

```json
{"type":"status"}
```

It returns `{"type":"status","status":{...}}` and closes the connection.
Additional fields or pipelined frames cannot register an agent. Status is not
accepted as a messaging-session command after `hello`. The existing one-MiB
frame limit and socket permissions apply. There is no new port, HTTP endpoint,
access token, message-body flag, or direct database reader. Identities and
aggregate counts remain within the existing local broker access boundary.

Older brokers reject the first frame with `expected_hello`; the CLI reports
`unsupported` and leaves counts unavailable. It never falls back to registering
an inspection agent. Upgrading the broker adds status while keeping messaging
protocol v3 unchanged. Opening an existing database adds an index for bounded
recent-session queries; it does not rewrite session history or enroll legacy
messages in durable delivery.

`go test -race ./... -timeout=30s` covers absent/unresponsive/legacy brokers,
empty and multiple sessions, stale sessions after a real broker crash,
disconnects, pending/receipt/acknowledgment boundaries, storage failures,
bounded history and recipient lists, text/JSON output, cancellation, and
non-interference with ownership, replay, inbox frames, and message sequences.
