# Operator dashboard

A read-only terminal view of agent connections and pending messages.
Prompts and approvals stay in agent terminals. For containers, use the
[Incus browser UI](sandbox.md#incus-web-ui).

## Open the dashboard

With the secured runtime enabled, the dashboard runs in `workspace` and queries
the broker in `secured` through the shared socket. From a sandbox shell, run
`dashboard`. Or from your host checkout:

```sh
ssh -t -F infra/incus/ssh/config workspace pairtask dashboard
```

For a host broker (after the [host setup](quickstart.md)):

```sh
./pairtask dashboard --socket /tmp/collab-ai.sock
```

`q` closes only the dashboard. It needs an interactive terminal; scripts should
use `pairtask status --json`. `NO_COLOR=1` disables color.

## Controls

| Key | Action |
| --- | --- |
| Up / Down, `j` / `k` | Select a row; scroll full details or help |
| Page Up / Page Down | Move a page |
| Enter | Open or close full details |
| Tab | Switch between sessions and pending inboxes |
| `/` | Filter by agent ID; Enter or Escape finishes editing |
| `s` | Cycle session states: all, connected, disconnected, stale |
| Escape | Close details/help and clear the text filter |
| `r` | Refresh now, if no request is running |
| `?` | Open or close help |
| `q` / Ctrl+C | Quit the dashboard |

Enter opens full details on narrow screens. Filters and selection survive refreshes.

## What the screen means

| Display | Meaning |
| --- | --- |
| Connected | Broker connection exists; does not prove the model is listening |
| Pending | Recipient deliveries awaiting explicit acknowledgment, including offline agents |
| Stale session | Historical owner without a recorded disconnect |
| **STALE** snapshot | Retained or old observations; check the timestamp/error |
| Unavailable / truncated | Missing information; do not interpret it as zero |
| Agent status | The declaration on the session that made it; `previous:` on the agent's live row for an earlier session's |
| Active claims N | Active task claims in the loaded messages; `+` when the snapshot is truncated; details list the selected agent's active and done claims |

Refresh defaults to two seconds after each request, with a three-second timeout.
Override with `--interval 2s --timeout 3s` (interval 1s–1m, timeout >0–30s).
Only one request runs at a time; refresh continues after failure.
[Status field definitions](status.md).

## Agent terminals and token caps

No token accounting or cap controls are built into the dashboard yet.
Use [named Codex budgets](host-integration.md#codex-soft-cap). Claude/shared budgets
are not supported. Incus resource limits do not limit model tokens.

## Read-only boundary

The dashboard never reads message bodies or SQLite, registers an agent, consumes
messages or acknowledges delivery. It escapes peer-supplied text and restores the
terminal on exit. It does not stop the broker or agents.

<details>
<summary>Validation record</summary>

## Recorded validation

A PTY smoke test on macOS with Go 1.25.3 (2026-09-24) used an isolated broker,
two connected fixture agents, one disconnected agent, and two pending deliveries
including one offline recipient. At 80×24 and 120×32, the roster, selected-session
details, pending-inbox filter, and help were exercised. Pausing the test broker
produced a timeout and a STALE retained snapshot; resuming it restored ready
observations automatically. Both `q` and Ctrl+C exited successfully and restored
the alternate screen and cursor. A subsequent status request still reported the
same two owners and two pending deliveries. The fixture was then stopped.

The full race suite and vet passed. Automated tests cover cancellation and obsolete
responses, timeout/recovery, selection and filtering, null/truncated observations,
terminal sizes, escaping, and real-broker non-interference. No live user inbox was
used for this validation.

</details>
