# pairtask

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/banner/pairtask-banner-dark.svg">
    <img alt="pairtask" src="docs/brand/banner/pairtask-banner-light.svg" width="100%">
  </picture>
</p>

**Your AI coding agents, working as a team.** pairtask puts Codex, Claude and
Pi in one shared workspace, lets them message each other directly, and gives
you one chat to direct them. Ask one agent to build a change and another to
review it; they coordinate without you relaying every message.

[MIT](LICENSE) or [Apache 2.0](LICENSE-APACHE) · [Get started](#set-up-a-sandbox) · [Features](#features) · [Docs](#documentation) · [Troubleshooting](docs/troubleshooting.md)

## Features

- 🤝 **Agents that pair up**: Codex, Claude and [Pi](docs/pi.md) share one workspace and talk to each other directly. Run several of each and group them in rooms.
- 💬 **One chat to lead them**: Message one agent, a group or everyone. Replies, acknowledgments, search, history, [Markdown and diagrams](docs/chat-formatting.md). [Operator chat →](docs/operator-chat.md)
- 🔍 **Pull request reviews**: `/review OWNER/REPO#N AGENT` hands a pull request to an agent, which reviews it and publishes the result on GitHub. [Reviews →](docs/operator-chat.md#reviews)
- 📬 **Nothing gets lost**: Messages wait until they are acknowledged, survive restarts, and show where they are. [Delivery →](docs/durable-inboxes.md)
- 👀 **See who is doing what**: Agents report their status and claim tasks; the chat and a [dashboard](docs/dashboard.md) show it.
- 🔒 **Sandboxed by default**: Agents run in containers, apart from your machine; the message broker sits in its own [offline container](docs/secured-runtime.md). Your projects survive upgrades.
- 🧰 **Ready to code**: Your [host folders](docs/sandbox-mounts.md), [Docker](docs/sandbox.md#docker-and-compose) and the [tools agents need](docs/sandbox-tools.md), installed for you.
- 🌐 **Two machines, one team**: Pair your workspace with a colleague's over your LAN or tailnet. [Connect two machines →](docs/host-commands.md#connect-two-machines)
- ⚡ **One command**: `pairtask` installs, upgrades, starts and stops everything. [Host commands →](docs/host-commands.md)

No containers? Run it [directly on your machine](docs/quickstart.md), or
connect any MCP client [by hand](docs/mcp.md).

## Set up a sandbox

**Host** is your Mac or Linux terminal. **Workspace** is the shell that
`pairtask shell` opens.

### First time

1. **Install the prerequisites** on your host.
   - macOS: `brew install colima incus python git`
   - Linux: [Incus](https://linuxcontainers.org/incus/docs/main/installing/)
     (server 7.0.2+ or 7.5+, [why](docs/sandbox.md#incus-server-version)),
     Python 3.9+, Git and OpenSSH.

2. **Install pairtask** on your host, then open a new terminal:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
   ```

   Run it again later to upgrade. Upgrades end running sessions and keep your
   projects.

3. **Sign in** inside the workspace:

   ```sh
   pairtask shell
   command codex login --device-auth
   command claude auth login
   ```

4. **Clone a project** into the workspace, then `exit`:

   ```sh
   git clone https://github.com/you/your-project.git /workspace/your-project
   ```

### Every day

1. **Start** the sandbox on your host:

   ```sh
   pairtask start
   ```

2. **Start each agent** in its own host terminal, from your project:

   ```sh
   pairtask shell
   cd /workspace/your-project
   pairtask start codex --id codex-1     # or: claude --id claude-1
   ```

   Accept the agent's first prompt (Codex: folder trust; Claude: channel
   consent). The agents already know how to work together. Optional:
   `pairtask start pi --id pi-1 -- --skill /usr/local/share/pairtask`
   ([Pi guide](docs/pi.md)).

3. **Open your chat** in another host terminal:

   ```sh
   pairtask chat
   ```

   Tab picks a conversation; the line above the input shows who will receive
   your message. Try, in codex-1's conversation:

   ```text
   Improve one unclear step in the README and ask claude-1 to review it.
   ```

   Only one chat can be open at a time; `pairtask chat` again reconnects with
   the history.

4. **Stop** when you're done: `pairtask stop`. Closing a terminal ends that
   agent; the workspace and your files stay.

### Agent commands

Run inside the workspace, from your project:

| To | Run |
| --- | --- |
| Start an agent | `pairtask start codex --id codex-1` (or `claude`, `pi`) |
| Add another | same command, new ID: `--id codex-2` |
| Resume the last conversation | Codex: `pairtask start codex --id codex-1 -- resume`<br>Claude: `pairtask start claude --id claude-1 -- --resume` |
| Join a room | add `--room NAME` |
| Pick a model | add `--model NAME` |
| Preview the command | add `--dry-run` |

pairtask flags go before `--`; everything after goes to the agent.
More: [host commands](docs/host-commands.md) ·
[sign-in help](docs/sandbox.md#sign-in-and-network-access) ·
[mount host folders](docs/sandbox-mounts.md) ·
[back up](docs/sandbox-storage.md#back-up-and-restore) ·
[stop or remove](docs/sandbox.md#stop-or-remove).

## Dashboards

- **Agents and messages:** `pairtask shell --tty pairtask dashboard`
  ([controls](docs/dashboard.md)).
- **Containers and logs:** `incus webui colima-pairtask:` on macOS,
  `incus webui local:` on Linux; pick project `pairtask`
  ([help](docs/sandbox.md#incus-web-ui)).

## Connect two machines

Pair your workspace with one on another machine over your LAN or a tailnet:
see [connect two machines](docs/host-commands.md#connect-two-machines).

## How it fits together

Agents and the dashboard run in **workspace**; the message broker and its
database run in **secured**, which has no network. A shared socket connects
them. `/workspace` and `/home/agent` survive container upgrades.

![Codex, Claude and the read-only dashboard in workspace connect through a shared Unix socket to the broker and SQLite in secured.](docs/assets/architecture.svg)

[Diagram source](docs/assets/architecture.puml) · [Control runtime](docs/secured-runtime.md)

<details>
<summary>macOS and Linux layouts</summary>

| macOS: Colima hosts Incus | Linux: Incus runs directly |
| --- | --- |
| ![macOS sandbox](docs/assets/sandbox-macos.svg) | ![Linux sandbox](docs/assets/sandbox-linux.svg) |
| [Diagram source](docs/assets/sandbox-macos.puml) | [Diagram source](docs/assets/sandbox-linux.puml) |

</details>

## Security

Agents run commands inside the container, not on your machine. Host folders
are mounted read-only unless you set up writable sharing, which lets agents
change files you may later run. [Details](docs/sandbox.md#security-can-agents-execute-code-on-my-host).

## Run on your host

No Incus? The [host quick start](docs/quickstart.md) builds and runs pairtask
locally, without container isolation.

- **Resume:** in the sandbox, `pairtask start codex --id ID -- resume`
  continues that Codex agent's last conversation (close the old session
  first); for Claude, `pairtask start claude --id ID -- --resume`. On the
  host, use `codex resume SESSION_ID` or `claude --resume`.
  [More](docs/host-integration.md#codex-terminal).
- **Codex spending cap:** a [named budget](docs/host-integration.md#codex-soft-cap)
  caps one managed Codex; reports can arrive late, so it may overshoot.
- **Claude Code on the host** needs the
  [dedicated channel config](docs/quickstart.md#3-start-claude-code), channel
  consent and an account that allows channels.

## Documentation

**Use pairtask**

- [Host commands](docs/host-commands.md): every `pairtask` command on your
  machine, including [connecting two machines](docs/host-commands.md#connect-two-machines).
- [Operator chat](docs/operator-chat.md): conversations, commands, reviews and
  pairing in `pairtask chat`. [Formatting](docs/chat-formatting.md): Markdown,
  diagrams and `/diagram` export.
- [Dashboard](docs/dashboard.md): the read-only terminal view and its controls.
- [Pi](docs/pi.md): run Pi alongside Codex and Claude.
- [Troubleshooting](docs/troubleshooting.md): symptoms and what to check.
- [Renamed from collab-ai](docs/renamed.md): old command and setting names.

**Run the sandbox**

- [Sandbox setup](docs/sandbox.md): install, SSH, sign-in, Docker, recovery and
  the security model.
- [Control runtime](docs/secured-runtime.md): the offline `secured` container
  that holds the broker.
- [Host mounts](docs/sandbox-mounts.md): share host directories read-only or
  writable.
- [Storage](docs/sandbox-storage.md): persistent volumes, upgrades, backup and
  restore.
- [Tools](docs/sandbox-tools.md): the slim image and the installed tool stores.
- [Images](docs/sandbox-image.md): download or build workspace images.
- [Provisioning](docs/sandbox-operator.md): plan and apply without host
  Terraform.

**Run on your host and integrate**

- [Host quick start](docs/quickstart.md): build and run without Incus.
- [Host integration](docs/host-integration.md): launch options for Claude,
  Codex and Pi, resume, and soft token budgets.
- [Manual MCP](docs/mcp.md): register the adapter and use the messaging tools.
- [Durable inboxes](docs/durable-inboxes.md),
  [correlated replies](docs/correlated-replies.md) and
  [delegated listeners](docs/delegated-listeners.md): delivery guarantees and
  patterns.
- [Broker status](docs/status.md): the `pairtask status` command and its JSON.
- [Wire protocol](docs/protocol.md): the broker's newline-delimited JSON
  protocol.
- [Collaboration skill](docs/skills/collab-ai/SKILL.md): what the agents follow.

**Design notes and validation records**

[Operator chat design](docs/operator-chat-design.md) ·
[Runtime boundaries](docs/runtime-boundaries-v2.md) ·
[Budget boundary](docs/budget-boundary.md) ·
[Team presets](docs/team-presets.md) ·
[Agent roster experiment](docs/agent-roster-experiment.md) ·
[Live host handoff](docs/host-live-demo.md) ·
[Terminal communication](docs/managed-comms-live-demo.md) ·
[Codex resume validation](docs/codex-resume-validation.md) ·
[Skill acceptance cases](docs/skill-validation.md)

## Contributing

[Report an issue](https://github.com/pairtask/pairtask/issues). Run checks with:

```sh
go test -race ./... -timeout=30s
python3 -m unittest discover -s scripts/tests
```
