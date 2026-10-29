# Offline budget boundary proof

This **experimental test setup** creates two disposable offline Incus
containers (`secured` and `dev`) to verify budget supervision and tool-routing
boundaries without network interfaces, host credentials, or project mounts.

## Run

```sh
# Linux
sudo python3 scripts/sandbox-boundary.py --remote local --image-dir dist/installed
# macOS (with Colima running)
python3 scripts/sandbox-boundary.py --remote colima-pairtask --image-dir dist/installed
```

Pass `--bin-dir /absolute/path/to/bins` to overlay local Linux `pairtask` and
`pairtask-codex` binaries. CI runs this proof on both Linux architectures.

## What it proves and limits

- `dev` can read `/mnt/collab-status/BUDGET.sock` but cannot replace the
  socket, spoof admin identity, or modify caps.
- `pairtask-codex --restricted-operator` stops a fake client at 107 reported
  tokens against a cap of 100 (even with stdin open); exhaustion persists
  across container restarts and blocks relaunch.
- Killing the supervisor with `SIGKILL` terminates its systemd control group
  (including `setsid` descendants ignoring `SIGTERM`) and blocks budget reuse.
- Native Codex (`environments.toml`, `include_local = false`) and Claude
  (`--restricted --tools "" --strict-mcp-config`) route approved tools to `dev`
  and reject local execution in `secured`.
- **Limits:** uses deterministic loopback fake providers (`codex exec`, App
  Server, `claude -p`). Does not establish live subscription auth, interactive
  terminals, resume, shared Claude/Codex accounting, or zero token overshoot.

## Experimental restricted operator

`--restricted-operator` constrains the **stdio App Server protocol** (requires
`--budget NAME` and trusted execution outside `dev`):

| Method | Accepted parameters |
| --- | --- |
| `initialize` | Optional client metadata and `experimentalApi`; replaced with proxy-owned metadata/capabilities |
| `initialized` | Empty notification |
| `thread/start` | Empty object; native defaults and proxy-owned tools apply; approvals set to `never` |
| `turn/start` | Managed `threadId` and 1–16 literal text items (bounded to 64 KiB) |
| `turn/steer` | Same text input plus `expectedTurnId` |
| `turn/interrupt` | Managed `threadId` and `turnId` |

Unknown methods/fields, caller-supplied tool results, local images/skills,
config overrides, usage notifications, second threads, resume/fork, terminal
mode, and server approval/input requests are rejected (unexpected host approval
requests stop the session and mark a budget failure).

## Supervisor failure

Restricted sessions (budget file version 2) write an unfinished marker
**before** starting the native client:

- A crash, disconnect during work, interrupted turn, or missing usage report
  leaves `supervised_unfinished` (`session_unfinished: true`), refusing relaunch
  until operator reconciliation.
- Normal shutdown clears the marker only after stopping the child with all
  turns completed and usage recorded.
- Inside `secured`, `pairtask-supervised-codex BUDGET` runs
  `pairtask-codex-BUDGET.service` (`KillMode=control-group`, 3 s stop timeout,
  final `SIGKILL`, no auto-restart, status socket at
  `/mnt/collab-status/BUDGET.sock`; see [control runtime](secured-runtime.md)).

Source references: [Codex environment configuration](https://github.com/openai/codex/blob/rust-v0.156.1/codex-rs/exec-server/src/environment_toml.rs)
and [Claude CLI reference](https://code.claude.com/docs/en/cli-reference).
