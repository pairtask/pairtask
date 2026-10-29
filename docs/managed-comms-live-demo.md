# Automatic terminal communication, 2026-09-23

The normal **Codex CLI 0.156.1** terminal UI, launched through
`collab-codex --terminal`, received, explicitly acknowledged, and replied to
fixture messages without calling `listen`, `receive`, or a waiting tool. The
same broker session survived a real `/compact` and delivery during a shell job.

**Claude Code 2.1.280** registered automatically with `--claude-channel
--auto-listen` and received the broker frame at its adapter. The host reported
“Channels are not enabled for your org” and did not acknowledge it. This run
therefore does **not** establish working two-host push delivery. The applicable
organization must enable channels before that acceptance criterion can pass.
The development flag does not override that policy.

## Setup and identities

The test used temporary binaries, an isolated UDS socket/SQLite database, and
fresh logical inbox IDs. Codex ran with a read-only sandbox and the operator's
normal terminal UI. A fixture peer sent controlled test messages and observed
broker confirmations; it never acknowledged on behalf of either model.

| Component | Identity |
| --- | --- |
| Codex thread | `01a0ce6f-6612-72a0-adb7-c648e3a46920` |
| Codex logical inbox | `codex-managed-test` |
| Codex broker session | `d7445da1-c963-4165-a919-55481200824b` |
| Claude logical inbox | `claude-managed-test` |
| Claude broker session | `ff83db79-b094-4900-96c9-92468812ddfa` |

All times below are UTC. Receipt and acknowledgment timestamps were checked
against SQLite `message_receipts`, not inferred from host submission responses.

| Case / message ID | Adapter receipt | Explicit acknowledgment | Correlated reply ID |
| --- | --- | --- | --- |
| Idle: `auto-cli-idle` | 13:23:37.431719 | 13:23:44.591569 | `0c4feec1-41a5-453d-a3bc-0ae2c56b0f27` |
| After compaction: `auto-cli-after-compact` | 13:26:01.911496 | 13:26:06.879145 | `c4a7c17f-83b7-472d-9b6d-c62b4005b2d6` |
| Active tool: `auto-cli-active` | 13:28:45.569069 | 13:28:54.213265 | `0ece6375-8165-4bdd-9c2a-1002e0057556` |
| Claude, policy blocked: `auto-claude-idle` | 13:25:31.681181 | None observed | None observed |

Codex's rollout records compaction at **13:25:28.660**. No listener registration
or renewal followed it. For active delivery, the model started `sleep 30` at
**13:28:36.480**. A `write_stdin` call finished its intermediate poll at
**13:28:45.600**; the incoming message appeared as external `collab_receive`
function output at **13:28:45.606**. The model explicitly called
`collab_acknowledge` and `collab_send` before the shell job finished at
**13:29:06.555**. The external output was injected by the runtime; the model did
not call `collab_receive`.

The rollout's tool executions were checked for acknowledgment/send calls and
the bounded shell test, with no listen/receive/wait polling. Model acknowledgment
still means the message was considered, not that an arbitrary peer task finished.

## Startup regression found by the real terminal

The installed CLI requests `plugin/list` during startup. Its response was
**10,451,619 bytes**, exceeding the old proxy's 8 MiB scanner limit and closing
the host connection. The host-frame limit is now bounded at 32 MiB, with a
regression test for a 10 MiB inventory. The broker's 1 MiB peer-message limit
was not changed. The launcher uses WebSocket framing over a private Unix socket;
no TCP broker transport was added.

## Reproduction and limits

1. Build the three binaries and start an isolated broker with a temporary
   socket and database. Follow [host setup](host-integration.md).
2. Launch `collab-codex --terminal` with a unique agent ID. Do not call `listen`.
   Verify its broker session appears after thread creation, before a model call.
3. Give the model a bounded communication-test instruction: explicitly
   acknowledge test messages and reply with `in_reply_to`, without calling
   listen/receive/wait or changing files.
4. Send a durable fixture message while idle. Check the persisted acknowledgment
   and correlated reply. Run `/compact`, then repeat with another stable ID.
5. Run a bounded shell job, send a message while it is still running, and compare
   host exposure/acknowledgment timestamps with the job interval.
6. For Claude, use a dedicated auto-listen configuration and the interactive
   channel opt-in. Check the host's channel diagnostic before interpreting
   adapter receipt as delivery. If policy blocks channels, record that result;
   do not bypass it or claim successful model exposure.

Automated tests additionally cover 522 consecutive deliveries and explicit
acknowledgments without listener receive calls, receipt-history eviction,
protected fallback frames, selective reply reads, startup collisions, passive
default discovery, cancellation, and private terminal socket shutdown.

This test does not establish indefinite unattended operation, automatic recovery
from an established disconnect, or attachment to an already-running Codex
conversation. The supported launcher owns one new thread. Full Claude/Codex live
acceptance remains open.
