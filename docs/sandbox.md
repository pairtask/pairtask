# Set up an Incus sandbox

**Start Incus → rollout → SSH → sign in.** Run host commands from
this repository. Already provisioned? Use the [daily quick start](../README.md#set-up-a-sandbox).

The image includes collab-ai, Codex, Claude Code, Go, Git, tmux, Starship and RTK.
Defaults: 2 CPUs, 4 GiB RAM, 10 GiB root disk, 10 GiB projects, 2 GiB agent home.
Projects and agent home persist; tools live on the replaceable root disk.

## 1. Install the host tools

**macOS** — Homebrew required:

```sh
brew install colima incus python git
python3 scripts/sandbox-host.py plan
python3 scripts/sandbox-host.py apply
```

This starts the dedicated `pairtask` Colima VM running Incus. Existing Docker
profiles stay separate. Starting the VM does **not** create the workspace.

**Linux** — [install Incus](https://linuxcontainers.org/incus/docs/main/installing/),
Python 3.9+, Git and OpenSSH. Use an existing
quota-capable pool (ZFS/Btrfs), a managed network bridge and Incus access.
Skip Colima; replace `colima-pairtask:` with `local:` throughout.
Do not reinitialize an existing server. Mac requires Colima 0.10.3+.

### Incus server version

The Incus **server** must be **7.0.x at 7.0.2 or later** (LTS) or **7.5 or
later**. Older servers (6.0.x, 7.0.0–7.0.1, 7.1–7.4) can give two containers
created at the same moment overlapping host UID/GID ranges, which weakens the
file-permission separation between `secured` and `workspace`
([#183](https://github.com/pairtask/pairtask/issues/183); fixed upstream in
[lxc/incus#3999](https://github.com/lxc/incus/pull/3999)). Check the server
line, not the client:

```sh
incus version colima-pairtask:     # Linux: incus version local:
```

- **Stay on your track.** On 6.0 LTS, move to the fixed 7.0 LTS (7.0.2+). On
  7.1–7.4, upgrade forward to 7.5+; don't move to 7.0 LTS, because Incus
  can't downgrade its database.
- **Colima holds Incus.** Its VM image installs Incus from zabbly `stable` and
  `apt-mark hold`s the Incus packages, so `apt upgrade` in the VM skips them,
  and `brew upgrade incus` updates the Mac client, not the VM's server. Upgrade the held `incus*`
  packages deliberately inside the VM. Stop the sandbox first and back up the
  Incus database.
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

## 2. Install or upgrade

Exit agents before upgrading. The one-line installer does everything below
and puts the `pairtask` command on your PATH (run it again to upgrade):

```sh
curl -fsSL https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh | sh
```

From a checkout of this repository on your host instead:

```sh
python3 scripts/pairtask.py install        # later: python3 scripts/pairtask.py upgrade
python3 scripts/sandbox-provision.py rollout   # legacy form of upgrade: the rollout alone
```

`install` also starts the macOS VM first and lists the containers at the end;
`upgrade` and the legacy rollout are the same download-and-replace step.

Downloads the latest tested release for your server, verifies it, stops existing
containers, then provisions and configures SSH. No GitHub CLI, browser download,
or host Terraform/OpenTofu install. To select a version, add `--release workspace-vX.Y.Z`.
Projects and agent home persist; active processes end. [Back up before upgrading](sandbox-storage.md#back-up-and-restore).

New deployments include dev and offline control. State and settings stay on the host;
the script prints and remembers their directory. Existing settings are preserved.
Use `--storage-pool` / `--network` for a non-default Incus pool/bridge.
[Settings, plan preview and recovery](sandbox-operator.md).

## 3. SSH into the workspace

On the host:

```sh
python3 scripts/sandbox-session.py shell     # wraps the command below and restores your terminal after
ssh -F infra/incus/ssh/config workspace
```

You enter as `agent`, without sudo, in `/workspace`. The helper creates a dedicated
host login key and copies **only its public key**. It never forwards your SSH agent.
SSH uses `incus exec`, works offline and opens no network SSH listener.
A session that drops while a full-screen program (an agent harness, the chat)
runs can leave your terminal in mouse-reporting mode; the wrapper restores it,
and `reset` fixes a terminal that is already in that state (#122).

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

**RTK reduces shell output** through Codex/Claude hooks, configured on first boot.
Approve the RTK hook if Codex prompts. `rtk gain` shows estimated output savings,
not provider billing or a token cap. History stays in `~/.local/share/rtk/` across
container replacement; `/etc/codex/config.toml` grants it workspace-write access
through the App Server. User/project configuration can override that default.
Existing settings are preserved; telemetry is not enabled. To opt out, run
`rtk init -g --codex --uninstall` and `rtk init -g --uninstall --auto-patch`, then restart agents.

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
`dev_network_enabled = false` in the existing deployment variables and repeat
plan/apply. This interrupts network connections; SSH and the broker still work.
Set `true` and apply to restore access. For another project on the same server,
select a different bridge: instances named `workspace` cannot share its managed DNS.

## Status and recovery

Run on the host (Linux: `local:`):

```sh
incus --project pairtask list colima-pairtask:
```

Expect `workspace` (and optional `secured`) to be `RUNNING`; allow a few seconds
for the broker. **Keep the colon.** The remote is `colima-pairtask:`, not `pairtask:`.

| Problem | Action |
| --- | --- |
| Remote missing | Check `colima list`; rerun `sandbox-host.py apply` |
| Empty list / missing project | Check `incus project list colima-pairtask:`; run step 2 |
| SSH host key changed | Follow the [replacement procedure](sandbox-storage.md#replace-dev-retain-data) |
| Need a recovery shell | `incus --project pairtask exec colima-pairtask:workspace -- /bin/sh` (root) |

## Stop or remove

On the host; these retain data:

```sh
incus --project pairtask stop colima-pairtask:workspace
incus --project pairtask stop colima-pairtask:secured  # only in control mode
colima stop pairtask                                 # macOS only
```

Use the [daily block](../README.md#set-up-a-sandbox) to restart. Containers do not
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
proxy without stopping containers. Use the provisioning helper for managed changes to avoid drift.

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

Maintainer checks (CI supplies OpenTofu), after provider initialization:

```sh
python3 -m unittest discover -s scripts/tests -v
tofu -chdir=infra/incus fmt -check
tofu -chdir=infra/incus validate
tofu -chdir=infra/incus test
```

Live-tested on Apple Silicon with Colima 0.10.3 and Incus server 7.1/client 7.4:
image build/export, provisioning, unchanged second plan, unprivileged SSH,
host-key replacement detection, dev network enable/disable with offline control,
dedicated Linux sharing IDs, read-only denial, and restart persistence.
Mock tests pass with OpenTofu 1.12.6 and Terraform 1.15.4. The image workflow also
exercises provisioning and SSH on AMD64 and ARM64 Ubuntu runners.

</details>
