# Persistent sandbox data

Dev data lives on three Incus volumes, mounted only in `workspace`:

| Volume | Mount | Default quota | Ownership |
| --- | --- | --- | --- |
| `workspace-data` | `/workspace` | 10 GiB (`workspace_gib`) | `agent:agent`, directory `0750` |
| `agent-home` | `/home/agent` | 2 GiB (`agent_home_gib`) | `agent:agent`, directory `0700` |
| `docker-data` | `/var/lib/collab-ai-docker` | 10 GiB (`docker_gib`) | `agent:agent`; `0700` initially, Docker sets `0710`; no access for others |
| `workspace-tools` | `/var/lib/collab-ai-tools` | 4 GiB (`tools_gib`) | `root:root`, `0755`; the [tool store](sandbox-tools.md), written only by the host's rollout |
| `secured-tools` (secured only) | `/var/lib/collab-ai-tools` | 2 GiB (`secured_tools_gib`) | `root:root`, `0755`; secured's own store, never shared with the workspace |

These volumes survive restart and replacement. Both agents share the home;
existing settings are preserved. Stored sessions do not prove native resume across
versions. Host homes/memory are not imported automatically.
Docker images, containers and named volumes also persist. Their restart policies
determine which services resume when Docker starts.

Files outside these mounts—including standalone broker history—are lost on root-disk
replacement. [Control mode](secured-runtime.md) keeps broker history and budgets on
its private volume. Optional [host mounts](sandbox-mounts.md) store data on the host,
not in `workspace-data`.

## Resize

`pairtask size show` reports how big the sandbox is and how full each volume is:

```text
VM:        4 CPU · 8 GiB RAM · 40 GiB disk   (pool default 38 GiB, 6.1 GiB used)
workspace: 2 CPU · 4 GiB RAM · root 10 GiB (31%)
volumes:   agent-home 2 GiB (98% ⚠) · docker-data 10 GiB (22%) · workspace-data 10 GiB (1%) · workspace-tools 4 GiB (40%)
warning:   agent-home is 98% full; clear space or grow it (docs/sandbox-storage.md#resize)
```

It only reads, through Incus and the VM's settings; `--json` prints the same as
JSON. A volume at 90% or more gets a warning. `pairtask start` also refuses to
start an agent session when the agent home is nearly full, and lists the caches
that are safe to clear.

To grow the workspace or a volume today, write your own sizes next to the
deployment's managed `operator.auto.tfvars.json`, in a file of your own such as
`size.auto.tfvars.json` (the deployment lives in
`~/.local/state/pairtask/operator/<project>/`), then review and apply:

```json
{"memory_gib": 8, "agent_home_gib": 10}
```

```sh
pairtask plan     # expect only changes, no replacements
pairtask apply
```

The sizes are `cpu_count` (1–8), `memory_gib` (1–16), `disk_gib` (root disk,
2–100) and the volume quotas in GiB: `workspace_gib`, `agent_home_gib`,
`docker_gib`, `tools_gib` (1–100). Only ever raise these by hand: nothing on
this path stops a smaller value, and shrinking a disk or quota below what it
holds can fail or lose data. (The volumes themselves refuse to be destroyed.)
`pairtask size plan|apply`, coming next in #382, refuses to shrink.

The workspace can't use more than the VM has. On macOS, check the VM's line in
`pairtask size show` first: growing the VM itself still means editing its
Colima profile by hand, which a later step of [#382](https://github.com/pairtask/pairtask/issues/382)
replaces with a supported command.

## Back up and restore

The commands below export projects and agent home. For Docker, stop its workloads
and daemon first, then use [Incus volume backup](https://linuxcontainers.org/incus/docs/main/howto/storage_backup_volume/)
for `docker-data`; keep its numeric ownership. Do not restore it with `--no-same-owner`.

Exit agents, editors and other writers first; keep the container running for these
file-level exports. Unshare optional host mounts using the [mount guide](sandbox-mounts.md)
first, so the archives contain sandbox data only. Keep backups private and outside
a writable sandbox mount; a home may contain credentials once login is configured.

Run on the operator's host. These commands use macOS names; on Linux replace
`colima-pairtask:` with `local:`:

```sh
umask 077
incus --project pairtask exec colima-pairtask:workspace -T --user 1001 --group 1001 -- \
  tar -C /workspace --one-file-system -czf - . > workspace-data.tgz
incus --project pairtask exec colima-pairtask:workspace -T --user 1001 --group 1001 -- \
  tar -C /home/agent --one-file-system -czf - . > agent-home.tgz
# Both exports must succeed; inspect and retain the archives before changing storage.
tar -tzf workspace-data.tgz
tar -tzf agent-home.tgz
```

Restore into a **fresh, initialized** workspace, with no agents running. Substitute
its project name below if different. Extraction runs as `agent`, not root:

```sh
incus --project pairtask exec colima-pairtask:workspace -T --user 1001 --group 1001 -- \
  tar -C /workspace --no-same-owner --no-overwrite-dir -xzf - < workspace-data.tgz
incus --project pairtask exec colima-pairtask:workspace -T --user 1001 --group 1001 -- \
  tar -C /home/agent --no-same-owner --no-overwrite-dir -xzf - < agent-home.tgz
```

Check restored files, permissions and agent settings before discarding backups; hooks
and credentials are not validated. [Incus volume snapshots/exports](https://linuxcontainers.org/incus/docs/main/howto/storage_backup_volume/)
are another backup option. Persistence alone is not a backup.

## Replace dev, retain data

Run on the **host**, from the repository root. Exit agents and [back up](#back-up-and-restore)
first. Projects and agent home survive; processes and tmux sessions do not.
Standalone broker history is lost on replacement; control-mode history persists.

With Incus running, run from the repository root:

```sh
python3 scripts/sandbox-provision.py rollout
ssh -F infra/incus/ssh/config workspace pairtask status
ssh -F infra/incus/ssh/config workspace
```

This downloads `latest`, verifies it before stopping containers, replaces the runtime,
and refreshes SSH trust through Incus. Your private login key stays on the host.
Add `--release workspace-vX.Y.Z` to pin a version or `--image-dir dist/workspace-dev`
for a local build. Custom SSH state: `--ssh-state-dir DIRECTORY`.

Existing settings are retained, including `running = false`. On failure, inspect the
error before restarting; [state recovery](sandbox-operator.md) may be required.
**Do not destroy to upgrade.**

## Deliberate removal

`prevent_destroy = true` in [storage.tf](../infra/incus/storage.tf) blocks plans
that would delete or replace the dev volumes, including a full destroy. Changing
the project or storage pool must not silently replace them.

After backing up and deciding to delete the data, change that literal to `false`
in the deployment directory printed by the helper. Then review and apply a destroy plan:

```sh
python3 scripts/sandbox-provision.py plan --destroy
# This deletes the containers and their managed volumes, including dev data.
python3 scripts/sandbox-provision.py apply
```

Restore the guard to `true` before creating another deployment. The host VM and
shared storage pool remain. This is an operator safeguard, not an authorization
boundary: removing resource blocks/state or using Incus directly can bypass it.
[OpenTofu lifecycle behavior](https://opentofu.org/docs/language/meta-arguments/lifecycle/).
