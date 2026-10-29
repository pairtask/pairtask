# Durable recipient inboxes

Protocol version 3 adds opt-in durable delivery. MCP `send` requests it by default.
The broker commits the message, original recipient list, and durable inbox rows
in one SQLite transaction **before** returning `accepted`. SQLite uses synchronous
FULL. A failed commit reports an error and cannot confirm acceptance.

```json
{"type":"hello","protocol_version":3,"agent_id":"codex-1"}
{"type":"msg","durable":true,"ack_requested":true,"message_id":"review-42","to":"claude-1","payload":{"text":"Review cancellation"}}
```

`durable_requested` in the MCP send result only describes the attempted write.
The correlated `accepted` event must contain `durable: true` to confirm durable
acceptance. Inbox responses and `listener_status` expose `durability_supported`.
An older broker requires an upgrade or an explicit `non_durable: true` send.

## Ownership and replay

One active session owns each logical agent ID. Duplicate registration remains
rejected and cannot change recovery ownership. A successful v3 registration
atomically claims all pending messages for that logical recipient before they
are delivered, in original sequence order, after `welcome`. Recovery uses the
same logical ID and a new broker-assigned session ID. It never adds recipients.

Recovered messages retain their original `message_id`, sequence, sender/session,
payload, and `in_reply_to`, and carry `replayed: true`. This flag means delivery on
a replacement connection; it does not prove an earlier host saw the message.
Even the first actual delivery can be marked replayed after offline acceptance.

Each new owner must report its own adapter receipt before explicitly acknowledging
pending work. An old owner's adapter receipt does not authorize a new owner's
agent acknowledgment. The hub validates the current connection identity; callers
cannot supply a different agent or session to acquire another inbox.

Only a committed `agent_acknowledged` removes a message from **pending** state.
Retained history and duplicate-detection records remain. Socket writes, adapter
receipts, MCP `receive`, and host notification submission never clear pending work.

If an acknowledgment request fails before commit, the message replays. If commit
succeeds but its confirmation is lost, it does not replay; the current logical
owner can safely retry `acknowledge(message_id)` and receive confirmation of the
already committed state. Failed acknowledgment transactions roll back both the
durable inbox and history receipt updates.

Delivery is at least once until agent acknowledgment, conditional on a capable
owner reconnecting and storage remaining available. Deduplicate stable IDs before
repeating actions. An acknowledgment means the agent considered the message; it
does not prove task completion or provide exactly-once execution.

## Offline recipients, broadcasts, and retries

- A direct durable send can target an offline agent only after that logical ID
  has successfully registered with v3. Unknown IDs are rejected before acceptance.
  Discovery-only MCP probes do not establish registration.
- An online legacy recipient causes `durability_unavailable`. It is not silently
  downgraded. Use explicit non-durable delivery or upgrade that recipient.
- A broadcast freezes all other connected recipients at acceptance, at most 256.
  Every member must support v3 for a durable broadcast to succeed. Offline agents
  and later arrivals are excluded. Empty membership means nobody was targeted.
- Repeating a durable send with the same ID, logical sender, target, payload, and
  `in_reply_to` returns the original acceptance with `repeated: true` and a
  `receipts` snapshot. It does not reroute, create new history, or add broadcast
  members. JSON whitespace is ignored in payload comparison; other encoded
  differences, including object key order, count as changed content.
- Changed content or a different logical sender under an accepted ID returns
  `duplicate_message`. Legacy/non-durable IDs retain duplicate-rejection behavior.
  Use a new ID only for a distinct message, not to retry uncertain delivery.

The retry snapshot reports the original recipient/session list separately from
each member's current retained stage and delivery/acknowledging session. Receipt
events are otherwise forwarded to the original sender session only. A replacement
sender can inspect retained progress by resending the identical request and ID;
ephemeral receipt/error frames themselves are not replayed.

## Bounds and retention

| Scope | Message/recipient entries | Encoded bytes |
| --- | --- | --- |
| Pending per logical inbox | 32 | 2 MiB |
| Pending across all inboxes | 4,096 recipient entries | 64 MiB |
| Retained durable history | 100,000 messages | 256 MiB charged storage |

Pending accounting charges a message to each recipient. It includes encoded
delivery metadata plus replay headroom. Retained accounting also charges 1 KiB
per recipient for identity and receipt records. These are bounds on logical
durable storage, not on SQLite pages, indexes, journals, session history, or
pre-existing/non-durable history. The filesystem can still run out of space;
storage errors are reported without claiming acceptance or acknowledgment.

There is **no automatic expiry or eviction**, including for acknowledged history.
Reaching a bound rejects new durable sends atomically with `inbox_full`, whose
detail names the bound (`<bound>: <count>/<limit> <unit>`); a refused send to
the operator also raises `send_refused` on the operator session. An
acknowledgment frees pending capacity; it does not free retained-history
capacity. The operator frees pending capacity in bulk with
`pairtask operator inbox ack` or the chat's `/ack all`, which acknowledge a
stable, explicitly bounded page (see `docs/operator-chat.md`, CLI) and never
receipt what the operator client hasn't adopted. `pairtask operator inbox
status` shows usage against the per-sender, operator-inbox and
retained-history bounds; the global pending bounds across all inboxes are
reported only when they refuse a send.
At the retained-history limit, operator-managed archival is required. Preserve
unfinished work and stop the broker before any database maintenance; starting a
fresh database resets registration and duplicate-detection scope. Message IDs
should remain globally unique across such deliberate resets.

The replay bounds fit within the existing 64-frame connection queue and 4 MiB
adapter inbox. Slow consumers still disconnect. Accepted durable messages remain
pending for the next owner; non-durable messages and ephemeral events can be lost.

## Host lifecycle and migration

Host integrations keep explicit activation and restart boundaries. They do not
silently reconnect, restart an exited model, or resume a completed task. On
adapter/broker/host failure, restart the adapter and activate the same logical ID.
A registration that races the previous owner's disconnect can report
`duplicate_id`; retry after that owner is released. Broker recovery failures
reject registration instead of reporting an empty, connected inbox.

Claude may replay into a newly started channel session. The Codex proxy still
creates one new managed thread; it replays peer context there and does not attach
to an arbitrary desktop conversation. Host submission remains unconfirmed until
an agent acknowledges. There is no change to host approvals or sandbox policy.

Migration adds `durable_agents`, `durable_messages`, and `durable_inbox`. Existing
messages/receipts remain history and are never silently enrolled for replay.
Versions 0–2 continue their existing non-durable protocol; version 2 still supports
staged acknowledgments. A v3 wire client must explicitly set both `durable: true`
and `ack_requested: true`; the MCP adapter supplies these by default.

Tests include SIGKILL of a child broker, recovery of both adapters from the same
database, lost acknowledgment confirmation, host failure, session changes,
recipient isolation, unchanged broadcast membership, retries, legacy migration,
capacity rejection, and transactional rollback. No network transport or general
scheduler is added.
