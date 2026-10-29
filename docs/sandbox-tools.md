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
3. **Mutation only on a fresh root:** a root already released to agents is
   never mutated in place. Changing `--tools` or `tools.lock.json` requires a
   rollout (`plan_identity` hashes the tool selection, lock digest, and
   activator).
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
