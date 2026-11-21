"""pairtask update, the host's plan: which client versions, the lock that
pins them, each container's next manifest, and how its artifacts get there.

An update never changes a release's own lock. It writes an override: the
image's lock with nothing changed but the agent clients' versions and
checksums, which the container checks field by field before it trusts it.
Each container's next manifest is its current one with those clients
replaced, so the same client set always names the same generation and going
back needs no download.

The transaction itself is sandbox_tools_update.Update; this module gives it
its resolver and its containers.
"""

import copy
import hashlib
import json
from pathlib import Path
import subprocess
import sys
import tempfile

from sandbox_tools import CACHE, cache_artifact
from sandbox_tools_update import ContainerError, UNREACHABLE
import sandbox_versions as versions

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_lock import LANE_TOOLS, artifact  # noqa: E402
from tool_store import generation_id, tool_entry  # noqa: E402

STORE = "/var/lib/collab-ai-tools"
OVERRIDE_FILE = "override-lock.json"
# What the activator prints before a refusal; anything else on a failed
# exec is the transport.
REFUSAL_MARKS = ("pairtask-tools-activate:", "usage:")


def digest(data):
    return hashlib.sha256(data).hexdigest()


def encoded(lock):
    """The bytes an override lock is stored and digested as."""
    return (json.dumps(lock, indent=2, sort_keys=True) + "\n").encode()


def wanted_versions(requested, clients, latest=versions.latest):
    """An exact version for each client: the one asked for, else the
    publisher's default (Claude Code's stable channel; the latest release
    of the others)."""
    unknown = sorted(set(requested) - set(LANE_TOOLS))
    if unknown:
        raise ValueError("update changes only {}; not {}".format(", ".join(LANE_TOOLS), ", ".join(unknown)))
    return {name: versions.exact(requested[name], "--" + name) if requested.get(name) else latest(name) for name in clients}


def override_lock(base, wanted, checksum=versions.resolver):
    """base with each wanted client at its version, and the publisher's
    checksum for every architecture the lock has; nothing else differs."""
    lock = copy.deepcopy(base)
    for name, selected in sorted(wanted.items()):
        spec = lock["artifacts"][name]
        if spec["version"] == selected:
            continue
        lookup = checksum(name)
        spec["version"] = selected
        for per_arch in spec["architectures"].values():
            per_arch["sha256"] = lookup(selected, per_arch["arch"])
    return lock


def next_manifest(current, base_text, lock):
    """current with its agent clients taken from lock. A lock equal to the
    image's needs no override: that manifest is the release's own."""
    manifest = copy.deepcopy(current)
    for name in clients_of(manifest):
        manifest["tools"][name] = tool_entry(artifact(lock, name, manifest["architecture"]))
    manifest.pop("override", None)
    manifest["lock_sha256"] = digest(base_text)
    if lock != json.loads(base_text):
        manifest["lock_sha256"] = digest(encoded(lock))
        manifest["override"] = {"base_lock_sha256": digest(base_text)}
    return manifest


def clients_of(manifest):
    return [name for name in LANE_TOOLS if name in manifest["tools"]]


class Instance:
    """One container as the transaction sees it, over a
    sandbox_tools_install.Container."""

    def __init__(self, container):
        self.container, self.name = container, container.name

    @property
    def uuid(self):
        """Incus's immutable identity of this instance, read each time: a
        replaced container has another."""
        command = self.container.command + ["config", "get", self.container.target, "volatile.uuid"]
        with transport(self.name):
            value = self.container.run(command, check=True, capture_output=True, text=True).stdout.strip()
        if not value:
            raise ContainerError(UNREACHABLE, "{} has no instance UUID".format(self.name))
        return value

    def activate(self, subcommand, *args):
        """Run one activator step and return its JSON reply."""
        try:
            with transport(self.name):
                reply = self.container.output(self.container.layout.tools_activate, subcommand, *args)
        finally:
            if subcommand == "stage":
                self.discard(args)
        try:
            result = json.loads(reply)
            if not isinstance(result, dict):
                raise ValueError("expected a JSON object")
            return result
        except ValueError as error:
            raise ContainerError("refused", "{}: unreadable reply to {}".format(self.name, subcommand)) from error

    def discard(self, stage_args):
        """Remove a delivery once it was staged, or failed to be."""
        if "--incoming" in stage_args:
            incoming = stage_args[stage_args.index("--incoming") + 1]
            try:
                self.container.run(self.container.command + ["exec", self.container.target, "-T", "--", "rm", "-rf", incoming], check=False)
            except (OSError, subprocess.SubprocessError):
                pass  # delivery cleanup must not hide an activator result or failure

    def read(self, path):
        with transport(self.name):
            return self.container.output("cat", path)

    def share(self, name):
        return self.read("{}/{}".format(self.container.layout.share_dir, name))

    def provenance(self):
        """Release and image-lock identity, freshly read on every resume."""
        return {"release_sha256": digest(self.share("build-source.json").encode()),
                "base_lock_sha256": digest(self.share("tools.lock.json").encode())}

    def manifest(self, generation):
        return json.loads(self.read("{}/generations/{}/manifest.json".format(STORE, generation)))

    def complete(self):
        """The artifact digests the store already holds."""
        return self.activate_plain("status").get("complete", [])

    def activate_plain(self, subcommand):
        with transport(self.name):
            return json.loads(self.container.output(self.container.layout.tools_activate, subcommand))


def retained_lines(instances):
    """One line per container on what its kept generations cost; a store
    that can't be asked says so and is never shown as empty."""
    return [retained_line(role, instances[role]) for role in instances]


def retained_line(role, instance):
    try:
        reply = instance.activate_plain("status")
    except ContainerError as error:
        return "tools:     {}: retained generations unavailable ({})".format(role, error)
    info = reply.get("retained") if isinstance(reply, dict) else None
    if not counted(info):
        return "tools:     {}: retained generations unavailable (its tool store didn't report them)".format(role)
    return "tools:     {}: {} retained generations, {:.1f} MiB stored".format(role, info["generations"], info["stored_bytes"] / 2 ** 20)


def counted(info):
    """A retained report with both of its numbers, each a count."""
    if not isinstance(info, dict):
        return False
    return all(a_count(info.get(key)) for key in ("generations", "stored_bytes"))


def a_count(value):
    if isinstance(value, bool) or not isinstance(value, int):
        return False
    return value >= 0


class transport:
    """Failures of the Incus path inside become ContainerError: a refusal
    when the activator said why, else an unreachable container."""

    def __init__(self, name):
        self.name = name

    def __enter__(self):
        return self

    def __exit__(self, kind, error, _):
        if kind is None or not issubclass(kind, (subprocess.SubprocessError, OSError, ValueError)):
            return False
        text = (getattr(error, "stderr", None) or str(error)).strip()
        if isinstance(text, bytes):
            text = text.decode("utf-8", "replace")
        last = text.splitlines()[-1] if text else kind.__name__
        code = "refused" if any(mark in text for mark in REFUSAL_MARKS) else UNREACHABLE
        raise ContainerError(code, "{}: {}".format(self.name, last)) from error


class Resolver:
    """The plan for one update: versions, the override lock, and for each
    container its next manifest and delivery. It reads from the containers
    and the publishers and changes neither."""

    def __init__(self, instances, cache=CACHE, latest=versions.latest, checksum=versions.resolver):
        self.instances, self.cache, self.latest, self.checksum = instances, Path(cache), latest, checksum

    def __call__(self, requested, record):
        try:
            return self.plan(requested, record)
        except ContainerError as error:
            raise ValueError(str(error)) from error

    def plan(self, requested, record):
        current = {role: self.instances[role].manifest(record["containers"][role]["predecessor"]) for role in self.instances}
        clients = [name for name in LANE_TOOLS if any(name in current[role]["tools"] for role in current)]
        wanted = wanted_versions(requested, clients, self.latest)
        plans, override = {}, None
        for role, instance in self.instances.items():
            base_text = instance.share("tools.lock.json").encode()
            lock = override_lock(json.loads(base_text), wanted, self.checksum)
            manifest = next_manifest(current[role], base_text, lock)
            if "override" in manifest:
                override = self.keep(lock)
            plans[role] = {"candidate": generation_id(manifest), "deliver": Delivery(manifest, lock, self.cache)}
        return {"requested": wanted, "override": override, "containers": plans}

    def keep(self, lock):
        """The override lock in the host cache, by its digest."""
        payload = encoded(lock)
        path = self.cache / "overrides" / (digest(payload) + ".json")
        path.parent.mkdir(parents=True, exist_ok=True)
        if not path.exists():
            path.write_bytes(payload)
        return {"lock_sha256": digest(payload), "path": "overrides/{}.json".format(digest(payload))}


class Delivery:
    """Puts one container's manifest, its override lock and the client
    artifacts its store lacks into a directory of this update's own, and
    answers the stage arguments. Downloads are verified against the lock
    before anything is pushed."""

    def __init__(self, manifest, lock, cache):
        self.manifest, self.lock, self.cache = manifest, lock, cache

    def __call__(self, instance, update_id):
        incoming = "{}/incoming-{}".format(STORE, update_id)
        container = instance.container
        with transport(instance.name):
            container.execute("install", "-d", "-m", "0700", incoming)
            held = instance.complete()
            for name in clients_of(self.manifest):
                tool = self.manifest["tools"][name]
                if tool["sha256"] not in held:
                    print("{}: fetching {} {}".format(instance.name, name, tool["version"]), flush=True)
                    item = artifact(self.lock, name, self.manifest["architecture"])
                    container.push(cache_artifact(item, self.cache), "{}/{}".format(incoming, tool["file"]))
            with tempfile.TemporaryDirectory(prefix="pairtask-update-") as directory:
                files = {"manifest.json": (json.dumps(self.manifest, indent=2, sort_keys=True) + "\n").encode()}
                if "override" in self.manifest:
                    files[OVERRIDE_FILE] = encoded(self.lock)
                for name, data in files.items():
                    path = Path(directory) / name
                    path.write_bytes(data)
                    container.push(path, "{}/{}".format(incoming, name))
        return ["{}/manifest.json".format(incoming), "--incoming", incoming]
