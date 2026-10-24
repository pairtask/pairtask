# Separate control runtime

| Container | Runs | Network |
| --- | --- | --- |
| `workspace` | Codex, Claude, project code and read-only dashboard as `agent` | Online by default |
| `secured` | One broker, SQLite and private control state | Offline |

New helper deployments enable this by default. For an existing standalone sandbox,
set `secured_runtime = true` in its deployment variables and follow
[plan/apply](sandbox.md#2-install-or-upgrade). Existing containers must both be stopped;
preflight rejects live upgrades. Keep them stopped after a failed apply and retry.
Shared mounts must be present at boot, not hot-added.

Control adds 1 CPU, 2 GiB RAM, a `disk_gib` root disk, 1 GiB private state and three
16 MiB IPC volumes. Dev's local broker is disabled; persistent projects/home remain
in dev. [Upgrade and retention](sandbox-storage.md#replace-dev-retain-data).
**Sharing the broker does not give ordinary dev agents protected token caps.**

## Shared broker and dashboard

After [SSH setup](sandbox.md#3-ssh-into-the-workspace), run from your host:

```sh
ssh -F infra/incus/ssh/config workspace pairtask status
ssh -t -F infra/incus/ssh/config workspace pairtask dashboard
```

Both clients run in dev and query the broker through the shared socket.
The dashboard has no database access or cap/settings controls.

The shared socket is `/mnt/collab-ipc/broker.sock`. Existing `/tmp/collab-ai.sock`
defaults are root-owned aliases, so `pairtask-codex`, `pairtask-mcp`, and the installed
Claude MCP configuration connect without overrides. `/run/collab-ai/broker.sock`
also remains an alias in control for the experimental launcher.

Control mounts the IPC volume read-write; dev mounts it read-only at boot.
Directory `0750` and socket `0660` are owned by `broker:collab-clients`; `agent`
belongs to the client group. Incus shifts the volume identities into each isolated
container's mapping. Clients can send protocol requests, but cannot replace the
socket or access the database. This does not authenticate agent IDs or isolate
clients from other processes running as the same user. Within that trust in
the broker and its control plane, the `origin` the broker stamps on a
delivered message is reliable: the `operator` ID is reserved, so an agent's
text claiming to be the operator is just text; `unknown` marks legacy or
unlabeled history, not an authenticated origin (see "Trust model" in
`operator-chat.md`).

The only active collaboration database is
`/var/lib/collab-ai-secured/broker/broker.db`. On upgrade, an existing control
`broker.db` is copied consistently into that directory once; the original is retained.
The old workspace database is left untouched and is **not merged** into control.
Export any needed workspace history before replacing its root disk. The control
database survives broker crashes and control-container replacement.

## Operator messages

The human operator sends to agents from `secured`, never from the workspace. On
the host (Linux: `local:`):

```sh
incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --to codex-1 "Please pause after this task."
incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --group claude-1,codex-1 "Review starts now."
incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --all --json "Stand-up in 5 minutes."

# Read the operator inbox, acknowledge a message, and answer it
incus --project pairtask exec colima-pairtask:secured -- pairtask operator receive
incus --project pairtask exec colima-pairtask:secured -- pairtask operator ack MESSAGE_ID
incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --to codex-1 --in-reply-to MESSAGE_ID "Yes, go ahead."
```

`receive` prints a page, then confirms receipt. Continue a scan with the
printed `--after`/`--upper` values. `ack` records that you received the
message; it is not approval.

- **The endpoint:** `/run/collab-ai-operator/operator.sock` lives in a
  `0700` runtime directory owned by `broker`, created and removed by
  `collab-secured-broker`. It is on no shared volume, so the workspace never
  sees it.
- **Who it serves:** the broker accepts only UID 0 there (`SO_PEERCRED`,
  checked before reading). So root in `secured`, and therefore Incus or host
  administrators, act as the operator; workspace agents can't.
- **What agents see:** messages arrive with `origin: operator` and
  `from: operator`. Agents can't supply an origin or register the `operator`
  ID.
- **Audit:** every send and every rejected connection is recorded in
  `operator_audit` in the control database, without message text. Use
  `--message-id` to retry a send safely.
- **Not available yet:** `attach` and seat control (#69). See
  [protocol](protocol.md#origin-and-operator-messages-v4).

### Interactive chat

```sh
python3 scripts/sandbox-session.py chat      # wraps the command below and restores your terminal after
incus --project pairtask exec -t colima-pairtask:secured -- pairtask operator chat
```

The chat needs an interactive terminal (`-t`). Prefer the wrapper: a session
that drops (a reprovision, a lost connection) cannot turn the terminal's
mouse reporting off again, and every mouse move then types `35;26;31M`-like
text into your shell. The wrapper saves your terminal state before the
session and restores it afterwards, on any exit status and when interrupted,
and emits nothing when its output is not a terminal (#122). If a terminal is
already in that state, run `reset`, or:

```sh
printf '\e[?1000l\e[?1002l\e[?1003l\e[?1006l'
```
The chat runs full screen: the
conversation fills the window, newest at the bottom, and you type at the
bottom. A hint line above the input always shows who your message will reach.

- **Addressing:**
  - No mention sends to everyone: `Stand-up in 5 minutes`.
  - Mentions at the start pick recipients: `@codex-1 please review`, or
    `@codex-1 @claude-1 sync at 3`. Typing `@` opens a picker; Tab or Enter
    completes.
  - An `@` later in the text is plain text, and pasted text is always literal.
    Start with `\@` for a message that begins with `@`.
  - IDs that can't be typed as `@id` are addressed with `/to ["id"]`.
- **Commands:** `/` in an empty input, or Ctrl+K at any time, opens the command
  line; it never touches your draft. `/agents`, `/reply [#n]`, `/ack` (a
  picker of what needs acknowledging; `/ack all`, `/ack from id` in bulk)
  (received, not approval), `/history`, `/stages #n`, `/to`, `/find`,
  `/copy #n`, `/clear`, `/help`, `/quit`. `#n` is the number shown on each
  message. Each received message shows one mark for its state: `○ needs ack`,
  `◐ ack queued`, `● acknowledged`. A reply (`/reply`, Ctrl+R) acknowledges
  the message it answers once the broker accepts the reply; `/ack` is for
  "seen, no reply needed".
- **Keys:** Enter sends. Shift+Enter, Alt+Enter, Ctrl+J, or `\` then Enter
  insert a new line; the input grows with your draft up to six rows. PgUp/PgDn
  scroll, PgUp at the top loads older messages, Ctrl+End jumps to the newest
  message, Ctrl+R replies to the newest message to you, F1 shows help, Ctrl+Q
  quits. Tab and Shift+Tab switch between the common chat ("All") and one
  conversation per agent, Alt+1..9 jump to a position in the bar, `/chat`
  does the same by name; each conversation keeps its own draft, and a plain
  message to an agent acknowledges what it sent you so far. The mouse wheel
  scrolls the transcript (hold Shift, or Option on macOS, to select text in
  the terminal while the chat runs); Up in an empty input recalls your last
  sent message to correct and resend, Up again walks older, Down newer.
- **One chat at a time:** a second chat exits with `owner_busy`. While a chat
  is open, one-shot `pairtask operator receive` and `ack` are refused; `send`
  and `history` still work.
- **Reconnects** retry with backoff. A malformed broker frame stops retrying;
  press Ctrl+R to retry. If you quit while a send's outcome is unknown, the
  chat says so; `pairtask operator history` shows whether the broker accepted
  it. The design is in [operator chat](operator-chat.md), "Chat v2".

## Status, stop and restart

On the host (Linux: `local:`):

```sh
incus --project pairtask list colima-pairtask:
incus --project pairtask stop colima-pairtask:workspace colima-pairtask:secured
incus --project pairtask start colima-pairtask:secured colima-pairtask:workspace
```

Set `running = false` to keep both stopped across applies; boot autostart is disabled.
Private state survives container replacement. Disabling control or changing project/pool
can remove its volumes; use [explicit removal](sandbox-storage.md#deliberate-removal).

<details>
<summary>Experimental protected launcher — protocol clients only, no subscription login or terminal resume</summary>

## Experimental protected Codex launcher

This separate path still takes JSON protocol input, not terminal prompts.
Subscription login, protected terminal/resume support and Claude/shared accounting
remain under [#34](https://github.com/pairtask/pairtask/issues/34). Its managed
provider points to an unused loopback fixture port (`18080`).

Run on your host. On Linux replace `colima-pairtask:` with `local:`:

```sh
incus --project pairtask exec colima-pairtask:secured -- \
  env PAIRTASK_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  pairtask budget create my-task --tokens 100000

incus --project pairtask exec colima-pairtask:secured -- \
  env PAIRTASK_BUDGET_DIR=/var/lib/collab-ai-secured/budgets \
  pairtask budget status my-task --json
```

Launch from the host, connecting stdin/stdout to a restricted App Server client:

```sh
incus --project pairtask exec colima-pairtask:secured -T -- \
  pairtask-supervised-codex my-task
```

This takes [JSON-line protocol messages](budget-boundary.md#experimental-restricted-operator),
not terminal prompts. Terminal and resume support are not available on this path.
The budget is fixed by the administrator; operator messages cannot select another.
To stop it from another host terminal:

```sh
incus --project pairtask exec colima-pairtask:secured -- \
  systemctl stop pairtask-codex-my-task.service
```

The budget directory remains root-only on the private volume mounted only in secured.
Creation refuses to overwrite a cap or reset usage. There is no workload-facing
administration endpoint. Incus administrators remain trusted: they can change
containers and disks. Keep host control scripts, state, credentials and the Incus
socket outside dev mounts; run project code only in `workspace`.

`/mnt/collab-status` is a separate volume: secured root can publish a status socket;
dev receives a read-only mount. Its root-owned directory is not world-writable.
Only public status belongs here, never budget JSON, credentials or admin sockets.
While `my-task` is running, a dev terminal can read its published status:

```sh
pairtask budget status my-task --socket /mnt/collab-status/my-task.sock --json
```

Provisioning alone leaves this directory empty. A missing socket is an error;
there is no fallback to a dev-local budget. Read-only mounting prevents directory
changes but does **not** prevent socket requests; the status server's read-only
API rejects mutations. Polling this endpoint is visibility, not admission control.

## Installed services and trust

- `collab-secured-setup` installs managed configuration on every secured boot,
  preserving budgets and usage. `/workspace` there is root-owned and contains no dev repository.
- `pairtask-secured-broker` runs as `broker` and owns the shared IPC socket and private
  database directory. The broker group can traverse the private volume root; budget
  and native configuration directories stay root-only. Its startup helper removes
  only a stale socket it owns, and refuses live endpoints, files or symlinks.
- `collab-client-setup` connects default client paths to the shared socket. The dev
  broker is disabled by its mount condition; a missing control broker never causes
  fallback to a dev-local database. Without `secured_runtime`, the standalone
  workspace broker still runs as before.
- `pairtask-dev-executor.socket` owns a root-only socket on `/mnt/collab-executor`.
  Each accepted connection starts a native executor as `agent`, with no added privileges.
  Secured mounts the endpoint read-only. Agent commands cannot connect, unlink it,
  substitute a symlink, or modify installed executables.

Container/host administrators remain trusted. Dev root can replace its executor;
the protection here is against unprivileged agent commands. Missing or unsafe
endpoints prevent launch through a bounded connection check; there is no local-execution
fallback. This startup check does not monitor later executor disconnects, which
remain native tool errors under the session's soft cap. The supervisor
[stops native clients on failure](budget-boundary.md#supervisor-failure).


</details>

<details>
<summary>Validation and host requirements</summary>

## Validation

CI tests both architectures with an offline fake client: rejection of unsafe hot upgrades,
stopped-workspace upgrade without data loss, unchanged second plan,
separate mappings, denied private-state access, denied status replacement/remount,
real status reads over the shared UDS, persisted accounting after stop/start and
secured replacement, and teardown. Dev host mounts are tested in the same deployment.
It also exercises the installed native configuration, broker and executor, endpoint
replacement denials, restart and missing-executor refusal. No provider requests or
subscription credentials are used.
The shared-broker proof exchanges a correlated reply between the real dev MCP
adapter and managed Codex proxy with a fake App Server, verifies both containers
see the same broker, and checks client-group permissions and SIGKILL recovery.

To repeat on the Linux Incus host (including inside the dedicated Colima VM):

```sh
sudo python3 scripts/sandbox-smoke.py --remote local \
  --image-dir dist/installed-workspace --secured-check
```

Use a maintained Incus release (CI uses [Zabbly's 6.0 LTS packages](https://github.com/zabbly/incus#60-lts-repository)).
Ubuntu 24.04's original Incus 6.0.0 package can reject these mounts on newer
kernels because of an [upstream detection bug](https://github.com/lxc/incus/issues/882).
The storage pool/kernel must support Incus ID-mapped custom volumes
([`security.shifted`](https://linuxcontainers.org/incus/docs/main/reference/storage_zfs/#storage-volume-configuration));
ZFS requires 2.2+ for idmaps. Unsupported hosts should fail provisioning rather
than sharing the containers' identities or using world-writable IPC directories.

</details>
