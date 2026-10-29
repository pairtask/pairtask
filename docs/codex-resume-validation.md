# Codex resume compatibility validation

Validated on macOS with **Codex CLI 0.156.1** (2026-09-24) using temporary
Codex homes, fixture conversations, a local broker fixture, and a fake
Responses endpoint.

| Case | Result |
| --- | --- |
| Resume or fork saved conversation via proxy | Retains/inherits history; `collab_runtime` tools available; registers one broker owner |
| Start via proxy, exit, then resume | Same thread ID and history retained; fresh runtime MCP endpoint works |
| Peer delivery after resume/fork | Starts a turn without operator prompt; submission does not implicitly acknowledge |
| Terminal `resume SESSION_ID` / `fork SESSION_ID` | Normal Codex UI renders history and registers one broker owner |
| Permission overrides on remote resume | Codex 0.156.1 rejects `-s`/`--sandbox` and `-a`/`--ask-for-approval` on remote resume (upstream restriction); saved permissions work |

## Managed `--resume` (2026-10-25)

Codex's interactive picker (`resume` without an ID) and `resume --last` only
send `thread/list` and do not resume App Server sessions (observed on 0.159.3).
Instead, the launcher records each agent ID's owned thread ID and working
directory on start/resume/fork, and `pairtask start codex --id ID --resume`
resumes that recorded thread by ID. Explicit native resume arguments still pass
through unchanged.
