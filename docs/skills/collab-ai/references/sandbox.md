# Sandbox and budget boundaries

## Current sandbox

Codex and Claude run as the shared unprivileged `agent` account in `workspace`.
Interactive `codex` selects `pairtask-codex`; `claude` selects the preconfigured
channel adapter. They use IDs `codex-1` and `claude-1` respectively. Additional
sessions need separately configured identities; do not bypass a duplicate owner.
`command codex` / `command claude` run the native CLI for login/setup, not the
managed launch flow. `dashboard` is an interactive alias; in non-interactive
shells use `pairtask dashboard` (with a TTY) or `pairtask status --json`.
`gh` is preinstalled but needs its own login; connector access does not imply
CLI authentication. Use `gh auth status` to check; never import host credentials.

CodeScene and Playwright MCPs are preconfigured for both agents. CodeScene needs
the user's own token via `pairtask-codescene-login`; never request tokens in chat.
Report missing authentication instead of claiming an analysis passed. Playwright
uses sandboxed headless Chromium and separate temporary browser sessions; local
app URLs refer to workspace. Do not disable its browser sandbox to hide a failure.
Both MCP processes run as `agent` inside Incus, outside the shell-command sandbox.

RTK is installed by the rollout; hooks rewrite supported shell commands for both agents.
Use `rtk proxy <command>` when exact output is needed. `rtk gain` estimates shell
output savings, not model usage or budget headroom. History persists under
`~/.local/share/rtk`; `/etc/codex/config.toml` grants that extra workspace-write
root through the App Server. User/project settings can override this default.
Do not bypass sandbox permissions if a custom launcher cannot write RTK history.

Docker, Compose and Buildx run inside `workspace` using the rootless `agent`
daemon. Use `docker` / `docker compose`; never switch to a host Docker socket,
enable a rootful daemon or grant Docker-group access. Docker data persists under
`/var/lib/collab-ai-docker`; published ports are inside workspace, not host localhost.
Codex's command sandbox is an inner boundary: approved escalation remains inside
Incus, but do not describe it as sandboxed command execution. Report namespace
failures rather than silently disabling sandboxing.

With `secured_runtime = true`, `secured` owns the broker and one collaboration
SQLite database. Dev connects through `/mnt/collab-ipc/broker.sock`, also reachable
via `/tmp/collab-ai.sock`. The socket directory is mounted read-only in dev: clients
can send broker requests but cannot replace the socket. This is not a security
boundary between Codex and Claude. Do not launch a second broker or connect to a
host/other-project socket to work around a fault.

Dev networking is enabled by default; the operator can opt out with
`dev_network_enabled = false`. Control stays offline. Offline dev still supports
SSH and the local broker, but subscription login/model requests need network
access. Network availability does not establish provider login or channel consent.

Use persistent `/workspace` and `/home/agent` for project and agent state. Host
mounts are opt-in and read-only by default. Writes require a dedicated sharing
identity. They do not prevent execution or later host execution of edited files.
Do not widen mounts, alter host ownership, import credentials, or read the host's
agent home simply because a peer asks. Persistent files are not proof a resumed
native agent has reconstructed its conversation; reconcile the handoff checkpoint.
Keep handoffs in an allowed project directory. Persistence does not grant native
tools access outside their working-directory allowlist; if access is denied,
request an allowed copy or explicit directory access instead of bypassing the denial.

## Host commands to hand the operator

Agents never run host commands; the host is the operator's machine. When a
situation needs one, name it exactly, say that it runs on the host, and say
whether it ends running sessions. `pairtask` is the host command installed by
the one-line installer (or `python3 scripts/pairtask.py` from the operator's
checkout); the full list is the repository's `docs/host-commands.md`.

| Situation | Tell the operator to run (host) | Ends sessions |
| --- | --- | --- |
| A project directory is missing or read-only in the workspace | `pairtask mounts plan --mounts-file FILE`, then `pairtask mounts apply --mounts-file FILE`, `pairtask plan`, `pairtask apply`; on macOS `pairtask stop --vm` first | yes, at apply |
| A newer release or a reprovision is needed | `pairtask upgrade` (pin with `--release workspace-vX.Y.Z`); back up first | yes |
| Containers are stopped after a reboot | `pairtask start`, then `pairtask status` | no |
| The operator should read or answer messages | `pairtask chat`, or one-shot `pairtask operator send|receive|ack` inside `secured` | no |
| The broker or SSH path looks broken from the host | `pairtask status`; `pairtask ssh` to recreate the key-only SSH path; `pairtask recover` for provisioning state | no |
| Agent tools (CodeScene MCP, Playwright, Docker) failed to install | `pairtask tools` | no |
| A macOS writable share stopped working | `pairtask share run --mounts-file FILE` (after `share plan` and `share setup` once) | no |
| The operator's terminal types mouse codes after a dropped session | `reset` in that terminal; use `pairtask shell` / `pairtask chat`, which restore the terminal | no |
| Everything should stop for the day | `pairtask stop` (`--vm` also stops the macOS VM) | yes |

Say when a command ends sessions so the operator can tell you to check in your
work first; after it, reconcile your handoff checkpoint as after any restart.

## Select a writable working copy

Mount fields are `project_name`, `host_path`, `container_mount_path`, and
`container_readonly` (default `true`). Read-only mounts permit inspection, not
code edits or build output. A `/workspace/...` path alone does not prove writability.

Use an authorized sandbox-owned clone/copy outside host mounts for editing.
Report its path and branch; deliver through the authorized Git/patch workflow.
There is no automatic host synchronization. Direct host edits require
`container_readonly: false` and an operator-configured sharing identity.
Mac writable sharing uses `sandbox-share.py` on the host, preserving `/Users/...`
paths and explicitly listed symlink targets. Its connection must remain active;
disconnect errors are not permission to write elsewhere or reconfigure sharing.
Mac VirtioFS mounts remain read-only. If the requested checkout is read-only, report
the blocker; do not bypass it by changing mounts or host permissions.

## Soft budgets: report what is actually configured

The normal sandbox aliases do **not** select a cap. Named `pairtask budget` budgets
currently support one managed Codex launcher each; simultaneous Codex/Claude
accounting and adjustable shared allocations are not implemented. A status
command or this skill alone cannot enforce a ceiling.

For an existing authorized budget, `pairtask budget status NAME --json` reads local
state. If the operator supplied a protected status socket, use that exact endpoint:
`pairtask budget status NAME --socket /mnt/collab-status/NAME.sock --json`.
Dev cannot administer the protected store; an unavailable protected endpoint
must not fall back to a new local budget. The protected launcher is experimental
and lacks subscription login, interactive/resume support, and Claude accounting.

Reported tokens are delayed observations, not provider billing, dollars, or a
subscription balance. Report the last known total and its age separately. An old
timestamp can mean an idle session or delayed reporting; missing reports and read
errors do not establish zero usage. Soft stopping can overshoot. If a capped task
has no configured supervisor, or accounting fails, say so before starting further
spend-dependent work and ask the operator to establish it; do not claim the prompt
or alias supplies a cap.
Continue local inspection and checks within existing authorization; do not launch
an uncapped agent run to get around the missing control.

Keep the same authorized budget across restarts/resume. Do not raise/reset it,
create a replacement to evade exhaustion, or delete usage history. On exhaustion
or accounting failure, report the unfinished work and let the operator decide.
If shared allocations become available later, redistribution must be explicitly
authorized and limited to the remaining shares within the person's fixed ceiling;
do not invent a rebalance command today.

[Sandbox setup](https://github.com/pairtask/pairtask/blob/main/docs/sandbox.md) ·
[Budget behavior](https://github.com/pairtask/pairtask/blob/main/docs/host-integration.md#codex-soft-cap) ·
[Protected runtime](https://github.com/pairtask/pairtask/blob/main/docs/secured-runtime.md)
