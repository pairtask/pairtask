# pairtask

**Codex and Claude, working together in a sandbox.** pairtask pairs a Claude and
a Codex agent as a duo that shares tasks, messages and a workspace.

- Real-time collaboration with durable messages and automatic delivery.
- An isolated Incus workspace with persistent projects and agent memory.
- Token usage visibility and soft caps for managed Codex; shared caps are not yet available.

[MIT](LICENSE) or [Apache 2.0](LICENSE-APACHE) licensed · [First install](docs/sandbox.md) · [Troubleshooting](docs/troubleshooting.md) · [Renamed from collab](docs/renamed.md)

## Set up a sandbox

[Provision once](docs/sandbox.md), then use the block below each day.
**Tools are installed by the rollout—no compilation needed.** The image is small;
the rollout fetches the pinned agent tools once and keeps them across upgrades.
Provisioning runs OpenTofu in a disposable container; no host Terraform/OpenTofu
install is needed. Dev has network access; optional
control stays offline. Host credentials and conversations are not imported.

**Host prerequisites:** Python, Git, OpenSSH and Incus; macOS also needs Colima.
[Install commands](docs/sandbox.md#1-install-the-host-tools). No `gh` or browser download needed.
Every host command, by task: [host commands](docs/host-commands.md).

```sh
# First install or upgrade (Incus must be running; upgrades end active sessions)
python3 scripts/pairtask.py install     # first time; later: python3 scripts/pairtask.py upgrade
# Pin a version: add --release workspace-vX.Y.Z
```

Run host commands from your provisioning checkout. On Linux, skip Colima commands
and use `local:` / `--remote local`. For host mounts, see the
[`mounts.json` example](docs/sandbox-mounts.md#choose-directories).

```sh
# HOST: run from your provisioning checkout
# Start the Mac VM and the containers; expect workspace (and secured, if enabled) RUNNING
python3 scripts/pairtask.py start
python3 scripts/pairtask.py status

# Optional: preview host mounts; uncomment with your file (does not apply)
# python3 scripts/pairtask.py mounts plan --mounts-file /path/to/mounts.json

# Check the broker, then enter dev (SSH was configured by the install)
ssh -F infra/incus/ssh/config workspace pairtask status
python3 scripts/pairtask.py shell

# CONTAINER: all commands below until exit run inside your SSH session
# First use only: uncomment to sign in before starting agents
# Codex: open the printed URL on your host and enter the device code
# command codex login --device-auth
# command claude auth login
# gh auth login --web --git-protocol https  # optional GitHub CLI login
# pairtask-codescene-login                  # optional CodeScene token; hidden prompt

# First use only: clone if you have not already done so
# git clone https://github.com/pairtask/pairtask.git /workspace/collab-ai
tmux new -A -s collab

# In each tmux window: enter the project, then choose one agent or dashboard
# Ctrl+B then C opens another window
cd /workspace/collab-ai
codex                         # new managed Codex conversation
# codex resume                # choose a saved sandbox conversation
# codex resume SESSION_ID     # resume a specific sandbox conversation
# claude                      # Claude with collaboration channels enabled
# claude --resume             # resume Claude
# dashboard                   # broker status and agent inboxes
# rtk gain                    # estimated shell-output token savings
# docker info                 # rootless Docker inside workspace
# docker compose up -d        # run your project's compose.yaml

# Disconnect: Ctrl+B then D detaches tmux; exit leaves SSH
exit

# HOST AGAIN: optional shutdown; stops agents, keeps persistent files
# python3 scripts/pairtask.py stop        # add --vm to stop the Mac VM too
```

Expect `Broker: ready`. Run one Codex and one Claude session; additional sessions
need distinct agent IDs. [Login help](docs/sandbox.md#sign-in-and-network-access).
Both agents should read `/usr/local/share/pairtask/SKILL.md`.

[Mount directories](docs/sandbox-mounts.md) ·
[Mac writable sharing](docs/sandbox-mounts.md#mac-writable-sharing) ·
[Docker and Compose](docs/sandbox.md#docker-and-compose) ·
[Bundled MCP tools](docs/sandbox.md#bundled-mcp-tools) ·
[Upgrade or reprovision](docs/sandbox-storage.md#replace-dev-retain-data) ·
[Back up](docs/sandbox-storage.md#back-up-and-restore) ·
[Stop or remove](docs/sandbox.md#stop-or-remove)

## Dashboards

Run on your host:

```sh
# Agent connections and pending messages (terminal UI)
ssh -t -F infra/incus/ssh/config workspace pairtask dashboard

# Containers, resources and logs (browser UI)
incus webui colima-pairtask:  # Linux: incus webui local:
```

For Incus, open the printed URL and select project `pairtask`.
The collaboration dashboard is read-only; prompts and approvals stay in agent
terminals. [Controls](docs/dashboard.md) · [UI help](docs/sandbox.md#incus-web-ui).

## How it fits together

With `secured_runtime = true`, Codex, Claude and the read-only dashboard run in
**workspace**; **secured** owns one broker and SQLite database.
The shared Unix socket connects them.
`/workspace` and `/home/agent` persist across container replacement.

![Codex, Claude and the read-only dashboard in workspace connect through a shared Unix socket to the broker and SQLite in secured.](docs/assets/architecture.svg)

[Architecture source](docs/assets/architecture.puml) · [Control setup](docs/secured-runtime.md)

<details>
<summary>macOS and Linux layouts</summary>

| macOS: Colima hosts Incus | Linux: Incus runs directly |
| --- | --- |
| ![macOS sandbox](docs/assets/sandbox-macos.svg) | ![Linux sandbox](docs/assets/sandbox-linux.svg) |
| [Diagram source](docs/assets/sandbox-macos.puml) | [Diagram source](docs/assets/sandbox-linux.puml) |

</details>

## Security

Agent commands run inside the container. Host mounts default to read-only;
writes require a dedicated sharing identity. Writable sharing lets agents
change files that you or host automation may later execute.
[Boundaries](docs/sandbox.md#security-can-agents-execute-code-on-my-host).

## Run on your host

No Incus? Follow the [host quick start](docs/quickstart.md) to build and run locally.
These agents have no Incus isolation.

### Resume or fork

[Host commands and compatibility](docs/host-integration.md#codex-terminal).
Sandbox start/resume commands are in the daily block above.

### Set a Codex soft cap

The sandbox aliases are uncapped by default. Use a [named budget](docs/host-integration.md#codex-soft-cap)
for one managed Codex launcher. Reports can arrive late; overshoot is possible.
Claude and shared budgets are not yet covered.

### Claude Code

The sandbox alias is preconfigured. For host sessions, use the
[dedicated channel config](docs/quickstart.md#3-start-claude-code).
Claude requires channel consent and account/organization support.

## Reference and contributing

[Protocol](docs/protocol.md) · [Manual MCP](docs/mcp.md) ·
[Status API](docs/status.md) · [Agent skill](docs/skills/collab-ai/SKILL.md)

[Report an issue](https://github.com/pairtask/pairtask/issues). Run checks with:

```sh
go test -race ./... -timeout=30s
```
