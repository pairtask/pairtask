# Codex resume compatibility validation

Validated on macOS with installed Codex CLI **0.156.1**, 2026-09-24. Tests used
temporary Codex homes, fixture conversations, a local broker-protocol fixture,
and a local fake Responses endpoint. They did not use real model inference or
the operator's existing conversations.

## Observed behavior

| Case | Result |
| --- | --- |
| Resume an ordinary saved conversation through the proxy | Same thread ID and conversation history retained; seven `collab_runtime` tools available |
| Fork an ordinary saved conversation through the proxy | New thread ID, inherited history, and runtime tools available |
| Start through the proxy, exit, then resume | Same thread ID and history retained; fresh runtime MCP endpoint works |
| Broker ownership | Exactly one registration per managed launch; MCP discovery and tool calls do not create another owner |
| Peer delivery after resume/fork | Incoming peer output starts a turn without an operator prompt; submission does not implicitly acknowledge the message |
| Terminal `resume SESSION_ID` | Normal Codex UI renders the saved history and the broker registers one owner |
| Terminal `fork SESSION_ID` | Normal Codex UI renders inherited history and the broker registers one owner |
| Terminal shutdown | Launcher exits and its fixture processes terminate; the test must keep draining terminal output during shutdown |

The App Server checks also verified `read-only` sandbox and `never` approval
settings in the lifecycle responses. Unit tests cover preservation of caller
options, saved developer-instruction handling, failed-resume retry, single-thread
binding, exact argument forwarding, passive MCP discovery, readiness gating, and
MCP connection cleanup. Legacy dynamic-tool dispatch retains its existing tests.

## Upstream CLI restriction

Codex 0.156.1 rejects permission overrides such as `-s`/`--sandbox` and
`-a`/`--ask-for-approval` when resuming a remote task. The same rejection occurs
with Codex's native remote transport without this proxy. The successful terminal
resume used saved permissions and completed Codex's normal folder-trust prompt.
The launcher preserves these arguments and lets Codex report the restriction.

These checks establish the integration behavior for this installed version.
The fake model does not establish real model attention or human approval;
explicit acknowledgments and operator-controlled permissions still apply.

## Automated checks

```sh
go test -race ./... -timeout=30s
go vet ./...
```

The collaboration skill also passed the Skill Creator validator. Targeted race
tests were rerun after changes to the MCP readiness guard and test helpers.

## Managed resume

Codex's own resume picker (`resume` without an ID) and `resume --last` don't
resume through the managed launcher: the picker sends `thread/list` and
nothing else (observed with 0.159.3; the cause, likely a source filter
excluding App Server sessions, is unconfirmed). Instead of changing what the
proxy forwards, the launcher records the owned thread (its ID and directory,
from the successful lifecycle response) per agent ID, and `pairtask start
codex --id ID --resume` resumes it by ID, the path validated above. Explicit
native resume arguments still go to Codex unchanged.
