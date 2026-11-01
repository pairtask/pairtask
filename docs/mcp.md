# Manual MCP setup and messaging reference

Use [managed Codex / Claude channels](host-integration.md) for automatic delivery.
**Manual MCP needs inbox checks and does not wake idle conversations.**

Start one broker and one adapter per agent. Default initialization/discovery is
passive; call `receive` once to register before another agent sends to you.
Channel `--auto-listen` is different: reserve that configuration for its owner.

Each logical `agent_id` (1–128 bytes, except `*`) has one owning `session_id`.
A duplicate returns `duplicate_id` / `owner_session_id` without displacing it.
Close the owner to transfer the inbox; use distinct IDs for simultaneous agents.
A failed initial registration can retry. After an established disconnect, restart
with the same ID for durable recovery; other in-memory frames are lost.

For a child listener, use [delegation](delegated-listeners.md), not duplicate
registration. For working conventions, install the [skill](skills/collab-ai/SKILL.md)
with its `references/` directory. [Acceptance cases](skill-validation.md).

## Register an adapter

Replace `/absolute/path/to/pairtask/pairtask-mcp` below with the absolute path to
your built MCP executable. The socket path must match the broker's
`PAIRTASK_SOCKET_PATH`; these examples use `/tmp/collab-ai.sock` as in the [quick start](../README.md#run-on-your-host).

Codex:

```sh
codex mcp add pairtask -- /absolute/path/to/pairtask/pairtask-mcp \
  --agent-id codex-1 --harness codex --socket /tmp/collab-ai.sock
```

Claude Code:

```sh
claude mcp add --transport stdio pairtask -- /absolute/path/to/pairtask/pairtask-mcp \
  --agent-id claude-1 --harness claude-code --socket /tmp/collab-ai.sock
```

`pairtask` is the MCP server name. Claude shows the tools as `mcp__pairtask__*`
and peer messages as channel source `pairtask`; any name works as long as
Claude's `--dangerously-load-development-channels server:NAME` matches it. The
executable after `--` becomes `command`,
and its flags become `args`. In Claude's configuration, the entry inside
`mcpServers` should look like this:

```json
{
  "pairtask": {
    "type": "stdio",
    "command": "/absolute/path/to/pairtask/pairtask-mcp",
    "args": [
      "--agent-id", "claude-1",
      "--harness", "claude-code",
      "--socket", "/tmp/collab-ai.sock"
    ]
  }
}
```

## Troubleshooting

If the server does not connect, the error names which half is wrong:

- `ENOENT: Executable not found in $PATH: pairtask` — `command` holds the server
  name rather than the binary, and the binary path has been placed in `args`.
  The executable belongs in `command`.
- A messaging tool returns `connect to broker: dial unix <path>: no such file or
  directory` — no broker is listening on that path. Discovery can still succeed.
  Start the broker, or match `--socket` to its `PAIRTASK_SOCKET_PATH`. A socket
  file left behind by a crashed broker looks the same as a live one to `ls`;
  connecting to it is the only way to tell.

If the server appears under several project entries in `~/.claude.json`, fix
each one.

Running the adapter by hand to test it exits immediately with `server is
closing: EOF`, because the pipe closes its stdin. That is the end of an MCP
session, not a fault; hold stdin open to see it answer.

For a different socket, replace the `--socket` value in both configurations with
the broker's absolute socket path. `PAIRTASK_SOCKET_PATH` also sets the default when
`--socket` is omitted. Optional `--model` records
the model name with the agent session. Start the broker before the first messaging
tool call. A failed initial connection can be retried with another tool call.
The adapter reserves stdout for MCP protocol frames and writes diagnostics to stderr.

These commands follow the official [Codex MCP documentation](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
and [Claude Code MCP documentation](https://code.claude.com/docs/en/mcp).

## Messaging tools

| Tool | Arguments | Behavior |
|------|-----------|----------|
| `send` | `to`, `text`, optional `message_id`, `in_reply_to`, `non_durable`, `workspace` | Writes a direct message or broadcast (`to: "*"`); returns its ID for tracking. With `workspace` (its full key, or a name exactly one paired workspace has: its `local_name` or the name it gives itself), `to` is an agent of that [paired workspace](pairing.md#7-let-agents-talk-across) or `operator` for its operator. |
| `workspaces` | none | Lists the paired workspaces this agent is shared with, the only ones it may write to: name it gives itself, `local_name` (what this side's operator calls it, when set), key, whether connected, and the agents each says it shares. |
| `reviews_asked` | none | Lists the review requests this agent sent paired workspaces, newest first, with where each stands, its verdict and published link. |
| `request_review` | `workspace`, `to`, `target`, `base`, `head`, optional `scope`, `publish`, `formal`, `message_id` | Asks an agent of a paired workspace to review a pull request pinned to `base` and `head`; that workspace's operator decides whether it may, and what is published. |
| `acknowledge` | `message_id` | Writes an explicit agent acknowledgment; its persisted confirmation arrives through `receive`/`wait`. |
| `receive` | optional `limit` (default 20, maximum 100) | Consumes queued messages and broker errors immediately. |
| `wait` | optional `timeout_seconds` (default 30, maximum 30), `limit` | Consumes queued frames or waits for the next arrival. |
| `wait_reply` | `message_id`, `from`, optional `timeout_seconds` (default 30, maximum 30) | Consumes one correlated reply or broker error; leaves unrelated frames and receipts queued. |
| `delegate_listener` | none | Manual adapter: grants one listener read access for 15 minutes; replaces any previous grant. |
| `wait_delegated` | `socket_path`, `token`, optional `after_cursor`, `timeout_seconds` (default 25), `limit` | Reads retained parent frames without consuming or acknowledging them; never registers the child's adapter. |
| `revoke_listener` | none | Revokes this adapter's grant and cancels its outstanding wait; keeps the parent connected. |
| `status` | `state`, optional `task`, `step`, `waiting_on`, `ref` | Declares what the agent is doing so the operator sees it without reading its terminal; replaces its previous declaration. `state` and `waiting_on` take the [values below](#status-values); `task`, `step` and `ref` are at most 120 bytes each. |
| `read_status` | optional `agent_id` | Reads an agent's latest declaration, by default its own; `null` when it declared none. Nothing is inferred. |

Example collaboration: Codex first calls `receive({})` to register.
Claude then calls `send({"to":"codex-1","text":"Please review my changes"})`;
Codex calls `wait({"timeout_seconds":30})` and receives the message. After
considering it in the active conversation, Codex calls
`acknowledge({"message_id":"<request ID>"})` and can reply using
`send({"to":"claude-1","text":"Review findings…","in_reply_to":"<request ID>"})`.
Claude can call `wait_reply({"message_id":"<request ID>","from":"codex-1"})`
for that specific answer, then explicitly acknowledge the returned reply's own
message ID after considering it. A timeout does not mean the request failed;
waiting again does not resend it. Continue `receive` checks for receipts and other
conversations. See [correlated replies](correlated-replies.md) for outcomes,
concurrent readers, replay, and host-mode behavior. `receive`
and `wait` return `messages`, `connected`, and an `error` if disconnected. An empty
wait that reaches its deadline also returns `timed_out: true`. These tools consume
frames, so repeated calls do not return the same message. Concurrent consumers
share one inbox; each frame goes to only one call.

Check `receive` between work steps, use `wait_reply` for a specific request, and
use `wait` for general arrivals. Waiting
blocks on socket arrivals rather than querying SQLite repeatedly. This manual mode
does not push input into a model's conversation or wake an idle agent after its
turn ends; unattended collaboration still needs session orchestration.

### Status values

The `status` tool's schema lists these values, and the broker refuses any
other with `invalid_status`, naming the allowed ones.

<!-- status-states:start -->
| State | Meaning |
| --- | --- |
| `active` | Working on the task |
| `waiting` | Waiting on someone or something; `waiting_on` says on what |
| `blocked` | Can't continue without help |
| `idle` | Between tasks |
| `done` | Finished the task |
<!-- status-states:end -->

<!-- status-waiting:start -->
| waiting_on | Meaning |
| --- | --- |
| `""` | Not waiting |
| `operator` | The operator |
| `peer` | Another agent |
| `ci` | Continuous integration |
| `tools` | A tool or a long-running command |
<!-- status-waiting:end -->

## Delivery and acknowledgments

`send` returns `status: "written"`, `message_id`, and
`delivery_confirmed: false`. `receive` and `wait` include correlated `ack` and
`error` frames alongside messages:

| Stage | What the broker can confirm |
|-------|-----------------------------|
| `accepted` | SQLite committed the message, reply correlation, and recipient/session membership atomically. No recipient receipt is implied. |
| `adapter_received` | The intended recipient session reported buffering the message; the broker committed that receipt. This does not mean the model has seen it. |
| `agent_acknowledged` | The recipient explicitly called `acknowledge`, and the broker committed it. This does not assert understanding or task completion. |

The adapter automatically reports receipt after decoding and checking inbox
capacity, before exposing a message to inbox consumers. It never automatically
acknowledges on behalf of the agent. `acknowledge` itself confirms only a write;
the subsequent `agent_acknowledged` event confirms persistence to both the
recipient and the original sender session. Repeating a receipt is idempotent.
For non-durable messages, only the session selected at acceptance can acknowledge.
Durable messages can be recovered and acknowledged by the current logical inbox
owner after that owner reports its own adapter receipt.

Errors carry the original `message_id`. Unknown direct recipients are rejected
before persistence. Durable sends can target offline logical IDs previously
registered with v3; online recipients must support durability. A full outgoing queue reports `recipient_unavailable`; a
recipient disconnect before explicit acknowledgment reports
`recipient_disconnected` to the original sender session, even if adapter receipt
was already confirmed. A broker disconnect, write failure, or wait timeout leaves
missing stages **unconfirmed**. `timed_out` describes an empty wait, not message
expiry or successful delivery. Check each message ID independently.

Durable messages remain pending until explicit agent acknowledgment and replay
on v3 registration after restart. Legacy history is never enrolled for replay.
Connection loss is reported without automatic reconnection; restart the adapter
with the same logical ID to recover. See [limits and retries](durable-inboxes.md). Buffered frames remain available until that
restart. The inbox is bounded to 256 frames and 4 MiB of encoded frame data;
overflow closes the connection and reports that messages may be missing. Receipt
events count toward these limits, so manual-mode senders must also drain their
inboxes. Host listeners consume the broker inbox continuously, release fallback
copies after confirmed acknowledgment, and evict receipt history under pressure;
see [host queue maintenance](host-integration.md#status-fallback-and-shutdown).
