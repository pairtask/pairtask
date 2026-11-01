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

[MIT](LICENSE) or [Apache 2.0](LICENSE-APACHE) · [Get started](#set-up-a-sandbox) · [Pair two machines](#connect-two-machines) · [Features](#features) · [Docs](#documentation) · [Troubleshooting](docs/troubleshooting.md)

> [!TIP]
> **Work across machines.** Pair your workspace with a colleague's, then
> message each other, let your agents talk to theirs and ask each other's
> agents for reviews. [Pair in five steps ↓](#connect-two-machines) ·
> [Pairing guide →](docs/pairing.md)

## Features

- 🤝 **Agents that pair up**: Codex, Claude and [Pi](docs/pi.md) share one workspace and talk to each other directly. Run several of each and group them in rooms.
- 🌐 **Two machines, one team**: Pair your workspace with a colleague's over your LAN or tailnet. Messages arrive at once, you choose which of your agents theirs may talk to and ask for reviews, and `/pair status` shows how each paired workspace stands. If one moves to another address, `/pair address` reaches it again without pairing anew. [Pair in five steps ↓](#connect-two-machines)
- 💬 **One chat to lead them**: Message one agent, a group or everyone. Replies, acknowledgments, search, history, [Markdown and diagrams](docs/chat-formatting.md). [Operator chat →](docs/operator-chat.md)
- 🔍 **Pull request reviews**: `/review OWNER/REPO#N AGENT` hands a pull request to an agent, which reviews it and publishes the result on GitHub. [Reviews →](docs/operator-chat.md#reviews)
- 📬 **Nothing gets lost**: Messages wait until they are acknowledged, survive restarts, and show where they are. [Delivery →](docs/durable-inboxes.md)
- 👀 **See who is doing what**: Agents report their status and claim tasks; the chat and a [dashboard](docs/dashboard.md) show it.
- 🔒 **Sandboxed by default**: Agents run in containers, apart from your machine; the message broker sits in its own [offline container](docs/secured-runtime.md). Your projects survive upgrades.
- 🧰 **Ready to code**: Your [host folders](docs/sandbox-mounts.md), [Docker](docs/sandbox.md#docker-and-compose) and the [tools agents need](docs/sandbox-tools.md), installed for you.
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

Pair your workspace with one on another machine over your LAN or a tailnet.
Once paired, the two operators can message each other, the agents you share
can talk across, and each side can ask the other's agents for reviews. The
[pairing guide](docs/pairing.md) has every step with its checks and what to
do when something doesn't connect.

On both machines, update and turn pairing on:

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
pairtask pairing on --host-port 7445 --name my-laptop
```

Then pair in `pairtask chat`. A invites, B joins:

| On | Type | What it does |
| --- | --- | --- |
| A | `/pair open` | Opens an invitation. Send its code to B over a trusted channel; it works once. |
| B | `/pair redeem A_IP:7445 CODE my-laptop` | Joins A. The last word is the name A will see for B. |
| B | `/pair status` | Shows B's key. Read its first characters out to A. |
| A | `/pair accept` | Pick the request whose key matches what B read out. |
| B | `/pair settle` | Finishes the pairing on both sides. |

Each paired workspace gets its own `⇄` tab. In it:

| Type | What it does |
| --- | --- |
| a message | Goes to the other operator. |
| `@codex-1 a message` | Goes to one of their agents, shown as `codex-1@WORKSPACE`. |
| `/pair share claude-1` | Lets their side message your claude-1; `/pair unshare claude-1` stops it. |
| `/pair reviews claude-1 ask` | Lets them ask claude-1 for reviews, each waiting for your OK. |
| `/pair review codex-1 OWNER/REPO#12 BASE HEAD` | Asks one of their agents to review those commits. |
| `/pair name office-m5` | Calls that workspace office-m5 here, in the tab and its agents' labels. |
| `/pair address HOST:PORT` | Reaches it again after it moved to another address. |
| `/pair pause` | Blocks new traffic with that workspace, both ways, until `/pair resume`. |

`/pair status` lists every paired workspace, how it stands and what is
waiting for you. A name you choose stays on your side; the key identifies a
workspace ([names](docs/pairing.md#names)).

## How it fits together

Agents and the dashboard run in **workspace**; the message broker and its
database run in **secured**, which has no network. A shared socket connects
them. `/workspace` and `/home/agent` survive container upgrades. With
pairing on, a third container, **connector**, relays pairing traffic between
the broker and other machines. It holds no key, and secured still has no
network.

![You start agents (Codex, Claude Code, Pi) in workspace and open the operator chat in secured. Agents and the read-only dashboard reach the broker through one shared Unix socket; only the broker uses SQLite, and secured has no network. With pairing on, the connector relays a paired workspace on another machine to the broker over TLS pinned to each identity key.](docs/assets/architecture.svg)

[Diagram source](docs/assets/architecture.py) · [Control runtime](docs/secured-runtime.md)

On macOS, a Colima VM runs the Linux host for Incus; on Linux, Incus runs
directly. The containers are the same.

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
  machine.
- [Pair two machines](docs/pairing.md): connect your workspace with one on
  another machine, step by step.
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
