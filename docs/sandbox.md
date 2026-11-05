# Set up an Incus sandbox

**Install → SSH → sign in.** Setup and maintenance commands run in a host
terminal, from any directory, unless a section says they run inside the
workspace; with the installed `pairtask` command (`pk` for short; use
`pairtask` if the installer skipped `pk` because another one exists).
Already provisioned? Use the [daily quick start](../README.md#set-up-a-sandbox).
Working from a clone of this repository? See
[from a source checkout](#from-a-source-checkout-contributors-and-maintainers).

The image includes collab-ai, Codex, Claude Code, Go, Git, tmux, Starship and RTK.
Defaults: 2 CPUs, 4 GiB RAM, 10 GiB root disk, 10 GiB projects, 2 GiB agent home.
Projects and agent home persist; tools live on the replaceable root disk.

## 1. Install the host tools

**macOS** — Homebrew required:

```sh
brew install colima incus python git
```

pairtask runs Incus in its own `pairtask` Colima VM. Existing Docker profiles
stay separate. Mac requires Colima 0.10.3+.

**Linux** — [install Incus](https://linuxcontainers.org/incus/docs/main/installing/),
Python 3.9+, Git and OpenSSH. Use an existing
quota-capable pool (ZFS/Btrfs), a managed network bridge and Incus access.
Skip Colima; replace `colima-pairtask:` with `local:` throughout.
Do not reinitialize an existing server.

The Incus **server** must be 7.0.2+ (7.0 LTS) or 7.5+;
[check it](#incus-server-version).

## 2. Install or upgrade

Exit agents first. The one-line installer puts `pairtask` and `pk` on your
PATH (under `~/.pairtask`), starts the macOS VM and installs the workspace.
Open a new terminal afterwards.

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
```

Or step by step, previewing the VM first:

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh -s -- --no-provision
# open a new terminal so `pk` is on your PATH
pk vm plan       # macOS: preview the VM, read-only
pk vm apply      # macOS: create and start it; starting the VM doesn't create the workspace
pk install       # starts the VM if needed, installs the workspace, lists the containers
```

`--no-provision` only installs or updates the `pairtask` command; it doesn't
touch the VM or the containers.

To upgrade later, see [upgrades](#upgrades): run the installer again to
update both `pairtask` and the workspace, or `pk upgrade` for the workspace
alone.

Install and upgrade download the latest tested release for your server, verify it, stop existing
containers, then provision and configure SSH. No GitHub CLI, browser download,
or host Terraform/OpenTofu install. To select a version, add `--release workspace-vX.Y.Z`.
Projects and agent home persist; active processes end. [Back up before upgrading](sandbox-storage.md#back-up-and-restore).

New deployments include dev and offline control. State and settings stay on the host;
`pairtask` prints and remembers their directory. Existing settings are preserved.
Use `--storage-pool` / `--network` for a non-default Incus pool/bridge.
[Settings, plan preview and recovery](sandbox-operator.md).

## 3. SSH into the workspace

On the host:

```sh
pk shell        # restores your terminal after; short: pk sh
```

The raw equivalent is `ssh -F ~/.pairtask/host/infra/incus/ssh/config workspace`.

You enter as `agent`, without sudo, in `/workspace`. The helper creates a dedicated
host login key and copies **only its public key**. It never forwards your SSH agent.
SSH uses `incus exec`, works offline and opens no network SSH listener.
A session that drops while a full-screen program (an agent harness, the chat)
runs can leave your terminal in mouse-reporting mode; the wrapper restores it,
and `reset` fixes a terminal that is already in that state.

[Sign in below](#sign-in-and-network-access), then use the
[README commands](../README.md#set-up-a-sandbox) to clone, start/resume and reconnect.

| Inside an interactive SSH/tmux shell | Behavior |
| --- | --- |
| `codex` | Managed terminal, ID `codex-1` |
| `claude` | Preconfigured channels, ID `claude-1` |
| `dashboard` | Same broker's read-only terminal dashboard |
| `command codex` / `command claude` | Native CLI for login/setup |

Use one session per ID; extra sessions need separate identities. Scripts use native
binaries, not these aliases. The image installs the `collab-ai` skill for both
agents from `/usr/local/share/pairtask/SKILL.md`; ask them to use it when they
start collaborating. Copy only `SKILL.md` and `references/` when installing the
skill elsewhere.

Starship settings go in persistent `~/.config/starship.toml`. Older containers need
an [image update](sandbox-storage.md#replace-dev-retain-data).

**RTK reduces shell output** for Codex and Claude (hooks) and Pi (an extension
in `~/.pi/agent/extensions/`), set up on first boot; a sandbox from before Pi
was wired gets Pi's once on its next boot. Approve the RTK hook if Codex
prompts. `rtk gain` shows estimated output savings, not provider billing or a
token cap. History stays in `~/.local/share/rtk/` across container
replacement; `/etc/codex/config.toml` grants it workspace-write access through
the App Server. User/project configuration can override that default.
Existing settings are preserved; telemetry is not enabled. To opt out, run
`rtk init -g --codex --uninstall`, `rtk init -g --uninstall --auto-patch` and
`rtk init -g --agent pi --uninstall`, then restart agents; setup never
reinstalls what you removed. For Pi, `RTK_DISABLED=1` turns it off for one
session.

## Upgrades

Four different things can be upgraded, each with its own command:

| To upgrade | Run | What changes |
| --- | --- | --- |
| `pairtask` and the workspace | the [one-line installer](#2-install-or-upgrade) again | Updates the `pairtask` command first, then replaces the containers like `pk upgrade`. Add `--no-provision` to update only the command. |
| The workspace | `pk upgrade` | Downloads the release and replaces the containers' images and tools, with the `pairtask` you have. Keeps projects and agent home; ends sessions. |
| The Incus server in the macOS VM | `pk vm incus-plan`, then `pk vm incus-apply` | Only when the server is too old: [upgrade the VM's Incus](#upgrade-the-vms-incus). |
| The Mac's Incus client | `brew upgrade incus` | The `incus` command on your Mac only, never the server in the VM. |

On Linux the server is your host's Incus: upgrade it from the repository you
installed it from.

### Incus server version

The Incus **server** must be 7.0.x at 7.0.2 or later, or 7.5 or later
(7.0.x is the LTS track). `pk install`, `pk start`, `pk vm apply` and
`pk upgrade` check the server's own version and stop on any other, naming it,
the requirement and the way forward; `pk plan` only warns. Older servers (6.0.x, 7.0.0–7.0.1, 7.1–7.4) can give two containers
created at the same moment overlapping host UID/GID ranges, which weakens the
file-permission separation between `secured` and `workspace` (fixed upstream
in Incus 7.0.2 and 7.5). Check the
server line, not the client:

```sh
incus version colima-pairtask:     # Linux: incus version local:
```

- **Stay on your track.** On 6.0 LTS, move to the fixed 7.0 LTS (7.0.2+). On
  7.1–7.4, upgrade forward to 7.5+; don't move to 7.0 LTS, because Incus
  can't downgrade its database.
- **Colima holds Incus.** Its VM image installs Incus from zabbly `stable` and
  `apt-mark hold`s the Incus packages, so `apt upgrade` in the VM skips them,
  and `brew upgrade incus` updates the Mac client, not the VM's server.
  [Upgrade the VM's Incus](#upgrade-the-vms-incus) deliberately, with the
  sandbox stopped and Incus backed up.
- **Upgrading alone doesn't repair an existing overlap.** After upgrading,
  check that the existing containers' ranges don't overlap:

  ```sh
  K=volatile.idmap.current
  incus --project pairtask config get colima-pairtask:secured $K
  incus --project pairtask config get colima-pairtask:workspace $K
  ```

  Each entry maps container IDs to host IDs `[Hostid, Hostid + Maprange)`.
  The two containers' UID ranges must not overlap, and the same for their GID
  ranges. If they do overlap, ask before repairing anything.

### Upgrade the VM's Incus

macOS only. pairtask can upgrade the Colima VM's held Incus packages for you,
with a read-only plan first and then an apply.

**Check your storage pool first.** Automatic apply supports only a `dir` pool
stored under `/var/lib/incus`. Colima's Incus runtime creates a ZFS pool named
`default`, and pairtask uses it, so **on a default setup the automatic upgrade
is not available: the plan stops with the storage blocker**. Check yours:

```sh
incus storage list colima-pairtask:     # the DRIVER column
```

If it isn't `dir`, there is no validated upgrade path in these docs yet; a
tested procedure for the default ZFS pool is tracked as a follow-up. Until
then, don't upgrade the packages by hand from this guide: the Incus
[backup guide](https://linuxcontainers.org/incus/docs/main/backup/) explains
what a manual upgrade has to protect, and you can still run steps 1–6 to see
exactly what blocks it.

Using a `pairtask` from before `pk vm incus-plan` existed? Update the command
first without touching the containers, run the steps, then upgrade the
workspace:

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh -s -- --no-provision
# steps 1–8 below
pk upgrade
```

Running the installer without `--no-provision` is safe too: it updates
`pairtask` before it provisions, and on an old server the provisioning step
stops at the version check before it changes anything.

In a host terminal:

1. **Check that you need it.** Run `incus version colima-pairtask:`. If the
   `Server version` line is 7.0.2–7.0.x or 7.5+, stop here.
2. **Exit agent sessions**, and close `pk chat` and any `pk shell`.
3. **Stop the containers, not the VM:** `pk stop`. It stops `workspace` and
   `secured` only; don't add `--vm`, since the plan reads the running VM. If
   the VM is already stopped, `pk vm apply` starts it; on an old server it
   then reports the version error, which is expected here, and the VM stays up.
4. **List every instance:** `incus list colima-pairtask: --all-projects`.
   Any instance in any project that isn't `STOPPED` blocks the plan. Note
   which ones are running now: you start exactly those again in step 8. With
   pairing on, stop `connector` too:
   `incus --project pairtask stop colima-pairtask:connector`. Stop anything
   else by hand only if you own it and are sure:
   `incus --project PROJECT stop colima-pairtask:NAME`.
5. **Plan:** `pk vm incus-plan --manifest ~/incus-upgrade.json`. It changes
   nothing in the VM and writes that manifest file itself; don't create it.
   It refuses to overwrite an existing file, so remove an old manifest first
   (`rm ~/incus-upgrade.json`) or pick a new name.
6. **Read the blockers** it prints after `Blocked:` (listed below). Fix what
   you can, remove the manifest and plan again: after any change in the VM,
   only a fresh plan is valid.
7. **Apply that exact manifest:**
   `pk vm incus-apply --manifest ~/incus-upgrade.json --yes`. Incus is stopped
   while it runs.
8. **Start and check:** `pk start` (it starts `secured` and `workspace`). If
   `connector` was running in step 4, start it again with
   `incus --project pairtask start colima-pairtask:connector` and check
   `pk pairing status`. Start any other instance you stopped, and only those.
   Then `pk status`, `incus version colima-pairtask:` for the new server
   version, and the [idmap overlap check](#incus-server-version).

The plan reads the server version, every instance in every project, the
storage pools, the Incus packages (installed, candidate and origin), the
exact apt transaction, the holds and the units. It writes them to the
manifest and lists anything that blocks the upgrade:

- an instance in any project that isn't stopped;
- a pool that isn't a `dir` pool physically inside `/var/lib/incus` (ZFS, LVM
  and Btrfs pools, Colima's default among them, need their own backup and a
  manual upgrade);
- `/var/lib/incus` that isn't a plain directory;
- an Incus unit in a state apply couldn't put back;
- a candidate older than what's installed, not from zabbly stable, or not a
  supported server version;
- a transaction that removes anything;
- an existing `/usr/sbin/policy-rc.d`;
- too little space in `/var/backups`.

Apply refuses a manifest with blockers, re-reads all of it and stops unless
it still matches the plan. Then it:
1. stops Incus;
2. backs up `/var/lib/incus` (and `/etc/subuid`, `/etc/subgid`) to
   `incus.tar.gz` in a new directory `/var/backups/pairtask-incus-<random>/`, and
   checks the archive;
3. installs exactly the planned versions, with service starts held off;
4. puts back the holds and the units' states;
5. checks the server reports the planned version.

It never restores, downgrades or deletes anything itself; on a failure it
names the step and the backup. To restore that backup, follow the Incus
[full server backup](https://linuxcontainers.org/incus/docs/main/backup/)
guide inside the VM (`colima ssh -p pairtask`): stop Incus, replace
`/var/lib/incus` and the subuid/subgid files from the archive, and start it.

## Docker and Compose

Docker Engine, Compose and Buildx are installed into the workspace by the
rollout when the `docker` tool is enabled (the default; see
[sandbox tools](sandbox-tools.md)). In `workspace`:

```sh
docker info                  # Security Options includes rootless
docker compose up -d         # from your project with compose.yaml
docker compose down          # keeps named volumes; -v deletes them
systemctl --user status docker
```

The daemon runs as `agent`; its socket is `/run/user/1001/docker.sock`. No host
Docker socket or Docker-group root access is provided. Colima stays on the Mac.
Docker data persists on its own [10 GiB volume](sandbox-storage.md).
Published ports belong to `workspace`, not your host's localhost.

Dev enables Incus nesting and a larger isolated UID range. Packaged AppArmor
profiles allow Docker and Codex's Bubblewrap namespaces without disabling host
AppArmor restrictions. `secured` keeps nesting disabled and has no Docker daemon.
Stop/start Docker with `systemctl --user stop docker` / `systemctl --user start docker`.
To keep it off after restart, use `systemctl --user disable --now docker`.

## Bundled MCP tools

Both agents get CodeScene and Playwright automatically; inspect them with `/mcp`.
Playwright includes headless Chromium with its browser sandbox enabled. Each
connection has separate, temporary cookies; `localhost` means the workspace.

For CodeScene, create a [personal access token](https://codescene.io/users/me/pat),
then run `pairtask-codescene-login` inside workspace before starting agents. The
hidden prompt saves it privately in persistent agent home. No host login is copied.
Use the token rather than `cs-mcp auth` there: its browser OAuth flow needs a
callback the headless workspace is not configured to receive
([troubleshooting](troubleshooting.md)).
Analysis requires a valid CodeScene account/license; installation does not provide one.

These MCPs run as `agent` inside workspace, outside the agents' shell-command
sandbox. They receive no Incus admin socket. GitHub uses the installed `gh` CLI.

## Sign in and network access

Before starting agents, sign in inside the container:

```sh
command codex login --device-auth
command codex login status
command claude auth login
```

Follow the CLI instructions in your host browser. Enable Codex device login in
your account/workspace if required. If the browser fails at `localhost:1455`, exit
the Codex sign-in screen and run `command codex login --device-auth` above.
SSH forwarding is disabled; [device login](https://learn.chatgpt.com/docs/auth#login-on-headless-devices)
avoids the host-to-container callback.
Claude channels also require consent and account/organization support.
Credentials saved in `/home/agent` persist; host credentials are not imported.

`gh` is preinstalled. For GitHub CLI access, run `gh auth login --web --git-protocol https`
inside workspace and follow the printed browser instructions. Check with
`gh auth status`. A GitHub MCP connector login does not authenticate `gh`.

**Dev is online by default; control has no NIC.** To disable dev networking, set
`dev_network_enabled = false` in the existing deployment variables and run
`pk plan`, then `pk apply`. This interrupts network connections; SSH and the broker still work.
Set `true` and apply to restore access. For another project on the same server,
select a different bridge: instances named `workspace` cannot share its managed DNS.

## Status and recovery

Run on the host:

```sh
pk status        # same as: incus --project pairtask list colima-pairtask: (Linux: local:)
```

Expect `workspace` (and optional `secured`) to be `RUNNING`; allow a few seconds
for the broker. **Keep the colon.** The remote is `colima-pairtask:`, not `pairtask:`.

| Problem | Action |
| --- | --- |
| Remote missing | Check `colima list`; run `pk vm apply` |
| Empty list / missing project | Check `incus project list colima-pairtask:`; run `pk install` |
| SSH host key changed | Follow the [replacement procedure](sandbox-storage.md#replace-dev-retain-data) |
| Need a recovery shell | `incus --project pairtask exec colima-pairtask:workspace -- /bin/sh` (root) |

## Stop or remove

On the host; these retain data:

```sh
pk stop          # stops workspace, then secured
pk stop --vm     # macOS: also stops the pairtask VM
```

`pk start` starts them again (and the VM on macOS); see the
[daily block](../README.md#set-up-a-sandbox). Containers do not
autostart with the VM. Set `running = false` in deployment variables to keep them
stopped across applies; otherwise an apply may start them.
For deletion, follow [back up and remove](sandbox-storage.md#deliberate-removal).

## Incus web UI

On the host, with Incus running:

```sh
incus webui colima-pairtask:  # Linux: incus webui local:
```

Open the printed URL, select project `pairtask`, and keep the command running.
The URL contains a temporary login token; keep it private. Ctrl+C closes the UI
proxy without stopping containers. Use `pk plan` and `pk apply` for managed changes to avoid drift.

If `webui` is unknown, update the Incus client (`brew upgrade incus` on Mac).
Missing UI assets belong on the **Incus server**, not in the workspace.
On Debian/Ubuntu with the [Zabbly repository](https://github.com/zabbly/incus#other-packages):

```sh
# Mac: install in the dedicated VM only if missing
colima ssh --profile pairtask -- sudo apt-get update
colima ssh --profile pairtask -- sudo apt-get install incus-ui-canonical
# Linux: run apt-get update/install incus-ui-canonical on the Incus server
```

For other distributions, use their Incus UI package instructions.
[Agent dashboard](dashboard.md) is a separate terminal UI.

## Security: can agents execute code on my host?

Commands run inside the container, including scripts from host mounts. No host
shell, sudo or SSH-agent forwarding is provided. Writable mounts allow changes
that **you or host automation may later execute**; review those changes.

Dev has general network access, including reachable host/LAN services—not a provider
allowlist. No port forwards are added; NAT is not an ingress firewall. Keep host
credentials and administrative sockets out. Containers share the Linux server's
kernel (Colima's VM on Mac), so isolation is not absolute.
[Incus security](https://linuxcontainers.org/incus/docs/main/explanation/security/).

<details>
<summary>Isolation, repeatability, and validation details</summary>

The Mac host uses [colima.json](../infra/incus/colima.json): 4 CPUs, 8 GiB RAM,
40 GiB disk, no host mounts by default, no SSH-agent forwarding, and no default-context switch.
The script refuses foreign profiles and saved configuration drift. Inspect an
error before changing or deleting an existing profile.

The workspace uses an isolated UID mapping, blocks privileged/nested containers,
and disables the Incus guest API. It shares the Linux host kernel. Keep the host's
administrative socket outside the workspace.

The image is cached in the project. Keep its exported artifact for reprovisioning.
Host packages and Colima's VM image are not pinned OS builds.
Storage backends may reject disk shrinking. Keep state local, serialize
applies, and do not use a second empty state for the same project. Project deletion
does not force-delete resources created outside this state.

Live-tested on Apple Silicon with Colima 0.10.3 and Incus server 7.1/client 7.4:
image build/export, provisioning, unchanged second plan, unprivileged SSH,
host-key replacement detection, dev network enable/disable with offline control,
dedicated Linux sharing IDs, read-only denial, and restart persistence.
Mock tests pass with OpenTofu 1.12.6 and Terraform 1.15.4. The image workflow also
exercises provisioning and SSH on AMD64 and ARM64 Ubuntu runners.

</details>

## From a source checkout (contributors and maintainers)

Optional. Customers don't need a clone: the installed `pairtask` runs the
same scripts. From the root of a checkout, each command above has a script
form, and flags pass through unchanged. `pk --dry-run COMMAND` prints the
script a command runs.

| `pairtask` command | From the checkout root |
| --- | --- |
| any `pairtask COMMAND` | `python3 scripts/pairtask.py COMMAND` |
| `pk vm plan` / `pk vm apply` | `python3 scripts/sandbox-host.py plan` / `apply` |
| `pk vm incus-plan` / `pk vm incus-apply` | `python3 scripts/sandbox-host.py incus-plan` / `incus-apply` (same flags) |
| `pk install` | macOS: `python3 scripts/sandbox-host.py apply`, then `python3 scripts/sandbox-provision.py rollout` |
| `pk upgrade` | `python3 scripts/sandbox-provision.py rollout` |
| `pk plan` / `pk apply` / `pk recover` | `python3 scripts/sandbox-provision.py plan` / `apply` / `recover` |
| `pk shell` / `pk chat` | `python3 scripts/sandbox-session.py shell` / `chat` |
| raw SSH | `ssh -F infra/incus/ssh/config workspace` |
| `pk ssh` | `python3 scripts/sandbox-ssh.py` |
| `pk download` | `python3 scripts/sandbox-download.py` |

A checkout keeps its own state: SSH keys under `infra/incus/ssh/` and images
under `dist/`. The installed command keeps them under `~/.pairtask/host`.

Maintainer checks (CI supplies OpenTofu), after provider initialization:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```
