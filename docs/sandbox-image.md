# Workspace images

## Download a built image

Run on the host with Incus running:

```sh
# Download, verify, provision/upgrade and configure SSH
python3 scripts/sandbox-provision.py rollout
# Or pin a published version
# python3 scripts/sandbox-provision.py rollout --release workspace-vX.Y.Z
```

Defaults to `latest`; macOS uses `colima-pairtask`, Linux uses `local`.
No GitHub CLI, login or manual download. The script selects the server architecture,
resolves one release tag, verifies its checksum, and imports the container archive.
[First setup](sandbox.md) · [Upgrade and retained data](sandbox-storage.md#replace-dev-retain-data).

<details>
<summary>Download only or use a local build</summary>

`sandbox-download.py --release latest` downloads without provisioning; explicit tags
also work. Verified images stay under `dist/images/`; `current` selects the latest
download without overwriting older images. Keep the cache for future provisioning.

For a local image, use `sandbox-provision.py rollout --image-dir dist/workspace-dev`.
The download helper also accepts extracted CI assets (`--from-dir`) or an existing
artifact ZIP (`--from-zip`). CI artifacts expire after seven days and require GitHub
sign-in; public releases do not. `--output NEW_DIRECTORY` selects an explicit cache location.

</details>

## What is installed

| Component | Installation |
| --- | --- |
| `pairtask` (and `pk`), `pairtask-broker`, `pairtask-codex`, `pairtask-mcp` | Compiled from the selected app source inside the image; Go tests run first, the Go toolchain is removed afterwards |
| Git, ripgrep, tmux, OpenSSH, Python, `gh`, AppArmor | Ubuntu 24.04 packages |
| Tool store launchers | `go`, `gofmt`, `codex`, `claude`, `node`, `npm`, `npx`, `cs-mcp`, `rtk`, `starship` launch from the [tool store](sandbox-tools.md), which the rollout fills from `tools.lock.json` and the release bundles |
| Codex, Claude Code, Go, RTK, Starship, Node, CodeScene MCP, Playwright + Chromium, Docker | **Not in the image.** Installed at rollout, checksum-verified; the optional ones follow the tool selection |
| Claude collaboration config | `/etc/pairtask/claude-mcp.json` |
| Interactive shell aliases | `/etc/profile.d/collab-agent-aliases.sh`; `codex`, `claude` and `dashboard` |
| Collaboration skill and build manifests | `/usr/local/share/pairtask/` |

No host files, credentials or private keys enter the image. SSH host keys are
generated per workspace; Claude auto-updates are disabled. The broker starts at
boot, in dev or [control](secured-runtime.md). [Persistent storage](sandbox-storage.md).

## Development builds

Build locally only when you need a custom source or tool version:

```sh
# macOS; Linux: --remote local. Defaults use tools.lock.json.
python3 scripts/sandbox-image.py --remote colima-pairtask \
  --collab-ref HEAD --output dist/workspace-dev
```

| Control | Selection |
| --- | --- |
| `--collab-ref TAG_OR_SHA` | Build a locally available commit/tag; fetch it first if needed |
| `--collab-ref working-tree` | Default: tracked local edits, excluding untracked files |
| `--codex-version`, `--claude-version`, `--go-version` | Exact versions; defaults come from the lock |
| `--base-image SHA256` | Ubuntu 24.04 fingerprint for the build server's architecture |
| `--lock-file FILE` | A custom complete lock file, including checksums |
| `--repo OWNER/REPO` | Repository holding the base image mirror releases (default `pairtask/pairtask`) |
| `--output DIRECTORY` | New directory for the build; existing directories are never overwritten |

`tools.lock.json` (schema 2) has one `artifacts` entry per third-party tool:
its version, kind (`archive`, `binary` or `zip`), a URL and entry template, and
a `sha256` plus the vendor's architecture label per machine architecture.
`architectures` holds the Ubuntu base image fingerprint per architecture. The
same table drives the image build and, with [sandbox tools](sandbox-tools.md),
the host-side artifact cache.

Each release also references the optional tool bundles built by
`scripts/sandbox-bundles.py` (Playwright MCP with Chromium, Chromium's apt
packages, Docker's apt packages). Its `collab-ai-bundles-linux-<arch>.json`
holds their digests and names the `bundles-<digest>` release they're
published in, once per set of inputs; see
[bundle releases](sandbox-tools.md#bundle-releases).

Overrides resolve publisher checksums; unsupported packages and moving versions
such as `latest` are rejected. The manifest records source commit, tracked edits,
recipe file hashes, tool versions and checksums. The recipe comes from your current
checkout, even when building another app commit. `--network` / `--storage-pool`
select non-default Incus resources.

Build on the target architecture. The builder uses an unprivileged temporary
`collab-build-*` project (4 CPUs, 4 GiB RAM, 12 GiB disk), an allowlist of source files,
no host mounts, and no model calls. It verifies Go dependencies and cleans up its
own project on failure. After a forced kill, inspect the printed project before
removing it; Incus 6.0 LTS requires deleting its instances/images before the project.

Output: `workspace.tar.gz`, manifest, `image.tfvars.json` and release files.
Pass its output directory with `--image-dir` for [initial setup](sandbox.md#2-install-or-upgrade)
or [upgrade](sandbox-storage.md#replace-dev-retain-data). Rebuilds are not bit-identical: OS packages
resolve at build time and metadata includes timestamps. OS versions are recorded.

## CI and releases

[CI](../.github/workflows/ci.yml) runs Go/Python/OpenTofu checks and native ARM64/AMD64
image tests: provisioning, SSH, installed tools, broker, persistence, replacement,
network opt-out and control boundaries. Tests use no credentials or model requests.

A pull request that changes only unshipped documentation skips the image proofs.
That means `README.md` or a Markdown page directly under `docs/`, outside every
path the image is built from. A `classify` job decides with
[`scripts/ci-classify.py`](../scripts/ci-classify.py). Both per-architecture
image checks still run and report, with their steps skipped. Any other path,
pushes, tags and manual runs always build. So does any failure to read the diff.
Publishing also requires that explicit decision.

Successful pushes to `main` publish both architectures under a unique
`workspace-v0.0.0-build.RUN.ATTEMPT` tag and make that release `latest`.
Explicit `workspace-vX.Y.Z` tags publish versioned releases too. Publication happens
only after all checks and both image builds pass; incomplete uploads stay in draft.
Published assets are never overwritten. `latest` becomes available after the first
successful main release build.

### Base images

`images.linuxcontainers.org` keeps only the last three daily Ubuntu builds, so a
pinned base image disappears upstream within days. Each pin is therefore mirrored
once into its own release, `base-<fingerprint>`, holding `incus.tar.xz` and
`rootfs.squashfs`. The fingerprint is sha256 over those two files in that order,
as Incus computes it, and is the only trust root.

- **Lookup:** the image build, the bundle build and the rollout's operator
  container look for the pin in this order:
  1. the Incus project's image store;
  2. the verified cache in `dist/base/<fingerprint>/`;
  3. the mirror release;
  4. upstream, as a last resort.
  Only confirmed absence moves on to the next source. A checksum mismatch, a
  network or API error, or a failed import stops the build. The operator uses
  the pin from the host checkout, not the selected release.
- **Publishing:** the `base-mirror` CI job runs on `main` and `workspace-v*`
  pushes, one at a time per repository, and the release job waits for it. It
  stages a draft, verifies the uploaded pair, and then publishes it without
  making it `latest`. A published mirror is never changed. An interrupted draft
  is resumed from its own verified assets, so a pin that upstream already
  pruned isn't lost.
- **PRs:** `checks` runs `scripts/base-image-mirror.py check`, which confirms
  each pin is mirrored or still upstream. It checks availability only; builds
  verify the bytes. A new pin builds from upstream in its PR and is mirrored
  when it merges.
- **Refreshing:** run `python3 scripts/base-image-refresh.py` to pin the newest
  upstream builds for every architecture (for example for security updates),
  then open a PR. It changes nothing unless every architecture resolves.

Keep a `base-*` release while any published workspace release pins it: building
or rolling out that release needs it.

**Run workflow** accepts development source/tool overrides without publishing.
