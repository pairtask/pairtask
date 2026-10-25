# Host commands

Everything you run on your own machine for the sandbox, by task. One command
does it all: `pairtask`, put on your PATH by the one-line installer below, or
`python3 scripts/pairtask.py` from a provisioning checkout (`pairtask` below
stands for either). The installer also writes `pk` as a short alias when no
other `pk` exists; it never replaces one it did not write. Each `pairtask`
subcommand runs one of the scripts in `scripts/` with the matching action, or
an Incus command, and passes your flags through unchanged, so the legacy
`python3 scripts/sandbox-*.py` forms keep working for one release. `pairtask
--dry-run <command> …` prints what would run.

Where a command runs: **host** is your Mac or Linux machine; **workspace** is
the dev container you SSH into; **secured** is the offline control container
(when `secured_runtime = true`). "Ends sessions" means running agents and
shells in the containers are stopped.

## Install and upgrade

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh \| sh` | host | The one-line install: checks Python 3.9+, Git, Incus (and Colima on macOS, offering the `brew install` line), fetches the latest release's host tooling into `~/.pairtask/host`, puts `pairtask` on your PATH, then runs `pairtask install --release <that tag>`; run again later to upgrade both. Options after `sh -s --`: `--version TAG`, `--no-provision`, `--no-modify-path`, `--yes`. Never run as root. | yes, if containers exist |
| `pairtask install` | host | First install: on macOS starts the Colima VM, then downloads and verifies the latest release, provisions the containers, configures SSH and lists them. Flags go to the rollout (`--release workspace-vX.Y.Z`, `--remote`, `--project`, `--storage-pool`, `--network`, `--tools`). | yes, if containers exist |
| `pairtask upgrade` | host | Download the release, stop the containers, replace them, keep projects and agent home. Same flags as install. Legacy: `python3 scripts/sandbox-provision.py rollout`. | yes |
| `pairtask download` | host | Fetch and verify a release image without provisioning (`--release`, `--from-zip`, `--from-dir`). Legacy: `python3 scripts/sandbox-download.py`. | no |
| `pairtask vm plan` / `pairtask vm apply` | host (macOS) | Preview or create and start the dedicated `pairtask` Colima VM that runs Incus. Legacy: `python3 scripts/sandbox-host.py plan|apply`. | no |
| `pairtask ssh` | host | Create the key-only SSH path into the workspace; the remote defaults to your OS (`colima-pairtask` on macOS, `local` on Linux), `--remote` and `--instance` override. The rollout does this for you. Legacy: `python3 scripts/sandbox-ssh.py --remote …`. | no |

Prerequisites the installer cannot bundle: Incus (and Colima on macOS), Python
3.9+, Git, OpenSSH. macOS: `brew install colima incus python git`. Linux:
[install Incus](https://linuxcontainers.org/incus/docs/main/installing/) with a
quota-capable pool and a managed bridge. Details: [sandbox setup](sandbox.md).

## Start, stop and status

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask start` | host | On macOS starts the VM, then starts `secured` (if present) and `workspace`, one Incus call each. `--remote`, `--project`. | no |
| `pairtask stop` | host | Stops `workspace`, then `secured`; `--vm` also stops the macOS VM. | yes |
| `pairtask status` | host | Lists the containers and their state (`incus --project pairtask list`). | no |
| `pairtask shell pairtask status` | workspace | Broker and agent inbox status inside the sandbox; expect `Broker: ready`. `pairtask shell --tty pairtask dashboard` opens the dashboard. | no |
| `incus webui colima-pairtask:` (Linux: `local:`) | host | The Incus web UI, after the one-time setup in [sandbox.md](sandbox.md#incus-web-ui). | no |

## Shell and chat

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask shell` (short: `pairtask sh`, or `pk sh`) | host → workspace | SSH into the workspace as `agent` and restore your terminal afterwards; extra arguments run as a remote command, with a terminal when `--tty` is given. Raw: `ssh -F ~/.pairtask/host/infra/incus/ssh/config workspace` (or the checkout's `infra/incus/ssh/config`). | no |
| `pairtask chat` | host → secured | The operator chat (`incus exec -t … pairtask operator chat`), terminal restored afterwards. [Chat guide](operator-chat.md). | no |
| `incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --to codex-1 "text"` | secured | One-shot operator message, receive or ack without the chat; refused while a chat is open. [Secured runtime](secured-runtime.md#operator-messages). | no |
| `reset` | host | Repairs a terminal left in mouse-reporting mode by a dropped session. | no |

## Mounts and sharing

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask mounts plan --mounts-file FILE` | host | Preview host directories mounted into the workspace. Legacy: `python3 scripts/sandbox-host.py mounts-plan`. | no |
| `pairtask mounts apply --mounts-file FILE` | host | Write the mount variables; then `pairtask plan` and `pairtask apply` attach them. On macOS: `pairtask stop --vm`, `pairtask mounts apply …`, `pairtask start`, then `pairtask plan` and `pairtask apply`; see the [apply sequence](sandbox-mounts.md#apply). | yes, at apply |
| `pairtask share plan` / `pairtask share setup` / `pairtask share run` (`--mounts-file FILE`) | host (macOS) | Writable sharing through a dedicated local account: preview, create the account and ACLs (asks for sudo), run the helper. Legacy: `python3 scripts/sandbox-share.py`. | no |

Details and the `mounts.json` format: [mounts](sandbox-mounts.md).

## Recovery, backup and removal

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask plan` / `pairtask apply` | host | Preview and apply a provisioning change: `pairtask plan --replace` replaces the dev container keeping data; `pairtask plan --destroy` plans deletion (volume guards must be removed first). Legacy: `python3 scripts/sandbox-provision.py plan|apply`. | yes, when replacing or destroying |
| `pairtask recover` | host | Repair provisioning state without changing data. Legacy: `python3 scripts/sandbox-provision.py recover`. | no |
| `pairtask tools` | host | Retry the pinned agent tools install in running containers. Legacy: `python3 scripts/sandbox-provision.py tools-retry`. | no |
| `incus --project pairtask stop colima-pairtask:workspace` then `colima stop pairtask` | host | Full stop on macOS, keeps persistent files. | yes |

Back up before upgrading: [storage](sandbox-storage.md#back-up-and-restore).
Operator inbox backup and archive are tracked in #119.

## Inside the sandbox

Not host commands, listed so the split is clear: `codex`, `claude`,
`dashboard`, `pairtask status`, `pairtask budget …`, `docker …` run in your SSH
session; see the [README](../README.md#set-up-a-sandbox) block. A second
session of either agent starts with `pairtask start codex --id codex-2` or
`pairtask start claude --id claude-2` (optional `--model NAME`, `--effort
LEVEL`, and the agent's own arguments after `--`), which passes the ID to the
Codex launcher or writes a per-ID Claude MCP config under
`$XDG_CONFIG_HOME/pairtask/` (default `~/.config/pairtask/`); `--dry-run`
shows the command.

**Rooms** (#153): `pairtask start codex --id x --room pairtask` (repeatable)
joins rooms on registration, in addition to any the agent already has. From
`secured`, `pairtask operator room list|create R|add R AGENT|remove R AGENT`
manages rooms and `pairtask operator send --all --room R TEXT` posts to one.

**Pi** (#155): `pairtask start pi --id pi-1 [--room R]` starts Pi with the
pairtask extension; `pairtask join --id ID -- COMMAND` passes the adapter
environment to any other harness that integrates itself
([host integration](host-integration.md#pi)).

**Resume Codex** with `pairtask start codex --id codex-1 --resume` (#67). The
launcher records each agent ID's conversation and directory when a thread
starts, resumes or forks (`$XDG_STATE_HOME/pairtask/`, default
`~/.local/state/pairtask/`, private files named by a hash of the ID);
`--resume` continues that conversation from that directory. It refuses while
the ID is still connected (close the old session first), and when nothing is
recorded it lists saved conversations with a ready command for each, never
picking one itself. `-- resume` and `-- resume --last` mean the same as
`--resume` here (this agent's recorded conversation, not Codex's picker or
latest-in-directory, which don't work through the launcher). An explicit
`-- resume ID` (or a name, `--all`, `--cd`, `fork ID`) goes to Codex
unchanged, except that a pairtask session ID, the ID shown in pairtask
messages, is refused with an explanation: it isn't a Codex conversation ID.
