# Mount a host project

## Where agents can edit

- **Sandbox copy:** agents can code; changes return through Git or a patch, not automatic host sync.
- **Host mount:** read-only by default. Writes require a dedicated sharing account; choose the Mac or Linux setup below.

For coding, clone or copy into an unmounted directory such as `/workspace/project-work`.

## Choose directories

Create `mounts.json` outside the repository:

```json
[
  {
    "project_name": "project",
    "host_path": "/absolute/path/to/your/project",
    "container_mount_path": "/workspace/project",
    "container_readonly": true
  }
]
```

`container_readonly` defaults to `true`: agents cannot edit or write build output
into the mount. Your host access stays unchanged.

Use existing, non-overlapping host directories and unique container paths directly
under `/workspace`. Project names must be unique: 1–30 lowercase letters, digits
or hyphens, starting with a letter. Host permissions still apply.

```sh
python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json
```

## Mac writable sharing

Use the current workspace image (includes SSHFS). Start workspace normally.
For this mode, keep each container path **identical to its host path**:

```json
[
  {
    "project_name": "etl-development",
    "host_path": "/Users/you/workspace/etl-development",
    "container_mount_path": "/Users/you/workspace/etl-development",
    "container_readonly": false
  }
]
```

List external symlink targets as separate entries; one parent entry covers its
children. `plan` reports unlisted targets, skipping common build/cache folders.
It never adds them automatically. Git worktrees also need their common repository
directory available at its original path.

```sh
# HOST: inspect first; setup creates the sharing account and grants directory access
python3 scripts/sandbox-share.py plan --mounts-file /path/to/mounts.json
python3 scripts/sandbox-share.py setup --mounts-file /path/to/mounts.json

# Keep running in a separate host terminal; sudo starts the restricted file helper
python3 scripts/sandbox-share.py run --mounts-file /path/to/mounts.json
```

Inside workspace, `cd` to that same path. Edits reach host files immediately.
Setup installs a root-owned helper outside shared projects. It drops to the
non-login `collab-share` account and clears supplementary groups before serving
files under a directory-restricted macOS sandbox. It exposes no host shell or port.
Existing ownership stays unchanged. You can edit newly created files too:
files written from the workspace belong to `collab-share` and inherit your
access from the directory. A later `setup` leaves those entries alone, since
only an owner may change an ACL, and grants the rest as before.

**Existing Mac installs:** rerun `sandbox-share.py setup --mounts-file /path/to/mounts.json`
before upgrading macOS. Setup sets `RealName` on both sharing records, including
older accounts; missing names can break macOS account migration.
If only the group remains after a failed upgrade, setup stops with inspection
instructions. After the operator repairs the account/group, rerun setup to reapply ACLs.

**Stop:** Ctrl+C disconnects shares. Restart workspace before reconnecting.
Disconnected mounts refuse writes; files remain on the host. Account/ACL grants
remain after stopping. This mode does not use `mounts-apply` or require a VM restart.
Writable files can later execute on the host; [review them first](sandbox.md#security-can-agents-execute-code-on-my-host).

## Linux writable sharing

Set `"container_readonly": false` and create a dedicated account (Ubuntu/Debian):

```sh
sudo useradd --system --user-group --no-create-home --shell /usr/sbin/nologin collab-share
```

Use a unique UID, private same-name group, no supplementary groups, login shell
or sudo. Grant access only to the chosen directory:

```sh
share_dir=/absolute/path/to/your/project
sudo setfacl -R -m u:collab-share:rwX "$share_dir"
# Inherit access for the sharing account and your operator on new files/directories.
sudo find "$share_dir" -type d -exec setfacl -m "d:u:collab-share:rwx,d:u:$(id -u):rwx" {} +

# Allow Incus to map only this dedicated UID/GID (skip IDs already allocated).
share_uid=$(id -u collab-share)
share_gid=$(id -g collab-share)
sudo usermod --add-subuids "$share_uid-$share_uid" --add-subgids "$share_gid-$share_gid" root

python3 scripts/sandbox-host.py mounts-plan --mounts-file /path/to/mounts.json --share-user collab-share
```

Install `acl` if needed. Private parent directories may need traverse permission
for `collab-share`. Preserve existing UID/GID mappings and host file ownership.

Mounts do **not** prevent execution. Review host changes before running them.
[Security limits](sandbox.md#security-can-agents-execute-code-on-my-host).

## Apply

For **macOS read-only VirtioFS mounts**, stop the dedicated VM before changing mounts:

```sh
colima stop pairtask
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json
python3 scripts/sandbox-host.py apply
```

On **Linux**, generate the Incus variables directly:

```sh
python3 scripts/sandbox-host.py mounts-apply --mounts-file /path/to/mounts.json

# Add --share-user collab-share for writable mounts or access via that account.
```

The helper writes mount settings into the discovered deployment directory.
On **both platforms**, review and apply:

```sh
python3 scripts/sandbox-provision.py plan
python3 scripts/sandbox-provision.py apply
```

For a first deployment, also pass `--image-dir` to plan. `--output` still overrides
the mount-variable destination for advanced setups. Restart dev after UID mapping changes.

## Change or remove a mount

Stop dev and detach the changed mount first (Linux: use `local:`):

```sh
incus --project pairtask stop colima-pairtask:workspace
incus --project pairtask profile device remove colima-pairtask:offline host-project
```

When removing the last mount, also clear its identity mapping. For an identity
change, detach all host mounts before clearing it:

```sh
incus --project pairtask profile unset colima-pairtask:offline raw.idmap
```

Edit the list and repeat **Apply**. Use `[]` to remove all mounts; keep the empty
generated variables file. Host data and separately granted ACLs remain unchanged.
