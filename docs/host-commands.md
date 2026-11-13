# Host commands

Everything you run on your own machine for the sandbox, by task. One command
does it all: `pairtask`, put on your PATH by the one-line installer below; run
it from any directory. The installer also writes `pk` as a short alias when no
other `pk` exists; it never replaces one it did not write.
`pairtask --dry-run <command> …` prints what would run. Contributors working
from a clone can run the same commands as scripts; see
[from a source checkout](sandbox.md#from-a-source-checkout-contributors-and-maintainers).

Where a command runs: **host** is your Mac or Linux machine; **workspace** is
the dev container you SSH into; **secured** is the offline control container
(when `secured_runtime = true`). "Ends sessions" means running agents and
shells in the containers are stopped.

Run every host command as your normal user. `pairtask`, `pk` and the
provisioning script refuse to run as root or under `sudo` before they read
or change anything: the host checkout and your pairtask state belong to
your user, and a root run would leave root-owned files behind and manage
the deployment from root's point of view. (Continuous integration sets
`PAIRTASK_ALLOW_ROOT=1` for its throwaway root-only runners; never set it
on a machine you keep.)

## Install and upgrade

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh \| sh` | host | The one-line install: checks Python 3.9+, Git, Incus (and Colima on macOS, offering the `brew install` line), fetches the latest release's host tooling into `~/.pairtask/host`, puts `pairtask` on your PATH, then runs `pairtask install --release <that tag>` (`pairtask upgrade` when already installed); run again later to upgrade both. `--no-provision` updates only the `pairtask` command, leaving the VM and containers alone. Options after `sh -s --`: `--version TAG`, `--no-provision`, `--no-modify-path`, `--yes`. Never run as root. | yes, if containers exist |
| `pairtask install` | host | First install: on macOS starts the Colima VM, then downloads and verifies the latest release, provisions the containers, configures SSH and lists them. Flags go to the rollout (`--release workspace-vX.Y.Z`, `--remote`, `--project`, `--storage-pool`, `--network`, `--tools`). | yes, if containers exist |
| `pairtask upgrade` | host | Download the release, stop the containers, replace them, keep projects and agent home. Same flags as install. It doesn't update `pairtask` itself (the installer does) or the Incus server (`vm incus-plan`). Restart agent sessions afterwards ([after an upgrade](sandbox.md#after-an-upgrade-restart-agent-sessions)). | yes |
| `pairtask version` | host | Which app release the containers run, which host tooling release this `pairtask` is, the latest published release, and the one manual command that would update them; it never upgrades anything. The running release is observed each time: each container's creation image and the build source baked into it must match exactly one published release, else it says `unknown` or `mixed`. The latest release is cached for 6 hours (`--refresh` asks GitHub now) and shown with its age. The command suggested is `pairtask upgrade` for an older app, or the installer for older host tooling, and never one that would go back to an older release; for a deployment named with `--project`, `--remote` or `--repo`, pass the same flags here to get its command. A deployment `pairtask upgrade` cannot target (a remote other than `local` or the Colima one, or a project name other than letters, digits, `-` and `_`) is still reported, with no upgrade suggested. `--json` prints the report. It also hands the result to the chat, which shows it in the footer and under `/version`; `pairtask chat` does so from cached data before it opens and refreshes it in the background, and `pairtask upgrade` reports what runs afterwards. | no |
| `pairtask size show` | host | How big the sandbox is and how full: the macOS VM's CPU, RAM and disk, the Incus pool, the workspace's CPU, RAM and root disk, and each volume with the share it uses; a volume at 90% or more is flagged. It only reads. `--json` prints the report. [Resize](sandbox-storage.md#resize). | no |
| `pairtask size plan --memory 6 --agent-home 10` then `pairtask size apply` | host | Grow the workspace's CPU, RAM or root disk, or a volume's quota. `plan` refuses shrinking, values out of range, CPU or RAM the VM can't give alongside the other containers, and sizes another tfvars file sets; then it records them in the deployment's `size.auto.tfvars.json` and previews the change, which `apply` carries out. [Resize](sandbox-storage.md#resize). | yes |
| `pairtask pairing on` / `pairtask pairing off` | host | Turn the experimental pairing route on or off in the deployment's settings, then run the same rollout as upgrade (same flags). `on` also turns on `secured_runtime`; it is refused while `peer_connector_probe` is on. `on` publishes the relay on every host address (LAN and tailnet) on `--host-port N` (1024–65535, or 0 for none), else the saved port, else 7445; `off` keeps the port for the next `on`. `on --name NAME` sets the name this workspace pairs under (at most 64 bytes; `""` clears it; without it the saved name stays), shown to workspaces paired from then on; the rollout keeps the secured broker down until the name it set is in place (a direct `tofu apply` doesn't). Settings are checked before anything is written or stopped. Later upgrades keep the setting. | yes |
| `pairtask pairing status` | host | Show the pairing route and its host port as configured and as deployed, read-only: `on`, `off`, or which command finishes a change an interrupted rollout left behind. It reports applied forwarding, not whether another machine can reach it. | no |
| `pairtask discovery run` | host | The discovery helper that `pairing on` runs as a user service (launchd on macOS, `systemd --user` on Linux): asks the secured broker for its lease, then announces this machine's pairing port on the local network or looks for paired workspaces that moved, and hands the endpoints to the broker, which moves a contact only when its pinned identity answers. While the operator keeps an invitation open with `/pair open --discoverable`, the announcement also carries this workspace's name; while they run `/pair find`, the helper reports the named invitations it hears and the online tailnet devices with each lease ([pair two machines](pairing.md#2-on-a-open-an-invitation)). `--once` runs one round and exits; `--interval 15s` (the idle poll; a changed report or an open find comes back within seconds), `--host-port N` (0: never announce), `--project`, `--remote`. `pairing on --no-discovery` turns the service off and keeps it off; `pairing off` removes it ([moving between networks](pairing.md#moving-between-networks)). | no |
| `pairtask classifier on` | host | Turns the shadow [classifier](classifier.md) on with your Jev (default) or Clef (`--provider clef --account ID`) key, from a hidden prompt or `--key-file PATH`; needs the pairing route. `pairtask classifier off` removes the key; `pairtask classifier status [--since 24h] [--json]` shows the settings and what was recorded. | no |
| `pairtask operator classifier report` | host | What the shadow [classifier](classifier.md) recorded, read-only: outcome counts, reasons, models, provider latency, the providers' usage counters summed (token counts as a cost proxy; no prices), agreement with the reply claim's holder (a weak label) and the newest predictions; `--since 24h` (up to 720h), `--json`. Nothing in it is applied anywhere. | no |
| `pairtask notify on --slack` | host | Turns [Slack notifications](slack-notifications.md) on with a Slack incoming webhook URL, from a hidden prompt or `--secret-file PATH`; `--hidden` names only the workspace; needs the pairing route. `pairtask notify off` removes the URL; `pairtask notify status [--json]` shows the settings, never the URL. | no |
| `pairtask discovery status` | host | What the discovery helper last did, read-only: the broker's lease, what it announced and under which name, how it looks for others (the system Bonjour service on macOS, its own multicast socket on Linux), the invitations and tailnet devices it last reported, and the outcome for each endpoint it handed over (`--json` for the file). Nothing in it is secret. | no |
| `pairtask download` | host | Fetch and verify a release image without provisioning (`--release`, `--from-zip`, `--from-dir`). | no |
| `pairtask vm plan` / `pairtask vm apply` | host (macOS) | Preview or create and start the dedicated `pairtask` Colima VM that runs Incus. | no |
| `pairtask vm incus-plan --manifest FILE` / `pairtask vm incus-apply --manifest FILE --yes` | host (macOS) | Plan, read-only, then apply an upgrade of the VM's held Incus packages ([sandbox guide](sandbox.md#upgrade-the-vms-incus)). Apply stops Incus, backs up `/var/lib/incus`, installs exactly the planned versions and restores the holds and units it found. Only self-contained `dir` storage is supported: on Colima's default ZFS pool the automatic upgrade is not available yet. | no, but it stops Incus while it runs |
| `pairtask ssh` | host | Create the key-only SSH path into the workspace; the remote defaults to your OS (`colima-pairtask` on macOS, `local` on Linux), `--remote` and `--instance` override. The rollout does this for you. | no |

Prerequisites the installer cannot bundle: Incus (and Colima on macOS), Python
3.9+, Git, OpenSSH. macOS: `brew install colima incus python git`. Linux:
[install Incus](https://linuxcontainers.org/incus/docs/main/installing/) with a
quota-capable pool and a managed bridge. The Incus server must be 7.0.x ≥ 7.0.2
or 7.5+ ([why and how to check](sandbox.md#incus-server-version)). Details:
[sandbox setup](sandbox.md).

## Connect two machines

Step by step: [pair two machines](pairing.md).

## Start, stop and status

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask start` | host | On macOS starts the VM, then starts `secured` (if present) and `workspace`, one Incus call each. `--remote`, `--project`. | no |
| `pairtask stop` | host | Stops `workspace`, then `secured`; `--vm` also stops the macOS VM. | yes |
| `pairtask status` | host | Lists the containers and their state (`incus --project pairtask list`), then one line on the running and latest releases from cached data (no network; see `pairtask version`). | no |
| `pairtask shell pairtask status` | workspace | Broker and agent inbox status inside the sandbox; expect `Broker: ready`. `pairtask shell --tty pairtask dashboard` opens the dashboard. | no |
| `incus webui colima-pairtask:` (Linux: `local:`) | host | The Incus web UI, after the one-time setup in [sandbox.md](sandbox.md#incus-web-ui). | no |

## Shell and chat

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask shell` (short: `pairtask sh`, or `pk sh`) | host → workspace | SSH into the workspace as `agent` and restore your terminal afterwards; extra arguments run as a remote command, with a terminal when `--tty` is given. Raw: `ssh -F ~/.pairtask/host/infra/incus/ssh/config workspace`. | no |
| `pairtask chat` | host → secured | The operator chat (`incus exec -t … pairtask operator chat`), terminal restored afterwards. [Chat guide](operator-chat.md). | no |
| `pairtask identity init` / `pairtask identity status` | host → secured | Make your signing keys, once, or show them and the folder they are in (`pairtask operator identity …` in secured). The keys stay on secured's state volume and survive upgrades; `init` refuses while keys exist. [Signed messages](operator-chat.md#trust-model). | no |
| `pairtask secured` | host → secured | A root shell in the secured runtime, where operator commands such as `pairtask operator send` run; terminal restored afterwards. With a command (`pairtask secured pairtask operator receive`) it runs that one command, with a terminal when `--tty` is given. | no |
| `incus --project pairtask exec colima-pairtask:secured -- pairtask operator send --to codex-1 "text"` | secured | One-shot operator message, receive or ack without the chat; refused while a chat is open. [Secured runtime](secured-runtime.md#operator-messages). | no |
| `reset` | host | Repairs a terminal left in mouse-reporting mode by a dropped session. | no |

## Mounts and sharing

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask mounts plan --mounts-file FILE` | host | Preview host directories mounted into the workspace. | no |
| `pairtask mounts apply --mounts-file FILE` | host | Write the mount variables; then `pairtask plan` and `pairtask apply` attach them. On macOS: `pairtask stop --vm`, `pairtask mounts apply …`, `pairtask start`, then `pairtask plan` and `pairtask apply`; see the [apply sequence](sandbox-mounts.md#apply). | yes, at apply |
| `pairtask share plan` / `pairtask share setup` / `pairtask share run` (`--mounts-file FILE`) | host (macOS) | Writable sharing through a dedicated local account: preview, create the account and ACLs (asks for sudo), run the helper. | no |

Details and the `mounts.json` format: [mounts](sandbox-mounts.md).

## Recovery, backup and removal

| Command | Runs on | What it does | Ends sessions |
| --- | --- | --- | --- |
| `pairtask plan` / `pairtask apply` | host | Preview and apply a provisioning change: `pairtask plan --replace` replaces the dev container keeping data; `pairtask plan --destroy` plans deletion (volume guards must be removed first). | yes, when replacing or destroying |
| `pairtask recover` | host | Repair provisioning state without changing data. | no |
| `pairtask tools` | host | Retry the pinned agent tools install in running containers. | no |
| `pairtask stop --vm` | host | Full stop on macOS: the containers, then the VM; keeps persistent files. | yes |

Back up before upgrading: [storage](sandbox-storage.md#back-up-and-restore).
Backing up or archiving the operator inbox isn't supported yet.

## Inside the sandbox

Not host commands, listed so the split is clear: `codex`, `claude`,
`dashboard`, `pairtask status`, `pairtask budget …`, `docker …` run in your SSH
session; see the [README](../README.md#set-up-a-sandbox) block. A second
session of either agent starts with `pairtask start codex --id codex-2` or
`pairtask start claude --id claude-2` (optional `--model NAME`, `--effort
LEVEL`, and the agent's own arguments after `--`), which passes the ID to the
Codex launcher or writes a per-ID Claude MCP config under
`$XDG_CONFIG_HOME/pairtask/` (default `~/.config/pairtask/`) and passes
Claude the installed [tool permissions](operator-chat.md#trust-model);
`--dry-run` shows the command.

**Rooms**: `pairtask start codex --id x --room pairtask` (repeatable)
joins rooms on registration, in addition to any the agent already has. From
`secured`, `pairtask operator room list|create R|add R AGENT|remove R AGENT|delete R`
manages rooms and `pairtask operator send --all --room R TEXT` posts to one.

**Pairing** (experimental): from `secured`, `pairtask operator pair [list]`,
`open`, `status [OPERATION_ID]`, `cancel [INVITATION_ID]`, `accept|reject KEY`
and `redeem DESTINATION` manage pairing. `open` prints a one-time 6-digit code
on stdout only; the broker keeps it in memory and never stores, logs or audits
it. `accept` and `reject` take a key prefix and send the full key only when it
matches exactly one listed request.

The exchange is CPace (`the-sarge/cpace` v0.1.3), an unaudited implementation
of an Internet-Draft: experimental, not production security. Confirmed
contacts can exchange operator messages in their workspace conversations.

`redeem DESTINATION` reads the other operator's code from the terminal, or
from stdin when piped, never from the command line. It checks the broker can
redeem before reading the code, records the redemption under a stable ID
before dialing, then follows it until it settles or `--wait` (45s) ends.
Repeating a request returns the same redemption; the first code wins, since
codes are never stored. `awaiting_acceptance` means the inviter recorded it
and has yet to decide; it is not a contact. `outcome_unknown` means our final
confirmation may have arrived without a receipt.

Once the inviter decides, the joiner's broker settles the redemption. It
dials the same destination; the connection is TLS pinned to the inviter's
fingerprint, with ALPN `pairtask-contact/1`, and only an identity with a
pairing request there is accepted. Over it, the joiner's broker asks what
became of its exact redemption, records the decision and acknowledges it.
The inviter's broker records the delivery, and only then is the contact
confirmed on both sides. The broker settles in the background every 5
minutes, for redemptions up to 7 days old. Each redemption gets at most 12
automatic passes, and failures back off from 1 minute to 1 hour.

`pairtask operator pair settle [OPERATION_ID] [--wait 45s]` queues one
redemption, or every one that needs it, at once, and resets its automatic
budget. With an ID it follows that redemption, and exits 0 only once its
decision is confirmed. `status` lists contacts, each `confirming` until both
sides confirmed it. An accepted request shows `accepted, awaiting peer
confirmation` until the joiner acknowledged it. In chat, each contact has
its own `⇄ workspace` conversation, keyed by its fingerprint, never by the
name it chose. Select that conversation to send operator messages and read
replies. `/pair settle [OPERATION_ID]` works as above, and chat follows
background changes as they happen.

Broker settings, all off by default:

- `PAIRTASK_PAIRING_SOCKET=PATH` accepts pairing connections on a Unix socket.
  Its directory must be the broker user's with mode 0700; the socket is 0600.
  An existing path is never removed: delete a stale socket yourself.
- `PAIRTASK_PAIRING_SOCKET_GROUP=NAME`, with the socket above, shares it with
  one group so connector's relay can connect: the directory must be the
  broker user's with group NAME and mode 0750, and the socket is 0660. The
  broker needs NAME as a supplementary group. The group may only connect;
  every connection must still complete TLS and the pairing exchange.
- `PAIRTASK_PAIRING_EGRESS=direct` lets `redeem` dial TCP from this broker,
  for a standalone broker only. `PAIRTASK_PAIRING_EGRESS=socket:/ABS/PATH`
  sends each redemption through connector's egress forwarder instead, for a
  `secured` broker with no network.
- `PAIRTASK_PAIRING_NAME` is the name this workspace pairs under
  (`workspace` by default).

A shared pairing or egress socket's directory must neither be, contain nor
sit inside the agent socket's, the operator socket's, the database's (its
configured and its real location) or the key's directory; the broker
refuses to start otherwise.

On connector, two commands carry pairing traffic without any key or any
view of its contents, since TLS and the pairing exchange run end to end
between the brokers. Neither starts by default:

- `pairtask peer relay --listen ADDR --dial-unix /PATH` forwards inbound
  pairing connections, sealed, to the broker's shared pairing socket.
- `pairtask peer egress --socket /PATH --group NAME` serves the broker's
  outbound redemptions on a socket it creates (directory 0750, socket 0660,
  an existing path never removed). Per connection the broker names one
  `host:port`; the forwarder resolves it once and refuses it if any address
  is loopback, link-local, multicast, unspecified, zoned or one of
  connector's own interface addresses, then dials that address. Private LAN
  and tailnet addresses are allowed: they are where other workspaces live.

Both take `--max-conns`, `--max-bytes` and `--idle` (16 connections, 1 MiB
per direction and 30 s by default).

`pairtask pairing on` installs the route in the Incus deployment: it sets
`pairing_route = true` (which needs `secured_runtime`, and can't be on
together with the validation-only `peer_connector_probe`) and rolls out.
The setting lives in the deployment's `operator.auto.tfvars.json`, or the
one JSON variables file that already sets these controls; the command
refuses to guess when they are split across files or set in HCL. It adds a
`connector` container on the dev bridge and two one-way volumes:

- `/mnt/pairtask-pairing`: read-write in secured (the broker's pairing
  socket), read-only in connector;
- `/mnt/pairtask-egress`: read-write in connector (the egress socket),
  read-only in secured.

secured keeps no network. The image's units start only in their role:
`pairtask-pairing-setup` (root) prepares secured's side and the broker's
route settings in `/run/pairtask/pairing.env`. In connector,
`pairtask-connector-setup` (root) prepares the egress volume, and
`pairtask-peer-egress` and `pairtask-peer-relay` (listening on port 7445)
run as the `peer` user (UID 1052, group `collab-peer`, GID 1050). The apply
refuses to turn the route on, off or change it while secured or connector
runs; `pairtask pairing on|off` stops workspace, connector and secured, in
that order, first. CI proves the installed route in a
disposable project with redemptions both ways, crash restarts included.

**Publishing** (`pairing_host_port`): with a port P set (default 0, none;
`pairtask pairing on` sets 7445 unless told otherwise), connector gets one
Incus proxy device, `pairing-relay`, that listens on `tcp:0.0.0.0:P` on the
host side and connects to the relay's 7445 inside connector (`bind=host`,
`nat=false`). It is a real listening socket, so on macOS Colima's port
forwarding also publishes it on every Mac address; that needs the managed
VM's `network.address = false`, which `pairtask vm` pins and checks.

- Anyone who can reach the host on P can connect, but each connection must
  still complete TLS and the pairing exchange, within the relay's limits.
  secured keeps no network.
- While the route is on, the project allows proxy devices
  (`restricted.devices.proxy = allow`, even with port 0). Only the host
  operator holds the Incus API, so neither dev nor secured can add one.
- Turning the route off removes connector, its device and the allowance. The
  two socket volumes live as long as `secured_runtime` and are detached while
  the route is off, so the same apply never deletes a volume secured still
  uses. They may keep stale socket entries; each unit replaces its own when it
  starts.
- macOS may ask whether to accept incoming connections for the forwarder, and
  a firewall or tailnet ACL may still block P. Status shows applied
  forwarding, not reachability.

With the socket or an egress set, the broker loads or creates its workspace
key at startup and marks redemptions interrupted before verification
`unverified`.

The workspace identity is an Ed25519 key in `<DB_PATH>.peer/identity.key`
(0600, in a 0700 directory). Its fingerprint, `sha256:` and the 64 lowercase
hex digits of the SHA-256 digest of the key's SubjectPublicKeyInfo DER, shows
in `status` and `/pair status`, so two operators can compare fingerprints by phone.
Certificates made from the key can be renewed without changing it. The
database records the fingerprint, so a missing, unreadable or different key is
reported (`identity_missing`, `identity_corrupt`, `identity_unsafe`,
`identity_mismatch`) and never silently replaced: restore the key from a
backup. While it is unavailable, `open` and `redeem` are refused. Storage is
bounded: 64 requests awaiting a decision, 1024 identities, 4096 pairing
records, and 64 open of 1024 redemptions. A full store refuses new ones and
never evicts.

The colleague entering the code records their consent, and the inviting
operator accepts once. Neither side reports the other as confirmed before a
durable acknowledgement, a rejected request is never revived, and nothing is
accepted automatically.

**Pi**: `pairtask start pi --id pi-1 [--room R] [--max-tokens N]`
starts Pi with the pairtask extension, capping each request's output at
32768 tokens unless `--max-tokens` says otherwise (0: no cap); `pairtask join --id ID -- COMMAND` passes the adapter
environment to any other harness that integrates itself
([host integration](host-integration.md#pi)).

**Resume Codex** with `pairtask start codex --id codex-1 -- resume` (close
the old session first). The launcher remembers each agent ID's last
conversation and directory, and continues it; `-- resume --last` does the
same, and so does the `--resume` flag. With nothing recorded it lists saved
conversations with a ready command for each. An explicit `-- resume ID`,
`fork ID` or other Codex arguments go to Codex unchanged; a pairtask session
ID is refused, since it isn't a Codex conversation ID. **Resume Claude** with
`pairtask start claude --id claude-1 -- --resume`, which passes Claude's own
`--resume` through.

### Classifier (shadow mode)

Off by default. Records which agent a model thinks an untagged message to
everyone was for, and applies nothing. Setup, what leaves the host and the
settings for a standalone broker: [Jev classifier guide](classifier.md).

### Notifications (Slack)

Off by default. Tells you in Slack when an agent needs your decision; you
answer in the chat. Slack setup, options, what leaves the host and the
settings for a standalone broker: [Slack notifications guide](slack-notifications.md).
