# Sandbox tools: slim image and installed tool stores

Status: design v5, agreed 2026-10-14 and delivered; see "Not yet verified on a
real sandbox" below for pending manual checks. To select optional tools, use
`--tools` in [host commands](host-commands.md#install-and-upgrade).

## Threat model and boundaries

- **`workspace` is untrusted:** agents and dev root processes run there.
- **`secured` is trusted:** it runs the broker, operator endpoint and
  supervised Codex, and never executes anything `workspace` can write. The two
  containers never share a writable tool store.
- **Host-only installer:** `sandbox-provision.py` is the only installer. Agent
  processes never fetch or replace their own executables.
- **Digest-verified artifacts:** every artifact has a SHA-256 in
  `tools.lock.json` or the bundle manifest, verified on the host before
  transfer and again in the container before extraction.

## Image contents vs tool artifacts

The base image (under the 400 MiB CI budget) includes Ubuntu 24.04 system
packages (`ca-certificates`, `curl`, `python3`, `git`, `gh`, `openssh-server`,
`sshfs`, `ripgrep`, `tmux`, `less`, `locales`, `vim-tiny`, AppArmor, systemd),
compiled pairtask binaries (`pairtask`, `pairtask-broker`, `pairtask-mcp`,
`pairtask-codex`), systemd units, the collaboration skill, `tools.lock.json`,
`build-source.json`, `pairtask-tools-activate`, and `/usr/local/bin` tool
launchers.

| Artifact | Kind | Source |
| --- | --- | --- |
| `go`, `codex`, `claude`, `rtk`, `starship`, `node`, `cs-mcp` | `archive` or `binary` | Vendor release pinned in `tools.lock.json` |
| `playwright` (`node_modules` + headless Chromium) | `archive` | Built in CI, SHA-256 in release bundle manifest |
| `chromium-deps` (`.deb` closure for Chromium) | `debs` | Built in CI from signed apt archive, installed offline via `dpkg -i` |
| `docker` (rootless Docker `.deb` closure) | `debs` | Built in CI from signed apt archive, installed offline via `dpkg -i` |
| `pi` (pinned Pi closure + pairtask extension, examples stripped, licenses checked, `THIRD_PARTY_NOTICES.txt` included) | `archive` | Built in CI by `infra/image/build-pi-bundle.py`; runs on locked Node |

### Bundle releases

`scripts/sandbox-bundles.py` builds `collab-ai-bundle-<kind>-linux-<arch>.tar.gz`
and `collab-ai-bundles-linux-<arch>.json` inside a disposable container of the
pinned base image.

- **Input digest:** each architecture's bundle set is keyed by the digest in
  `collab-ai-bundle-inputs-linux-<arch>.json` (recipe scripts, Node pin, base
  image pin, architecture, and bundle kinds). Codex, Claude or Go version bumps
  do not change it.
- **Reuse and publication:** CI runs `sandbox-bundles.py --reuse` to reuse an
  existing `bundles-<digest>` release or build a new one. `bundle-publish`
  publishes new sets once (never `latest`) or verifies byte identity.
- **Workspace releases** carry only the small bundle manifest naming the
  `bundles-<digest>` release. Keep a `bundles-*` release as long as any
  workspace release references it.

## Store layout and root dependencies

Each container has its own root-owned (`0755`, `security.shifted=false`,
`prevent_destroy`) persistent volume at `/var/lib/collab-ai-tools`:
`workspace-tools` (4 GiB) and `secured-tools` (2 GiB).

```text
/var/lib/collab-ai-tools/
  artifacts/<sha256>/            # immutable extracted content + .complete marker
  generations/<id>/              # manifest.json + <tool> -> ../../artifacts/<sha256>
  current -> generations/<id>    # atomic symlink flip (os.replace)
  lock                           # flock held by pairtask-tools-activate
```

- Incomplete `artifacts/<sha256>` entries without `.complete` are deleted on
  the next run.
- Launchers (`/usr/local/bin/claude`, `codex`, `go`, … symlinked to
  `pairtask-tool`) resolve `current` once at exec time and set fixed generation
  paths (`PATH`, `GOROOT`, `PLAYWRIGHT_BROWSERS_PATH`, Node path). Missing
  optional tools exit 127 with instructions to enable the tool and run rollout.
- **Root dependencies:** `.deb` bundles (`chromium-deps`, `docker`) and derived
  configs (Claude/Codex MCP entries, Docker unit condition) live on the root
  disk, tracked in `/var/lib/collab-ai/root-deps.json`. On root replacement,
  the activator re-installs missing `.deb` bundles and verifies every package
  version before flipping `current`.
- **Activation order:** stage and verify archives → install root `.deb`
  dependencies → write derived MCP/unit config → validate launchers, AppArmor
  (`collab-chromium`) and MCP JSON → flip `current` → prune (retaining current
  and previous generations).

## Rollout and agent admission gate

1. `sandbox-provision.py` caches and verifies required artifacts under
   `dist/tools/<sha256>` (`secured` gets Codex and Claude; `workspace` gets
   required tools plus selected optional tools).
2. The host queries `pairtask-tools-activate status`, pushes missing archives
   to `/var/lib/collab-ai-tools/incoming/`, and runs
   `pairtask-tools-activate <manifest>`.
3. **A released root is only ever added to:** a root already released to
   agents is never edited in place. Changing `--tools` or `tools.lock.json`
   requires a rollout (`plan_identity` hashes the tool selection, lock
   digest, and activator). The one exception is the agent clients, which
   `pairtask update` moves to another version by adding a generation beside
   the running one ([updating the agent clients](#updating-the-agent-clients)).
4. **No agent execution before release:** the activator writes
   `/var/lib/collab-ai/tools-state.json` (`maintenance` before mutation,
   `ready` after flip and prune). Until `ready`:
   - no agent SSH keys are installed in `/etc/ssh/authorized_keys/`;
   - `user@1001.service`, `pairtask-dev-executor@`, `pairtask-supervised-codex`,
     and Docker units require `pairtask-tools-ready.service`, which checks
     `tools-state.json`, `current/manifest.json`, `tools.lock.json`,
     `build-source.json`, and `root-deps.json`;
   - host session commands (`pairtask shell`, `pairtask start`, `pairtask ssh`)
     refuse unreleased roots.
5. **Recovery:** `pairtask tools` (`tools-retry`) reruns activation only on a
   root that was never released; otherwise `pairtask upgrade` replaces the root.

### Updating the agent clients

`pairtask update` moves Claude Code, Codex and the CodeScene MCP server to
newer versions in a running sandbox, without replacing the containers:

```sh
pairtask update                    # Claude Code's stable channel; the latest Codex and CodeScene MCP
pairtask update --claude 2.1.327   # this exact version of Claude Code; the others as above
pairtask update --rollback         # back to the clients from before; no download
pairtask update --status           # what the last update did; changes nothing
pairtask update --repair           # finish or revert an interrupted update
```

What it does, and what it never does:

- **Only the clients.** The release's own `tools.lock.json` isn't changed.
  The host writes an *override lock*: the image's lock with nothing
  different but the versions and checksums of `claude`, `codex` and
  `cs-mcp`, each checksum taken from its publisher. The container stores it
  under the root-owned `/var/lib/collab-ai-tools/overrides/` and accepts it
  only after comparing it with the image's lock field by field; any other
  difference leaves the root not ready.
- **Added, never edited.** Each container gets a new generation beside the
  one it runs. Nothing existing is changed and nothing is pruned, so a
  session that started on the earlier clients keeps its files. Earlier
  generations go when `pairtask upgrade` replaces the root. `pairtask
  status` and `pairtask update --status` show, per container, how many
  generations are kept and the bytes stored (file sizes, each shared file
  counted once), so you can see when an upgrade is due.
- **Checked before it is used.** Each new client is started as an
  unprivileged user in a private home before anything is switched: it must
  report its version, Claude Code must print its help, and Codex must answer
  an `initialize` request with the configuration this root runs it with.
- **Both containers or neither.** No new tool starts in either container
  while they are switched (running ones continue; a tool started in that
  window exits 75), and neither reopens until both verify. If a step fails,
  both go back to the clients they had. Host sessions, container startup,
  and SSH setup take the update lock and refuse while an update is active
  or its journal is invalid. An admitted session releases that lock as soon
  as its local process starts, so an existing shell never holds up an update.
- **It survives being interrupted.** The host records each step before and
  after it in `tools-update.json` beside the operator state. The next
  `pairtask update` (or `--repair`) reads back what each container actually
  runs and finishes or reverts; it never assumes. A container that can't be
  reached leaves everything where it is until it is back.
- **An upgrade ends it.** `pairtask upgrade` starts the new root from its
  release's own versions; an update never carries over, and `pairtask
  version` shows the updated clients until then. Upgrade holds the update
  lock through replacement and refuses an unfinished transaction; run
  `pairtask update --repair` first. To make a version
  permanent, pin it in `tools.lock.json` (the tools lane in CI checks such a
  change in minutes).

Agents see the new client when they next start one: restart agent sessions
after an update, as [after an upgrade](sandbox.md#after-an-upgrade-restart-agent-sessions).

### Image layout

During an upgrade, the host probes `/usr/local/share/pairtask/layout.json` in
each container (`scripts/sandbox_layout.py`):

- A missing marker or `{"layout": 1}` selects legacy (`collab-*`) paths.
- Layout v2 (`infra/image/layout-v2.json`) uses `/usr/local/lib/pairtask/` and
  `pairtask-*` units while keeping legacy forwarders for host compatibility.
- An unreadable, malformed, or unknown layout version halts tool activation and
  SSH key admission before downloading or pushing artifacts.

## Extraction policy

Before `extractall(filter="data")` into a mode-`0700` root staging directory,
the activator rejects the entire archive if any member has:
- an absolute path, `..` component, empty name, or normalized path outside the
  artifact root;
- duplicate or case-colliding normalized paths;
- block, character, FIFO, or socket file types;
- a symlink target resolving outside the artifact root, or a hardlink to a
  non-regular or later member;
- missing expected layout entries for its artifact kind (e.g. `bin/codex`,
  `bin/node`).

## Optional tools

Recorded in `/etc/pairtask/tools.json` and the generation manifest:

- **Always installed:** `go`, `codex`, `claude`, `rtk`, `starship`.
- **Optional (on by default):** `playwright` (bundle + `chromium-deps` +
  `node`), `codescene` (`cs-mcp`), `docker` (`.deb` set), `pi` (bundle +
  `node`).
- Disabled tools get no MCP config entry and no `npx` fallback; disabling
  `docker` leaves the persistent `docker-data` volume untouched.

## Not yet verified on a real sandbox

- Per-container volumes (`security.shifted=false`) keep content and root
  ownership across a root replacement.
- Member validation and the `tarfile` `data` filter accept the Codex and Node
  archives (with internal symlinks) and reject crafted escapes.
- Push and activation time for a cold `workspace` (~650 MiB) over Colima.
- Mode-`0700` maintenance state blocks the `agent` SSH session from executing
  store content, and launchers return the maintenance message.
