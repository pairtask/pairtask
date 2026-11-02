# Renamed in pairtask

collab-ai became pairtask. Commands, programs, services and settings carry the
new name. Old names keep working as described below, so existing instructions,
scripts and older host checkouts don't break all at once.

## Commands you type

| Old | New | Old name |
| --- | --- | --- |
| `collab status`, `collab dashboard`, `collab budget …`, `collab operator …` | `pairtask …` (short: `pk …`) | Works for one more release; prints a deprecation line on stderr |
| `collab-supervised-codex BUDGET` (secured admin) | `pairtask-supervised-codex BUDGET` | Kept: older host checkouts call it |
| `collab-codescene-login` | `pairtask-codescene-login` | Works for one more release |
| `COLLAB_SOCKET_PATH`, `COLLAB_DB_PATH`, `COLLAB_BUDGET_DIR`, `COLLAB_OPERATOR_HOME`, … | `PAIRTASK_…` | Works for now when the `PAIRTASK_` name is unset; one deprecation line ([details](troubleshooting.md)) |
| MCP server `collab` (Claude: `mcp__collab__*`) | `pairtask` (`mcp__pairtask__*`) | New Claude launches use the new name; running sessions keep theirs |
| Agent skill `collab-ai` (`docs/skills/collab-ai`) | `pairtask` (`docs/skills/pairtask`) | New images register only `pairtask`. Rename a link you made by hand on your host, for example `~/.claude/skills/collab-ai` → `~/.claude/skills/pairtask` |

## Inside the sandbox image

- **Internal programs** moved off `PATH` to `/usr/local/lib/pairtask/`, under
  `pairtask-*` names (`pairtask-broker`, `pairtask-mcp`, `pairtask-codex`,
  `pairtask-tool`, `pairtask-tools-activate`, `pairtask-tools-ready`, …). The tool
  launchers (`go`, `codex`, `claude`, `node`, …) are still on `PATH`.
- **Services** are `pairtask-*.service` (and `pairtask-dev-executor.socket`).
- **Configuration** lives in `/etc/pairtask`, and shared image files in
  `/usr/local/share/pairtask`.

Images that use these names carry a layout marker
([image layout](sandbox-tools.md#image-layout)). Old names that host checkouts
rely on stay as permanent forwarders: the readiness and activation helpers,
`collab-codex`, `collab-supervised-codex`, `/etc/collab-ai`,
`/usr/local/share/collab-ai` and the `collab-*` service names. Other old program
names (`collab-mcp`, `broker`, `collab-tool`, …) keep working for one more
release. The full list is `infra/image/layout-v2.json`.

## On your host

| Old | New | Old name |
| --- | --- | --- |
| Colima profile `collab-ai` (`colima stop collab-ai`) | `pairtask` (`colima stop pairtask`) | Not migrated; see below |
| Incus remote `colima-collab-ai:` | `colima-pairtask:` | Not migrated; `--remote colima-collab-ai` is still recognized and prints a deprecation line |
| Incus project `collab-ai` (`--project collab-ai`) | `pairtask` | Not migrated |
| Host state `~/.local/state/collab-ai/operator/` and `…/shares/` | `~/.local/state/pairtask/…` | Not migrated |
| Instance marker `user.collab-ai.managed` | `user.pairtask.managed` | Written by the next apply; the host finds managed instances by name and project, never by this key |
| Disposable CI projects `collab-smoke-*`, `collab-operator-*` | `pairtask-smoke-*`, `pairtask-operator-*` | Test deployments only |

Existing deployments are not migrated, and this host does not drive them.
Deleting the VM deletes its projects and agent home, so
[back up](sandbox-storage.md#back-up-and-restore) anything you need first. Then
stop and delete the old VM (`colima stop collab-ai`, `colima delete collab-ai`)
and follow the [first install](sandbox.md).

## Kept on purpose

These names live in stored state or saved sessions, so renaming them would
mean migrating existing data:

- Data paths inside the image: `/var/lib/collab-ai*`, `~/.local/state/collab-ai/`
  (budgets and setup markers), the broker database, and the `/mnt/collab-*` mounts.
- `/tmp/collab-ai.sock`, the `collab-clients` group and `sshd_config.collab-ai`.
- Release asset names (`collab-ai-workspace-*`, `collab-ai-bundle*`).
- The skill directory `collab-ai`.
- Codex's session tool names (`collab_runtime`, `collab_receive`, `collab_*`),
  which saved threads record ([details](host-integration.md)).
