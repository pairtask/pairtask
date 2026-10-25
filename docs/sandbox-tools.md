# Sandbox tools: a slim image and installed tool stores

Status: design v5 for #84, agreed by Claude and Codex on 2026-10-14
(DESIGN-OK, architectural). Implementation, the pending verification and the
acceptance tests follow in the PRs listed under "Delivery".

## Problem

The workspace image is about 1 GiB compressed (`build.147.1`: 1055 MiB amd64,
1041 MiB arm64) and 1.5 GiB on disk, because every third-party tool is baked
in. Each release stores two of them, and every host downloads one per release.
Measured on arm64 `build.141`:

| Component | On disk |
|---|---|
| Chromium for the Playwright MCP, its apt libraries and fonts, Node 24 | ~450 MiB |
| `claude` | 229 MiB |
| Codex (`/opt/codex`) | 203 MiB |
| rootless Docker stack (`dockerd`, `docker`, `containerd`, `rootlesskit`, `ctr`, `runc`) and its packages | ~130 MiB |
| Go toolchain | 101 MiB |
| `cs-mcp` (CodeScene) | 87 MiB |
| collab binaries, `gh`, rtk, starship | ~70 MiB |
| vim, locales, docs, man pages | ~55 MiB |

Operator decision (2026-10-14): **only the minimal collab/pairtask runtime is
preinstalled. All third-party tools are installed at install time, some of
them optional.**

## Threat model

- `workspace` is the untrusted side: agents run there, and dev root (the
  executor, setup units) is reachable from the dev side. Anything writable
  from `workspace` is untrusted content.
- `secured` is the trusted side. It runs the control broker, the operator
  endpoint and supervised Codex. **It executes nothing that `workspace` can
  write**, so the two containers never share a writable store.
- The host (`sandbox-provision.py` and the disposable operator) is trusted:
  it already builds the plan, replaces roots and holds the Incus admin socket.
  It is the only installer.
- Tools are trusted only by digest: every artifact has a sha256 in
  `tools.lock.json`; the host verifies it before transport and the container
  verifies it again before extraction. A version string is never used as
  evidence.

## Goals

- The image holds the Ubuntu base, the system packages the containers need to
  boot and be reached, and the collab runtime. Target: well under 300 MiB.
- Tools are installed once onto a persistent volume per container and survive
  rollouts; a rollout transfers only the artifacts whose pin changed.
- A host-side cache keeps verified artifacts across rollouts, like
  `dist/images` does for the image.
- Optional tools can be turned off per launch mode or preset (#73); the
  shipped default keeps today's feature set.
- Agent processes never fetch or replace their own executables.
- The store switch is atomic: a container runs one complete tool generation,
  never a mix. Root-level dependencies (deb bundles, derived configuration)
  are not atomic; they are installed only on a fresh root that no agent can
  use until the whole activation has committed.

## What stays in the image

- Ubuntu 24.04 base and system packages from Ubuntu's own archive, installed
  with apt at build time: `ca-certificates curl python3 git gh openssh-server
  sshfs ripgrep tmux less locales`, AppArmor, systemd pieces. **Rule:** a
  package from the distribution archive is part of the OS and may stay; a
  download from a vendor is a third-party tool and goes to the store. `gh` is
  the one borderline case and stays because it comes from apt and is 14 MiB.
- The pairtask binaries (`pairtask`, `pairtask-broker`, `pairtask-mcp`, `pairtask-codex`),
  built inside the builder as today. The Go toolchain is removed again before
  publishing.
- The systemd units, the `secured` runtime, the skill, configs, sshd config,
  `tools.lock.json` and `build-source.json` under `/usr/local/share/pairtask/`
  so the pins travel with the release.
- The in-container activator `pairtask-tools-activate` and the tool launchers.
- Housekeeping: `vim-tiny` instead of vim; `/usr/share/{doc,man}` and
  non-English locales removed; `incus publish --compression xz`.

## Artifacts

Every tool is an **artifact**: a URL, a sha256 per architecture, and an
install kind. `tools.lock.json` grows an `artifacts` section; the existing
per-tool fields are folded into it by the lock refresh script.

| Artifact | Kind | Source |
|---|---|---|
| go, codex, claude, rtk, starship, node, cs-mcp | `archive` or `binary` | vendor release, as today |
| playwright bundle: `node_modules` from `npm ci` + Chromium (or `chromium-headless-shell`) | `archive` | **built in CI** and published as a release asset with its sha256 in the release manifest |
| chromium-deps: the `.deb` set `playwright install --with-deps` would install | `debs` | built in CI with `apt-get download` against the signed archive, bundled, sha256 in the manifest |
| docker: Docker's `.deb` set plus `uidmap slirp4netns fuse-overlayfs …` | `debs` | same |
| pi: the pinned Pi closure plus the pairtask extension, examples removed, licenses checked, notices included (#155) | `archive` | **built in CI** by `infra/image/build-pi-bundle.py`, like Playwright; runs on the lock's Node |

CI-built bundles make the "closure" explicit: pinned Node + pinned MCP
dependencies + a pinned browser archive, verified by one digest. apt
signatures are checked at bundle build time; at activation the bundle is
offline `dpkg -i`, no repository access from inside the sandbox.

`scripts/sandbox-bundles.py` builds them in the images CI job, inside a
disposable container of the pinned base image, so the `.deb` closure is
exactly what the slim image lacks. The output is
`collab-ai-bundle-<kind>-linux-<arch>.tar.gz` per bundle and one
`collab-ai-bundles-linux-<arch>.json` with every bundle's digest, package
inventory (deb bundles) or pinned versions and Chromium path (playwright).

### Bundle releases

Bundles change far less often than the image, so they're published once per
set of inputs instead of with every workspace release.

- **The digest:** each architecture's bundle set is keyed by a digest of its
  inputs, recorded in `collab-ai-bundle-inputs-linux-<arch>.json`. The inputs
  are the recipe files (`scripts/sandbox_bundles.py`, `sandbox-bundles.py`,
  `sandbox_builder.py`, `infra/image/build-playwright-bundle.py`,
  `tool_download.py`, `tool_lock.py`, `mcp/package*.json`,
  `docker-packages.txt`), the Node pin, the base image pin, the architecture
  and the bundle kinds. Codex, Claude Code or Go bumps don't change it.
- **CI:** the images job runs `sandbox-bundles.py --reuse`. If a published
  `bundles-<digest>` release exists, it downloads and verifies those exact
  files; otherwise it builds new ones. The smoke tests then use those bytes.
  On `main` and `workspace-v*` pushes, the serialized `bundle-publish` job
  either publishes a newly built set (staged as a draft, verified as
  uploaded, never `latest`) or checks that the published release is
  byte-identical to what the run tested. A published bundle release is never
  changed.
- **Workspace releases:** they carry only the bundle manifest, whose
  `release` field names the bundle release. Rollouts fetch bundles from
  there, checked by digest.
- **Frozen package sets:** apt and Docker package sets resolve when a bundle
  set is first built and stay frozen until an input changes. Newer packages
  arrive with a deliberate change, such as a base image refresh
  (`scripts/base-image-refresh.py`) or a package list edit.

Keep a `bundles-*` release while any published workspace release references
it.

## The tool stores

Each container gets its **own** persistent custom volume, root-owned, mode
0755, `security.shifted=false` like the data volumes, `prevent_destroy`:

- `workspace-tools` at `/var/lib/collab-ai-tools` in `workspace` (default
  4 GiB);
- `secured-tools` at `/var/lib/collab-ai-tools` in `secured` (default 2 GiB).

Layout, identical in both:

```
/var/lib/collab-ai-tools/
  artifacts/<sha256>/            # immutable, extracted content; .complete marker last
  generations/<id>/              # one activation: manifest.json + <tool> -> ../../artifacts/<sha256>
  current -> generations/<id>    # the only mutable pointer; replaced atomically
  lock                           # flock held by the activator
```

- `artifacts/<sha256>` is written by the activator from a verified archive;
  an entry without `.complete` is garbage from a crash and is removed on the
  next run before anything else.
- `generations/<id>/manifest.json` records the release (`build-source.json`
  digest), the lock digest, the selection, the architecture and every tool's
  artifact digest. A generation is complete only when its manifest exists;
  `current` is flipped with `os.replace` of a symlink after that.
- Launchers resolve through `current` once at exec time, so a process is
  always on one generation.
- Pruning keeps the current and the previous generation and the artifacts
  they reference; everything else is deleted. Nothing is pruned on failure,
  and pruning runs only inside an activation, which only happens while the
  tool consumers are stopped (below).

### Artifacts versus root dependencies

The atomic flip covers the store only. Two things live on the **root disk**
and are replaced with it at every rollout:

- **deb bundles** (`chromium-deps`, `docker`): installed with offline
  `dpkg -i`, which changes `/usr`, `/etc` and the dpkg database;
- **derived configuration**: the MCP entries for Claude and Codex, the
  selection copy, the Docker unit condition.

So an artifact's `.complete` marker means "extracted in the store", never
"installed in this root". Each root keeps its own record,
`/var/lib/collab-ai/root-deps.json` on the root disk, listing the deb bundle
digests installed into *this* root and the dpkg versions they produced. The
activator installs a bundle when that record lacks its digest, re-verifies the
dpkg closure (every package of the bundle at the bundled version) even when the
artifact was cached, and only then continues. A failed deb install leaves the
root in whatever state dpkg left it; the activator records the failure, does
not flip `current`, and readiness stays failed with the recovery path:
`sandbox-provision.py tools-retry` on the still-unreleased root (the activator
is idempotent), or `rollout` to replace the root.

Order inside one activation: stage and verify artifacts → install root
dependencies → write derived configuration → validate (launchers resolve,
AppArmor profile loads, MCP config parses) → flip `current` → prune.

## Install flow

Host-driven, at `provision` and `rollout`, after the instances are running
and before any agent entry point is enabled:

1. `sandbox-provision.py` reads the release manifest (which carries the lock)
   and the selection, and resolves the artifact list per container:
   `secured` gets Codex and Claude; `workspace` gets the default set plus the
   optional tools the preset enabled.
2. Missing artifacts are downloaded into the host cache
   `dist/tools/<sha256>` and verified; a cached artifact is verified again
   before use. The cache is reused across rollouts and releases.
3. For each container, the host asks `pairtask-tools-activate status` which
   artifacts are already complete, pushes only the missing ones with
   `incus file push` into `/var/lib/collab-ai-tools/incoming/`, and runs
   `pairtask-tools-activate <manifest>` as the container's root.
4. The activator takes the flock, verifies every incoming archive's sha256
   against the manifest, extracts with Python's `tarfile` data filter (no
   absolute paths, no traversal, no links escaping the artifact, no device
   files; ownership forced to root), or runs offline `dpkg -i` for a `debs`
   artifact; writes `.complete`; builds the generation; flips `current`;
   prunes.
5. The host checks `status` once more: the current generation must match the
   release and selection, or the step fails with the activator's log.

The step has a bounded timeout and prints what it is transferring. The
containers never download anything themselves, so `secured` needs no network
interface and the egress work (#69–#74) is unaffected.

**Mutation only on a fresh root.** A live root can't be quiesced: SSH and
tmux sessions, the agent's own Docker daemon and transient supervised units
can all hold tool processes, and a unit check races with new launches. So the
activator **never changes a root that has been released to agents**. Tool
generations change only at `rollout` (and first `provision`), on a root that
is in maintenance from its first boot until the activation commits. A changed
selection or lock therefore requires a rollout; the image is cached and will
be small, so that is the normal, cheap path. `sandbox-provision.py start` only
re-verifies and reports; it installs nothing.

**Invariant: no agent execution before release.** The boundary is **agent
admission**, not file permissions. A fresh root admits no agent session, no
agent service and no agent-triggered execution from its first boot until the
host releases it, after the durable ready commit. Concretely:

- the agent's SSH keys live in `/etc/ssh/authorized_keys/` on the root disk,
  so a fresh root has none; the host installs them only at release. SSH
  transport, the control plane, the operator endpoint and host admin access
  stay up throughout;
- `user@1001.service` (the agent's user manager, which would autostart the
  rootless Docker daemon and any lingering user service),
  `pairtask-dev-executor@`, `pairtask-supervised-codex` and the Docker units all
  carry `Requires=pairtask-tools-ready.service` and `After=`, and
  `pairtask-tools-ready` fails while the state is not `ready`;
- `collab start` and every other host path that opens an agent session checks
  the instance state and refuses while it isn't released.

This gate stays closed across every mutation: artifact staging, deb installs,
derived configuration, validation, the `current` flip, pruning and the durable
ready write. Only then does the host release the root and start the
consumers. The store's `artifacts/` and `generations/` directories are
additionally 0700 until the commit, as defence in depth, never as the
boundary: the volume arrives 0755 from the previous root, and deb bundles put
executables under `/usr`, outside the store.

The activator writes `/var/lib/collab-ai/tools-state.json` on the root
(`{"state": "maintenance", "generation": …, "started": …}`) **before the first
mutation**, and `{"state": "ready", "generation": …}` only after the flip and
prune. Readiness and launchers read this file, so a crash anywhere in between
fails closed even when the manifest and `root-deps.json` happen to match. A
cached `ready` is never trusted across a mutation.

**A released root is never re-entered into maintenance.** `ready` is terminal
for mutation: once written, the activator refuses to touch that root even if
the host's release step (key install, consumer start) was interrupted; the
host completes the release instead. Once agents have been admitted, the only
way to change tools is a new root. Every path that admits agent execution is
audited against the state file, including `sandbox-ssh.py`, so no standalone
key install bypasses admission. If a root's
release state is uncertain (for example the state file is missing but agent
keys exist), the host requires replacement rather than guessing.

Recovery from a crashed activation: `sandbox-provision.py tools-retry`
reruns the activator on the same root **only** while that root was never
released (state file absent or `maintenance`, no agent keys installed, agent
units never started); otherwise, and in any doubt, `rollout` replaces the
root.

**Pruning** happens only inside such an activation. The previous root's
processes died with that root, and the current root hasn't admitted any, so
no file in use can be unlinked. If anything about the maintenance state is in
doubt, pruning is skipped; it is never required for correctness.

`running=false` records "tools not activated" and names the rollout as the
installing command. A plain `incus start` boots the control plane normally,
and the tool-dependent entry points stay in maintenance with that message.

## Readiness

- **Only tool-dependent entry points are gated.** The broker, the operator
  endpoint, `pairtask operator chat`, the dashboard and SSH itself are collab
  code in the image and never wait for third-party tools; that includes
  `collab-client-setup`, which only creates the brokers' socket aliases.
  `pairtask-dev-executor@`, `pairtask-supervised-codex` and the Docker units carry `Requires=pairtask-tools-ready.service` and
  `After=pairtask-tools-ready.service`, so a failure propagates instead of
  merely ordering.
- `pairtask-tools-ready.service` is a oneshot that fails unless
  `tools-state.json` says `ready` for the generation that `current` points
  at, `current/manifest.json` matches `/usr/local/share/pairtask/tools.lock.json`
  and `build-source.json` of the running root, and `root-deps.json` covers
  every deb bundle the selection needs. A root replaced by rollout therefore
  refuses tool entry points until the host has activated a matching
  generation; the host does that in the same rollout.
- Launchers are real programs, not dangling symlinks: `/usr/local/bin/claude`,
  `codex`, `go`, … are symlinks to one `pairtask-tool` launcher that dispatches
  on `argv[0]`, resolves `current` once, and `exec`s the tool **with fixed
  generation paths in its environment** (`PATH` prefixed with that
  generation's `bin`, `GOROOT`, `PLAYWRIGHT_BROWSERS_PATH`, Node's path), so
  child processes and library lookups stay on the same generation. A missing
  tool exits 127 with "claude is not installed in this sandbox; enable it in
  the preset and run rollout".
- `secured`'s supervised Codex uses the same launcher against its own store.

### Image layout

During an upgrade, a host checkout drives both the image it provisioned
before and the one it is rolling out. So the host first asks each container
which names its image uses for the paths, helpers and units it drives
(`scripts/sandbox_layout.py`).

- **How:** one Python probe reads `/usr/local/share/pairtask/layout.json`.
  It answers either "absent" or the parsed marker.
- **Legacy layout:** a confirmed missing marker, or an explicit
  `{"layout": 1}`, means the legacy (`collab-*`) layout. Every image so far
  uses it.
- **Hard stops:** an unreadable or malformed marker, a failed probe, or a
  layout version this checkout doesn't know stops tool activation and SSH
  key admission for that container. After a failed probe, the activation
  path neither downloads nor pushes tool artifacts. Steps that run earlier
  in a rollout, such as fetching the image, aren't affected. A newer image
  layout asks for a newer host checkout.
- **Compatibility:** images that use new names keep the legacy names the
  host relies on (the readiness helper, the tools-mode file, the units) as
  forwarders, so an older host checkout keeps its readiness gate.

## Extraction policy

Before anything is extracted, the activator validates every archive member
and rejects the whole artifact on the first violation:

- names are normalized; absolute names, `..` components, empty names and names
  that normalize outside the artifact root are rejected (Python's `data`
  filter strips a leading `/`, which is not a rejection, so this check comes
  first);
- duplicate or colliding names (after normalization and case) are rejected;
- only regular files, directories, symlinks and hardlinks are accepted; block,
  character, FIFO and socket entries are rejected;
- a symlink target must resolve inside the artifact root; a hardlink must
  point at an earlier regular member of the same archive;
- the supported layout per artifact kind is checked (for example the Codex
  archive has `bin/codex`; the Node archive has `bin/node`).

Then `extractall(filter="data")` runs as a second layer, into a fresh
root-private staging directory (0700) inside the store, with ownership forced
to root. The manifest comes only from the host's push path, never from the
archive.

## Optional tools

The selection is written by the provisioner to `/etc/pairtask/tools.json`
in both containers and recorded in each generation's manifest:

```json
{"go": true, "codex": true, "claude": true, "rtk": true, "starship": true,
 "playwright": true, "codescene": true, "docker": true}
```

- Required by every launch mode: Go, Codex, Claude Code, rtk, starship.
- Optional: playwright (bundle + chromium-deps + node), codescene (cs-mcp),
  docker (deb set). The default selection keeps today's feature set, so a
  rollout changes nothing for the operator; presets (#73) and a `--tools`
  flag turn optional tools off or on. Whether the shipped default becomes
  "off" is the operator's call once presets exist.
- The MCP configuration for Claude and Codex is written by the activator from
  the selection (`install-mcp-tools.py configure()` moves there), so a tool
  that is off has no MCP entry and no `npx` fallback.
- The Docker units get `ConditionPathExists=/var/lib/collab-ai-tools/current/docker`
  and keep using the `docker-data` volume; turning Docker off leaves the data
  volume alone.
- The `collab-chromium` AppArmor profile attaches by real path, so it matches
  `/var/lib/collab-ai-tools/artifacts/*/chrome*/chrome`.
- Turning a tool off removes it from the next generation; its artifact is
  pruned with the old generation.

## Current code this touches

- `install.sh` enables `collab-docker-{session,setup}` unconditionally; they
  become conditioned on the store (above).
- `sandbox_workspace_checks.py` and `sandbox_storage_checks.py` always probe
  Docker and rtk; the checks read the selection and probe only enabled tools,
  and assert that a disabled tool is absent.
- `pairtask-dev-executor@` and `pairtask-supervised-codex` call
  `/usr/local/bin/codex`; that path stays and is the launcher.
- `pairtask-playwright` hardcodes `/opt/collab-mcp`; it resolves the store path
  through `current` instead.
- **Launch modes require tools.** Each mode or preset (#73) declares the tools
  it needs (duo: go, codex, claude, rtk; a review preset may add codescene).
  The provisioner rejects a selection that lacks a required tool instead of
  half-starting.
- **Plan identity.** `plan_identity` hashes the provisioning files, image and
  operator runner today. It also covers the canonical selection, the lock
  digest and the activator's own digest, so a plan can't apply changed tools
  silently.
- **Release gate.** The digest check is necessary, not sufficient: CI builds
  the slim image, activates a generation in a test container, and runs the
  launcher checks, the AppArmor profile load and the MCP configuration before
  a release is published.

## Delivery, without a regression

Every release stays feature-complete; the image switch is the last step and
happens only when every artifact exists. PRs in order:

1. **Lock and artifact schema** (`artifacts` table in `tools.lock.json`,
   `sandbox_versions.py` refreshes it), the host cache and the download
   verification. No image change.
2. **CI bundles:** playwright, chromium-deps and docker built in CI and
   attached to releases with their digests in the manifest. No image change.
3. **Stores, activator, launchers, readiness, managed start**, in two PRs:
   - **3a:** the `workspace-tools` and `secured-tools` volumes, `tool_store.py`
     with the activator, readiness check and launcher, the gated units. The
     image records `tools-mode = baked`: it still carries the tools and the
     readiness gate passes, so nothing changes for agents while the store is
     exercised end to end.
   - **3b:** the host flow (cache → push → activate → release) in
     `scripts/sandbox_tools_install.py`, run by `rollout` between apply and
     the SSH key install; `--tools` selects optional vendor tools (bundles
     follow in PR 4); plan identity covers the selection, the release lock and
     the activator; `sandbox-ssh.py` refuses the agent key while
     `pairtask-tools-ready` fails; `tools-retry` reruns a crashed activation on
     never-released roots only.
4. **Bundle delivery (4b).** `sandbox-download` records next to each image
   where its bundles come from (`bundle-source.json`: the bundle release the
   manifest names, the workspace release URL for releases from before bundle
   releases, or the local directory the assets were copied into) and keeps the
   small bundle manifest. A local `--from-dir`/`--from-zip` source without any
   bundle files falls back to the manifest's bundle release; one with only
   some, or a corrupt one, is an error; the activator already installs deb sets (4a). In a store-mode
   workspace the host adds the Playwright, Chromium-deps and Docker entries
   from that manifest and fetches their files by digest on demand; baked roots
   and `secured` never get them. A selection that needs bundles from a release
   without them is refused with a message.
5. **Slim image (4c).** `install.sh` stops downloading vendor tools; Go is
   present only while collab compiles; the tool names in `/usr/local/bin` are
   launchers; `tools-mode` is `store`; CI asserts a 400 MiB image budget. The
   smoke and boundary tests activate tools after their `tofu apply`, as the
   rollout does, because a store-mode root admits agents only then. The default selection keeps today's feature set on (Docker and
   Playwright enabled) unless the operator decides otherwise; presets (#73)
   can turn them off.

The 200–250 MiB figure is the measured expectation from the component table,
not a guarantee; CI asserts a size budget once the slim image exists.

## Verification still pending

None of these has been run yet; each is done on the real sandbox during PR 3
and recorded here with its result:

- the per-container volumes (no `security.shifted`, as they aren't shared)
  keep their content and root ownership across a root replacement;
- the member validation and the `tarfile` data filter accept the Codex and
  Node archives (which contain internal symlinks) and reject crafted escapes;
- push and activation time for a cold `workspace` (~650 MiB) over
  `incus file push` on Colima;
- the 0700 maintenance mode blocks an SSH session of `agent` from executing
  store content, and the launchers return the maintenance message.

## Tests (scope only, written after authorization)

- Host side (`scripts/tests`): artifact resolution per container and
  selection; cache hit/miss and re-verification; a corrupt cache entry is
  re-downloaded; `running=false` skips; lock shape.
- Activator: wrong digest rejected before extraction; traversal, absolute
  path, link escape and device entries rejected; crash before `.complete`
  leaves no half artifact; crash before manifest leaves `current` unchanged;
  concurrent activation serialized; prune keeps current and previous.
- Readiness: unit fails on lock mismatch; launcher exits 127 with the message;
  Docker units conditioned off.
- Maintenance: crash before and after `ready` leaves the root closed or
  released respectively, never half-open; a direct SSH attempt and the agent
  user-manager autostart are refused before release; `tools-retry` refuses a
  root that was ever ready or released.
- Image: build without vendor downloads; size budget asserted in CI.
