# pairtask

**Work with Codex and Claude in one place.** pairtask gives both agents the
same project workspace and a shared message channel, with you directing the
work from an operator chat. Ask one agent to implement a change and the other
to review it; they can coordinate directly instead of making you relay every
message.

Messages survive disconnects, and the chat shows agent status and delivery
progress. The agents run in an Incus sandbox, separate from your host, while
project files and agent home persist across container upgrades. You decide
what to ask, review, and approve.

[MIT](LICENSE) or [Apache 2.0](LICENSE-APACHE) licensed · [First install](docs/sandbox.md) · [Troubleshooting](docs/troubleshooting.md) · [Renamed from collab](docs/renamed.md)

## Set up a sandbox

These steps use the installed `pairtask` command. **Host** means your Mac or
Linux terminal. **Workspace** means the shell opened by `pairtask shell`.
The installer provisions the sandbox and installs the agent tools; you do not
need to compile them or install OpenTofu on your host.

### First time

1. On **macOS**, install the host prerequisites:

   ```sh
   brew install colima incus python git
   ```

   On **Linux**, [install Incus](https://linuxcontainers.org/incus/docs/main/installing/)
   with a quota-capable storage pool and managed bridge. You also need Python
   3.9+, Git, and OpenSSH. See the [full sandbox setup](docs/sandbox.md) for
   host details.

2. On your **host**, install pairtask and provision the sandbox:

   ```sh
   curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
   ```

   The installer adds `pairtask` to your shell PATH, sets up SSH, and lists the
   containers. **Open a new host terminal** before using `pairtask`. Run the
   installer again later to upgrade; an upgrade ends active sessions but keeps
   projects and agent home.

3. On your **host**, enter the workspace:

   ```sh
   pairtask shell
   ```

   Inside the **workspace**, run these sign-ins one at a time and follow their
   prompts. Codex prints a device code to enter in your host browser.

   ```sh
   command codex login --device-auth
   command claude auth login
   ```

4. Still inside the **workspace**, clone a project. This repository is a
   copy-and-paste example; use your own Git URL and project directory for your
   work.

   ```sh
   git clone https://github.com/pairtask/pairtask.git /workspace/pairtask
   exit
   ```

### Each session

1. On your **host**, start the containers and check the broker:

   ```sh
   pairtask start
   pairtask status
   pairtask shell pairtask status
   ```

   Expect `workspace` and `secured` to be running, and `Broker: ready`.

2. Open a new host terminal for **Codex**. Enter the workspace, then start
   Codex from the project directory:

   ```sh
   pairtask shell
   ```

   ```sh
   cd /workspace/pairtask
   codex
   ```

   Complete Codex's folder trust prompt, then start a conversation by pasting
   this into Codex:

   ```text
   Read /usr/local/share/pairtask/SKILL.md. Collaborate with claude-1 on this project. Tell me when you are ready.
   ```

3. Open another host terminal for **Claude**. Enter the workspace, then start
   Claude in the same project:

   ```sh
   pairtask shell
   ```

   ```sh
   cd /workspace/pairtask
   claude
   ```

   Accept Claude's channel consent prompt, then paste this into Claude:

   ```text
   Read /usr/local/share/pairtask/SKILL.md. Collaborate with codex-1 on this project. Tell me when you are ready.
   ```

4. Open a third host terminal for **your chat**:

   ```sh
   pairtask chat
   ```

   Press Tab until the **codex-1** conversation is selected. Check the
   recipient hint above the input, then paste this task and press Enter:

   ```text
   Find one unclear step in this project's README, improve it, and ask claude-1 to review the edit.
   ```

   Press Tab until **claude-1** is selected, check the recipient hint, then
   paste this task and press Enter:

   ```text
   Review codex-1's README edit for accuracy and clarity. Report any issues to us both.
   ```

   Replace the example task with your own. Agents can send you updates and
   coordinate with each other; you still decide which changes to accept.

To work on another repository, replace `/workspace/pairtask` in steps 2 and 3
with that project's directory. [Sign-in help](docs/sandbox.md#sign-in-and-network-access) ·
[Host commands](docs/host-commands.md) · [Operator chat](docs/secured-runtime.md#interactive-chat) ·
[Mount host directories](docs/sandbox-mounts.md) · [Resume an agent](docs/host-integration.md#codex-terminal).

The workspace persists when you close its shell; closing an agent terminal
ends that agent process. To keep a terminal session running while you detach,
use `tmux` inside the workspace. To stop the containers from your host, run
`pairtask stop`.

[Mount directories](docs/sandbox-mounts.md) ·
[Docker and Compose](docs/sandbox.md#docker-and-compose) ·
[Upgrade or reprovision](docs/sandbox-storage.md#replace-dev-retain-data) ·
[Back up](docs/sandbox-storage.md#back-up-and-restore) ·
[Stop or remove](docs/sandbox.md#stop-or-remove)

## Dashboards

On your **host**, open the terminal dashboard for agent connections and
pending messages:

```sh
pairtask shell --tty pairtask dashboard
```

For container resources and logs, open the Incus web UI. On macOS, run:

```sh
incus webui colima-pairtask:
```

On Linux, run:

```sh
incus webui local:
```

For Incus, open the printed URL and select project `pairtask`.
The dashboard is read-only. Use `pairtask chat` for messages; agent-specific
prompts may still appear in the agent terminal.
[Controls](docs/dashboard.md) · [UI help](docs/sandbox.md#incus-web-ui).

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
For saved sandbox conversations, run `codex resume` or `claude --resume` from
the project directory inside the workspace.

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
[Status API](docs/status.md) · [Operator chat formatting](docs/chat-formatting.md) ·
[Agent skill](docs/skills/collab-ai/SKILL.md)

[Report an issue](https://github.com/pairtask/pairtask/issues). Run checks with:

```sh
go test -race ./... -timeout=30s
```
