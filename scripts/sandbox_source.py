"""Archive app source separately from the current, reviewed image recipe."""

import hashlib
import io
import json
from pathlib import Path
import subprocess
import tarfile


ROOT = Path(__file__).resolve().parents[1]
APP_PATHS = ["go.mod", "go.sum", "cmd", "internal"]
RECIPE_PATHS = ["infra/image/" + name for name in (
    "install.sh", "install-build-go.py", "install-layout.py", "layout_schema.py", "layout-v2.json", "sshd_config", "pairtask-broker.service",
    "pairtask-secured-setup.service", "pairtask-supervised-codex", "claude-mcp.json",
    "pairtask-secured-broker.service", "pairtask-dev-setup.service", "pairtask-dev-executor.socket",
    "pairtask-dev-executor@.service", "secured-codex.toml", "secured-environments.toml", "pairtask-executor-connect",
    "pairtask-client-setup.service", "pairtask-broker-setup",
    "pairtask-workspace-setup.service", "pairtask-workspace-setup",
    "pairtask-agent-aliases.sh", "pairtask-starship.sh", "install-skill.sh",
    "pairtask-rtk-setup", "pairtask-rtk-setup.service", "codex-config.toml",
    "docker-packages.txt", "pairtask-docker-setup",
    "pairtask-docker-session.service", "pairtask-docker-setup.service", "pairtask-user-workspace.conf",
    "tool_download.py", "tool_lock.py", "tool_store.py", "build-playwright-bundle.py",
    "pairtask-tools-activate", "pairtask-tools-ready", "pairtask-tool", "pairtask-tools-ready.service",
    "pairtask-tools-setup.service", "pairtask-playwright", "pairtask-codescene-login",
    "pairtask-chromium.apparmor", "mcp/package.json", "mcp/package-lock.json", "mcp/servers.json",
)] + ["docs/skills/collab-ai/" + name for name in (
    "SKILL.md", "references/managed.md", "references/manual.md", "references/operator.md", "references/sandbox.md",
)]


def git(*args, root=ROOT):
    return subprocess.run(["git", *args], cwd=root, check=True, capture_output=True).stdout


def commit(ref, root):
    return git("rev-parse", "--verify", "--end-of-options", f"{ref}^{{commit}}", root=root).decode().strip()


def repository_file(root, relative):
    source = root / relative
    if source.is_symlink():
        raise ValueError(f"Build input must not be a symlink: {relative}")
    if not source.resolve().is_relative_to(root.resolve()):
        raise ValueError(f"Build input escapes the repository: {relative}")
    if not source.is_file():
        raise ValueError(f"Build input must be a regular file: {relative}")
    return source.read_bytes()


def working_tree(root):
    names = git("ls-files", "-z", "--", *APP_PATHS, root=root).split(b"\0")
    return {name.decode(): repository_file(root, name.decode()) for name in names if name}


def committed_tree(root, revision):
    data = git("archive", "--format=tar", revision, *APP_PATHS, root=root)
    files = {}
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        for member in archive:
            if member.isdir():
                continue
            if not member.isfile():
                raise ValueError(f"Commit contains a non-regular build input: {member.name}")
            files[member.name] = archive.extractfile(member).read()
    return files


def app_source(root, ref):
    revision = commit("HEAD" if ref == "working-tree" else ref, root)
    metadata = {"requested_ref": ref, "commit": revision, "working_tree": ref == "working-tree"}
    if ref != "working-tree":
        return committed_tree(root, revision), metadata
    metadata["dirty"] = bool(git("status", "--porcelain", "--untracked-files=no", "--", *APP_PATHS, root=root))
    return working_tree(root), metadata


def add_file(archive, name, data):
    info = tarfile.TarInfo(name)
    info.size, info.mode = len(data), 0o644
    archive.addfile(info, io.BytesIO(data))


def source_archive(destination, lock=None, ref="working-tree", root=ROOT):
    files, app = app_source(root, ref)
    for relative in RECIPE_PATHS:
        files[relative] = repository_file(root, relative)
    if lock is None:
        lock = json.loads((root / "infra/image/tools.lock.json").read_text())
    files["infra/image/tools.lock.json"] = (json.dumps(lock, indent=2) + "\n").encode()
    provenance = {
        "collab": app,
        "recipe_commit": commit("HEAD", root),
        "files_sha256": {name: hashlib.sha256(data).hexdigest() for name, data in files.items()},
    }
    with tarfile.open(destination, "w") as archive:
        for name, data in sorted(files.items()):
            add_file(archive, name, data)
        add_file(archive, "build-source.json", (json.dumps(provenance, indent=2) + "\n").encode())
    return provenance
