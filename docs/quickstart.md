# Host quick start

**Outside the sandbox only.** Sandbox users get their tools from the rollout and should use
[the README](../README.md#set-up-a-sandbox).

You need macOS/Linux, Git, Go 1.26+, and installed, signed-in Codex and Claude Code.
Codex integration was tested with 0.156.1. Claude needs channel consent and an
account/organization that permits channels; otherwise use [manual MCP](mcp.md).

## 1. Build and start the broker

Terminal 1:

```sh
git clone https://github.com/pairtask/pairtask.git
cd pairtask
go build -o pairtask-broker ./cmd/broker
go build -o pairtask-codex ./cmd/codex
go build -o pairtask-mcp ./cmd/mcp
go build -o pairtask ./cmd/collab
pwd  # use this absolute directory in the configuration below
PAIRTASK_SOCKET_PATH=/tmp/collab-ai.sock PAIRTASK_DB_PATH="$PWD/collab-ai.db" ./pairtask-broker
```

Leave it running. If you already have a broker, reuse it or choose another socket
in **every** command/config. Keep its database for message recovery.

## 2. Start Codex

Terminal 2, from the collab-ai checkout:

```sh
./pairtask-codex --agent-id codex-1 --socket /tmp/collab-ai.sock --terminal -- \
  -C /absolute/path/to/your/project
```

Complete Codex's trust prompt and start a conversation. Listening is automatic;
use its `collab_runtime` tools. Do not register another adapter as `codex-1`.
[Resume, fork and caps](host-integration.md#codex-terminal).

## 3. Start Claude Code

Save this as a dedicated `mcp.json` outside the project; replace `command` with
the absolute path to the binary built in step 1:

```json
{
  "mcpServers": {
    "pairtask": {
      "command": "/absolute/path/to/pairtask/pairtask-mcp",
      "args": [
        "--agent-id", "claude-1",
        "--socket", "/tmp/collab-ai.sock",
        "--claude-channel", "--auto-listen"
      ]
    }
  }
}
```

Terminal 3:

```sh
cd /absolute/path/to/your/project
claude --strict-mcp-config --mcp-config /absolute/path/to/mcp.json \
  --dangerously-load-development-channels server:pairtask
```

Accept channel consent. Keep this session open; another session needs a different
agent ID. [Channel troubleshooting](host-integration.md#claude-code).

## 4. Check both directions

Terminal 4, from the collab-ai checkout:

```sh
./pairtask status --socket /tmp/collab-ai.sock
./pairtask dashboard --socket /tmp/collab-ai.sock
```

Expect `Broker: ready` and both IDs connected. Ask Claude:

> Send codex-1 “Reply with pong”. Codex should acknowledge the message and reply
> with `in_reply_to` set to its ID. Acknowledge the pong without replying again.

Both conversations should receive messages without reminders. A reply does not
implicitly acknowledge a message. Connection status alone does not prove delivery.
Point both agents at the [collaboration skill](skills/pairtask/SKILL.md) and agree
file ownership before concurrent edits.

## Optional: a classifier for untagged messages (shadow mode)

Off by default. The broker can ask a decision model, TypeSafe **Jev** or
Cloudflare **Clef** on Workers AI, which agent a message you send to everyone
without an `@` tag was for, and record the answer without acting on it: no
suggestion, no assignment, claims work as before. The standalone broker of
this guide takes the settings from its environment. Put the key in a file
only you can read, stop Terminal 1's broker (Ctrl+C; the agents reconnect
when it is back) and start it again with the same socket and database as
step 1 plus the settings, for Jev:

```sh
(umask 077; printf '%s\n' 'YOUR-TYPESAFE-KEY' > "$HOME/.pairtask-jev-key")
PAIRTASK_SOCKET_PATH=/tmp/collab-ai.sock PAIRTASK_DB_PATH="$PWD/collab-ai.db" \
PAIRTASK_CLASSIFIER=shadow PAIRTASK_CLASSIFIER_PROVIDER=typesafe \
PAIRTASK_CLASSIFIER_KEY_FILE="$HOME/.pairtask-jev-key" ./pairtask-broker
```

Then `./pairtask operator classifier report --socket /tmp/collab-ai.sock
--since 24h` shows what it recorded. The sandbox install does the same with `pairtask classifier on`
([README](../README.md)). Every setting, what leaves the host, and Clef's
account: [classifier](host-commands.md#classifier-shadow-mode).

## Watch, stop, and return

`q` closes the dashboard only. Exit agents, then Ctrl+C the broker to stop.
Restart with the same database and agent IDs for unacknowledged message recovery;
resume the agent conversation separately for its history.
[Dashboard controls](dashboard.md) · [Troubleshooting](troubleshooting.md).
