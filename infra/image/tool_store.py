"""The tool store: immutable digest-keyed artifacts, atomic generations and a
durable maintenance state. See docs/sandbox-tools.md.

Runs as root inside a container (activator, readiness, launcher) and in host
tests. It never downloads: artifacts arrive from the host, already named by
digest, and are verified again here before anything is extracted.
"""

import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import tarfile
import tempfile
import time
import uuid
import zipfile

STORE = Path("/var/lib/collab-ai-tools")
STATE = Path("/var/lib/collab-ai/tools-state.json")
ROOT_DEPS = Path("/var/lib/collab-ai/root-deps.json")
SHARE = Path("/usr/local/share/pairtask")
ETC = Path("/etc")
MODE_FILE = SHARE / "tools-mode"
LOCK_FILE = SHARE / "tools.lock.json"
SOURCE_FILE = SHARE / "build-source.json"
KINDS = ("archive", "binary", "zip", "debs")
REQUIRED_MANIFEST = ("release", "lock_sha256", "architecture", "selection", "tools")
# Optional tools and the MCP server each one provides (None: no server).
OPTIONAL_TOOLS = {"cs-mcp": "codescene", "playwright": "playwright", "docker": None, "pi": None}
# Launcher names that are not a tool's own entry: (tool, path in the artifact
# or a sibling of the entry).
TOOL_NAMES = {"gofmt": ("go", "go/bin/gofmt"), "npm": ("node", "npm"), "npx": ("node", "npx")}


class StoreError(ValueError):
    pass


def file_sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def mode(path=MODE_FILE):
    """`baked`: the image carries the tools and the store is exercised only;
    `store`: the launchers depend on an activated generation."""
    try:
        value = path.read_text().strip()
    except FileNotFoundError:
        return "baked"
    if value not in ("baked", "store"):
        raise StoreError(f"Unknown tools mode {value!r}")
    return value


def read_json(path, default=None):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return default


def read_state(path=STATE):
    return read_json(path)


def fsync_path(path):
    """Flush a file's contents, or a directory's entries, to stable storage."""
    descriptor = os.open(path, os.O_RDONLY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def fsync_tree(root):
    """Flush every file and directory under root; the completion marker must
    never outlive the content it vouches for."""
    for directory, _, names in os.walk(root):
        for name in names:
            path = Path(directory) / name
            if not path.is_symlink():
                fsync_path(path)
        fsync_path(directory)


def write_durable(path, text):
    """Write text so that after a crash the file is either absent/old or complete."""
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(path.name + ".tmp")
    temporary.write_text(text)
    fsync_path(temporary)
    temporary.replace(path)
    fsync_path(path.parent)


def write_state(value, path=STATE):
    write_durable(path, json.dumps(value, indent=2) + "\n")


def generation_id(manifest):
    return hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()[:16]


def manifest_for(lock, release, selection, tools):
    """The host builds this from the release's lock. `release` carries the
    digests this root must match: {"release", "lock_sha256", "architecture"}."""
    from tool_lock import artifact
    entries = {name: tool_entry(artifact(lock, name, release["architecture"])) for name in tools}
    return {**release, "selection": selection, "tools": entries}


def tool_entry(item):
    """One manifest entry: the artifact arrives under its URL's file name."""
    return {"kind": item["kind"], "entry": item["entry"], "sha256": item["sha256"],
            "file": PurePosixPath(item["url"]).name, "version": item["version"]}


def validate_manifest(manifest):
    missing = [key for key in REQUIRED_MANIFEST if key not in manifest]
    if missing:
        raise StoreError(f"Manifest lacks {', '.join(missing)}")
    for name, tool in manifest["tools"].items():
        validate_tool(name, tool)


def validate_tool(name, tool):
    if tool.get("kind") not in KINDS:
        raise StoreError(f"Tool {name} has an unsupported kind")
    if not valid_digest(tool.get("sha256")):
        raise StoreError(f"Tool {name} has no digest")
    validate_tool_paths(name, tool)


def valid_digest(value):
    return isinstance(value, str) and len(value) == 64


def validate_tool_paths(name, tool):
    if not tool.get("entry") or not tool.get("file"):
        raise StoreError(f"Tool {name} lacks its entry or file name")
    if "/" in tool["file"]:
        raise StoreError(f"Tool {name} has an invalid file name")


# Durable commit order: every artifact's content and `.complete`, the
# generation's manifest and its directory, then `current`, are flushed before
# the terminal `ready` state is written; a crash at any point leaves a root
# that is either not ready or ready with a complete store.

# Archive member policy. Python's tarfile "data" filter is the second layer.

def normalized(name):
    path = PurePosixPath(name)
    if path.is_absolute():
        raise StoreError(f"Archive member escapes the artifact: {name!r}")
    parts = [part for part in path.parts if part != "."]
    if not parts:
        raise StoreError("Archive member has an empty name")
    if ".." in parts:
        raise StoreError(f"Archive member escapes the artifact: {name!r}")
    return PurePosixPath(*parts)


def link_target_inside(member_path, linkname):
    target = PurePosixPath(linkname)
    if target.is_absolute():
        raise StoreError(f"Absolute link target in archive: {linkname!r}")
    resolved = os.path.normpath(str(member_path.parent / target))
    if resolved.startswith("..") or resolved == "..":
        raise StoreError(f"Link target escapes the artifact: {linkname!r}")


def validate_members(members):
    """Reject anything outside: regular files, directories, symlinks that stay
    inside, and hardlinks to earlier regular members. Names are unique after
    normalization, including case-insensitively."""
    seen, folded, regular = set(), set(), set()
    for member in members:
        if is_archive_root(member):
            continue
        path = normalized(member.name)
        key = str(path)
        if key in seen or key.lower() in folded:
            raise StoreError(f"Duplicate or colliding archive member: {member.name!r}")
        validate_member_type(member, path, regular)
        seen.add(key)
        folded.add(key.lower())


def is_archive_root(member):
    """`tar -C dir .` records the directory itself as `.`; harmless, not a name."""
    return member.isdir() and PurePosixPath(member.name).parts in ((), (".",))


def validate_member_type(member, path, regular):
    if member.isreg():
        regular.add(str(path))
    elif member.issym():
        link_target_inside(path, member.linkname)
    elif member.islnk():
        require_known_hardlink(member, regular)
    elif not member.isdir():
        raise StoreError(f"Unsupported archive member type: {member.name!r}")


def require_known_hardlink(member, regular):
    if str(normalized(member.linkname)) not in regular:
        raise StoreError(f"Hardlink to an unknown member: {member.name!r}")


def extract_tar(archive, destination):
    with tarfile.open(archive, "r:*") as tar:
        members = tar.getmembers()
        validate_members(members)
        tar.extractall(destination, members=members, filter="data")


def extract_zip(archive, destination, entry):
    with zipfile.ZipFile(archive) as package:
        matches = [item for item in package.infolist() if item.filename == entry]
        if len(matches) != 1:
            raise StoreError(f"Expected exactly one {entry} in the zip artifact")
        target = destination / normalized(entry)
        target.parent.mkdir(parents=True, exist_ok=True)
        with package.open(matches[0]) as source, target.open("xb") as output:
            shutil.copyfileobj(source, output, length=1024 * 1024)


def install_artifact(kind, source, destination, entry):
    destination.mkdir()
    if kind in ("archive", "debs"):
        extract_tar(source, destination)
    elif kind == "zip":
        extract_zip(source, destination, entry)
    else:
        shutil.copyfile(source, destination / entry)
    check_entry(kind, destination, entry)
    root_owned(destination)


def check_entry(kind, destination, entry):
    """A deb set holds packages; every other kind has its executable entry."""
    if kind == "debs":
        if not any(destination.glob("*.deb")):
            raise StoreError("Package artifact holds no .deb files")
        return
    target = destination / normalized(entry)
    if not target.is_file():
        raise StoreError(f"Artifact lacks its entry {entry}")
    if kind != "archive":
        target.chmod(0o755)


def root_owned(directory):
    """Vendor archive owners never apply; everything installed is root's and
    never group- or world-writable."""
    for path in [directory, *directory.rglob("*")]:
        if path.is_symlink():
            continue
        if os.geteuid() == 0:
            os.chown(path, 0, 0, follow_symlinks=False)
        path.chmod(path.stat().st_mode & ~(stat.S_IWGRP | stat.S_IWOTH))


# Root dependencies: deb sets change the root disk, not the store, so each root
# records what it installed and re-verifies the packages on every activation.

POLICY_RC = Path("/usr/sbin/policy-rc.d")


def dpkg_environment():
    return dict(os.environ, DEBIAN_FRONTEND="noninteractive", PATH="/usr/sbin:/usr/bin:/sbin:/bin")


def install_debs(directory, run=subprocess.run, policy=None):
    """Install a staged deb set offline. No daemon may start from a postinst:
    the gated units start services after the release, never dpkg."""
    policy = policy or POLICY_RC
    created = not policy.exists()
    if created:
        policy.write_text("#!/bin/sh\nexit 101\n")
        policy.chmod(0o755)
    try:
        packages = sorted(str(path) for path in directory.glob("*.deb"))
        run(["dpkg", "-i", "--force-confold", *packages], check=True, env=dpkg_environment())
    finally:
        if created:
            policy.unlink(missing_ok=True)


BWRAP_PROFILE = "bwrap-userns-restrict"


def after_install(name, run=subprocess.run):
    """Root-level follow-ups a deb set needs. Docker's rootless setup relies on
    the bwrap user-namespace profile that Noble ships as an extra profile."""
    if name != "docker":
        return
    source = Path("/usr/share/apparmor/extra-profiles") / BWRAP_PROFILE
    target = Path("/etc/apparmor.d") / BWRAP_PROFILE
    if source.exists():
        shutil.copyfile(source, target)
        target.chmod(0o644)
        run(["apparmor_parser", "-r", str(target)], check=False, env=dpkg_environment())


def installed_version(package, run=subprocess.run):
    result = run(["dpkg-query", "-W", "-f=${db:Status-Status} ${Version}", package],
                 capture_output=True, text=True, env=dpkg_environment())
    if result.returncode != 0:
        return None
    status, _, version = result.stdout.strip().partition(" ")
    return version if status == "installed" else None


def verify_packages(name, tool, run=subprocess.run):
    for package, version in tool.get("packages", {}).items():
        if installed_version(package, run) != version:
            raise StoreError(f"Tool {name}: package {package} is not installed at {version}")


# Derived configuration: the agents' MCP servers and the per-tool markers
# follow the selection, so a disabled tool has no server entry and no unit.

def enabled(selection, tool):
    return bool(selection.get(tool, False))


def chosen_servers(selection, servers):
    return {server: spec for tool, server in OPTIONAL_TOOLS.items()
            if server in servers and enabled(selection, tool) for spec in [servers[server]]}


def write_configuration(selection, share=SHARE, etc=ETC):
    """Write the agents' MCP configuration and the tool markers for selection.
    The base files ship with the image; without them (a store exercised
    outside an image) the image's own configuration stands."""
    servers = read_json(share / "mcp-servers.json")
    if servers is None:
        return
    chosen = chosen_servers(selection, servers)
    claude = read_json(share / "claude-mcp.base.json", {"mcpServers": {}})
    claude["mcpServers"] = {**claude.get("mcpServers", {}), **chosen}
    write_durable(etc / "pairtask/claude-mcp.json", json.dumps(claude, indent=2) + "\n")
    base = share / "codex-config.base.toml"
    codex = base.read_text() if base.exists() else ""
    for server, spec in chosen.items():
        codex += f"\n[mcp_servers.{server}]\n"
        codex += "\n".join(f"{key} = {json.dumps(value)}" for key, value in spec.items()) + "\n"
    write_durable(etc / "codex/config.toml", codex)
    write_markers(selection, etc / "pairtask/tools")


def write_markers(selection, directory):
    directory.mkdir(parents=True, exist_ok=True)
    for tool in OPTIONAL_TOOLS:
        marker = directory / f"{tool}.enabled"
        if enabled(selection, tool):
            marker.write_text("")
        else:
            marker.unlink(missing_ok=True)
    fsync_path(directory)


class Store:
    def __init__(self, root=STORE, state=STATE, root_deps=ROOT_DEPS, run=subprocess.run):
        self.root = Path(root)
        self.state_path = Path(state)
        self.root_deps = Path(root_deps)
        self.run = run
        self.artifacts = self.root / "artifacts"
        self.generations = self.root / "generations"
        self.current = self.root / "current"
        self.share, self.etc = SHARE, ETC

    # Reading

    def current_manifest(self):
        try:
            return json.loads((self.current / "manifest.json").read_text())
        except (FileNotFoundError, NotADirectoryError):
            return None

    def status(self):
        state = read_state(self.state_path)
        manifest = self.current_manifest()
        return {"mode": mode(), "state": state,
                "current": generation_id(manifest) if manifest else None,
                "manifest": manifest, "complete": sorted(self.complete_artifacts())}

    def complete_artifacts(self):
        if not self.artifacts.is_dir():
            return set()
        return {path.name for path in self.artifacts.iterdir() if (path / ".complete").exists()}

    def resolve(self, tool):
        """The absolute entry path of tool in the current generation; None for a
        tool that isn't installed or has no executable (a deb set)."""
        spec = (self.current_manifest() or {}).get("tools", {}).get(tool)
        if spec is None or spec["kind"] == "debs":
            return None
        return (self.current / tool).resolve() / normalized(spec["entry"])

    def resolve_name(self, name):
        """The executable for a launcher name: a tool's own entry, or a sibling
        such as gofmt, npm or npx; None when its tool isn't installed."""
        tool, relative = TOOL_NAMES.get(name, (name, None))
        entry = self.resolve(tool)
        if entry is None or relative is None:
            return entry
        if "/" in relative:
            return (self.current / tool).resolve() / normalized(relative)
        return entry.parent / relative

    # Activation

    def activate(self, manifest, incoming):
        validate_manifest(manifest)
        generation = generation_id(manifest)
        self.root.mkdir(parents=True, exist_ok=True)
        with (self.root / "lock").open("w") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            # Decided under the lock: a concurrent activation that just
            # released this root must not be followed by a second one.
            self.require_unreleased()
            write_state({"state": "maintenance", "generation": generation, "started": time.time()}, self.state_path)
            self.remove_incomplete()
            for name, tool in manifest["tools"].items():
                self.stage(name, tool, Path(incoming))
            self.install_root_deps(manifest)
            write_configuration(manifest["selection"], self.share, self.etc)
            self.build_generation(generation, manifest)
            os.chmod(self.artifacts, 0o755)
            os.chmod(self.generations, 0o755)
            self.flip(generation)
            self.prune()
            write_state({"state": "ready", "generation": generation, "activated": time.time()}, self.state_path)
        return generation

    def require_unreleased(self):
        """Only a root with no state, or one still in maintenance, may be
        mutated; ready, or any state this code doesn't know, means released."""
        state = read_state(self.state_path)
        if state is not None and state.get("state") != "maintenance":
            raise StoreError("This root already has an activated generation; tools change only on a new root")

    def remove_incomplete(self):
        for directory in (self.artifacts, self.generations):
            directory.mkdir(mode=0o700, exist_ok=True)
            os.chmod(directory, 0o700)
        remove_unless_marked(self.artifacts, ".complete")
        remove_unless_marked(self.generations, "manifest.json")

    def stage(self, name, tool, incoming):
        destination = self.artifacts / tool["sha256"]
        if (destination / ".complete").exists():
            return
        source = incoming / tool["file"]
        if not source.is_file() or source.is_symlink():
            raise StoreError(f"Artifact for {name} was not delivered")
        if file_sha256(source) != tool["sha256"]:
            raise StoreError(f"Artifact for {name} does not match its digest")
        staging = Path(tempfile.mkdtemp(prefix=".stage-", dir=self.artifacts))
        try:
            install_artifact(tool["kind"], source, staging / "content", tool["entry"])
            fsync_tree(staging / "content")
            write_durable(staging / "content" / ".complete",
                          json.dumps({"tool": name, "version": tool.get("version")}) + "\n")
            (staging / "content").rename(destination)
            fsync_path(self.artifacts)
        finally:
            shutil.rmtree(staging, ignore_errors=True)

    def install_root_deps(self, manifest):
        """Install each deb set this root lacks, then verify every package of
        every deb set, cached or not: `.complete` says nothing about a root."""
        record = read_json(self.root_deps, {"installed": {}})
        for name, tool in manifest["tools"].items():
            if tool["kind"] != "debs":
                continue
            if tool["sha256"] not in record["installed"]:
                install_debs(self.artifacts / tool["sha256"], self.run)
                after_install(name, self.run)
                record["installed"][tool["sha256"]] = {"tool": name, "installed_at": time.time()}
                write_durable(self.root_deps, json.dumps(record, indent=2) + "\n")
            verify_packages(name, tool, self.run)

    def build_generation(self, generation, manifest):
        target = self.generations / generation
        if target.exists():
            shutil.rmtree(target)
        staging = self.generations / (".build-" + uuid.uuid4().hex)
        staging.mkdir()
        for name, tool in manifest["tools"].items():
            (staging / name).symlink_to(Path("..") / ".." / "artifacts" / tool["sha256"])
            if tool["kind"] != "debs" and not (staging / name / normalized(tool["entry"])).is_file():
                raise StoreError(f"Generation validation failed: {name} has no entry {tool['entry']}")
        write_durable(staging / "manifest.json", json.dumps(manifest, indent=2, sort_keys=True) + "\n")
        staging.rename(target)
        fsync_path(self.generations)

    def flip(self, generation):
        temporary = self.root / (".current-" + uuid.uuid4().hex)
        temporary.symlink_to(Path("generations") / generation, target_is_directory=True)
        try:
            os.replace(temporary, self.current)
            fsync_path(self.root)
        finally:
            temporary.unlink(missing_ok=True)

    def prune(self):
        """Keep the current and the newest previous generation and the
        artifacts they reference; this only ever runs inside an activation,
        with no tool process alive on this root."""
        current = self.current.resolve().name
        others = sorted((path for path in self.generations.iterdir() if path.name != current),
                        key=lambda path: path.stat().st_mtime, reverse=True)
        keep = {current} | {path.name for path in others[:1]}
        referenced = set()
        for name in keep:
            manifest = json.loads((self.generations / name / "manifest.json").read_text())
            referenced |= {tool["sha256"] for tool in manifest["tools"].values()}
        for path in others[1:]:
            shutil.rmtree(path)
        for path in self.artifacts.iterdir():
            if path.name not in referenced:
                shutil.rmtree(path)


def remove_unless_marked(directory, marker):
    """Entries without their completion marker are garbage from a crash."""
    for path in directory.iterdir():
        if not (path / marker).exists():
            shutil.rmtree(path)


def ready(store=None, lock_file=LOCK_FILE, source_file=SOURCE_FILE, mode_file=MODE_FILE):
    """Return None when tool entry points may start, else the reason they may not."""
    if mode(mode_file) == "baked":
        return None
    manifest, reason = active_generation(store or Store())
    if reason:
        return reason
    return release_mismatch(manifest, lock_file, source_file)


def active_generation(store):
    """The current manifest when the state says ready for it, else the reason."""
    state = read_state(store.state_path)
    if state is None or state.get("state") != "ready":
        return None, "sandbox tools are in maintenance; run the rollout to activate them"
    manifest = store.current_manifest()
    if manifest is None or generation_id(manifest) != state.get("generation"):
        return None, "the active tool generation does not match the recorded state"
    return manifest, None


def release_mismatch(manifest, lock_file, source_file):
    if manifest["lock_sha256"] != file_sha256(lock_file) or manifest["release"] != file_sha256(source_file):
        return "tools were activated for a different release; run the rollout"
    return None
