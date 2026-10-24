# Host commands

Everything you run on your own machine for the sandbox, by task. One command
does it all: `pairtask`, at `scripts/pairtask.py` in your provisioning
checkout (the one-line installer puts it on your PATH). Each `pairtask`
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
| `pairtask install` | host | First install: on macOS starts the Colima VM, then downloads and verifies the latest release, provisions the containers, configures SSH and lists them. Flags go to the rollout (`--release workspace-vX.Y.Z`, `--remote`, `--project`, `--storage-pool`, `--network`, `--tools`). | yes, if containers exist |
| `pairtask upgrade` | host | Download the release, stop the containers, replace them, keep projects and agent home. Same flags as install. Legacy: `python3 scripts/sandbox-provision.py rollout`. | yes |
| `pairtask download` | host | Fetch and verify a release image without provisioning (`--release`, `--from-zip`, `--from-dir`). Legacy: `python3 scripts/sandbox-download.py`. | no |
| `pairtask vm plan` / `pairtask vm apply` | host (macOS) | Preview or create and start the dedicated `pairtask` Colima VM that runs Incus. Legacy: `python3 scripts/sandbox-host.py plan|apply`. | no |
| `pairtask ssh` | host | Create the key-only SSH path into the workspace (`--remote`, `--instance`). The rollout does this for you. Legacy: `python3 scripts/sandbox-ssh.py --remote …`. | no |

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
| `ssh -F infra/incus/ssh/config workspace pairtask status` | workspace | Broker and agent inbox status inside the sandbox; expect `Broker: ready`. | no |
| `incus webui colima-pairtask:` (Linux: `local:`) | host | The Incus web UI, after the one-time setup in [sandbox.md](sandbox.md#incus-web-ui). | no |

## Shell and chat

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask shell` | host → workspace | SSH into the workspace as `agent` and restore your terminal afterwards; extra arguments run as a remote command. Raw: `ssh -F infra/incus/ssh/config workspace`. | no |
| `pairtask chat` | host → secured | The operator chat (`incus exec -t … pairtask operator chat`), terminal restored afterwards. [Chat guide](operator-chat.md). | no |
| `incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --to codex-1 "text"` | secured | One-shot operator message, receive or ack without the chat; refused while a chat is open. [Secured runtime](secured-runtime.md#operator-messages). | no |
| `reset` | host | Repairs a terminal left in mouse-reporting mode by a dropped session. | no |

## Mounts and sharing

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask mounts plan --mounts-file FILE` | host | Preview host directories mounted into the workspace. Legacy: `python3 scripts/sandbox-host.py mounts-plan`. | no |
| `pairtask mounts apply --mounts-file FILE` | host | Write the mount variables; then `pairtask plan` and `pairtask apply` to attach them. | yes, at apply |
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
session; see the [README](../README.md#set-up-a-sandbox) block.
