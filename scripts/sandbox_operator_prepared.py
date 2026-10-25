"""A prepared operator image, reused within one disposable CI run.

Every operator operation still runs in a fresh project and a fresh container.
Only the bootstrap that does not depend on the operation (the pinned base,
python3 and ca-certificates, the verified OpenTofu executable) is prepared once
per run and published as an image in a dedicated project. The image is made
from a separate scratch container before anything operation-specific exists:
no config, token, plan, state or proxy. It is validated after it stops, and
every container launched from it is validated again before it is used.

Opt-in only: PAIRTASK_OPERATOR_PREPARED names the run (for example
run-123-1-amd64). Without it, operators bootstrap exactly as before.
"""

import argparse
from dataclasses import dataclass
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import time
from typing import Mapping

SCHEMA = b"pairtask-operator-prepared/1"
SCRIPTS = Path(__file__).resolve().parent
ROOT = SCRIPTS.parent
# The code whose behaviour defines what a prepared image contains.
RECIPE_FILES = ("sandbox_operator_runtime.py", "sandbox_operator_download.py", "sandbox_operator_prepared.py")
TOKEN = re.compile(r"^[a-z0-9-]{1,40}$")
ENV = "PAIRTASK_OPERATOR_PREPARED"
REQUIRED_DIRECTORIES = ("", "bin", "config")


class Contaminated(Exception):
    """A prepared candidate or launched instance failed validation; never publish or use it."""


@dataclass(frozen=True)
class Snapshot:
    devices: Mapping[str, Mapping[str, str]]  # expanded devices: name -> properties, incl. "type"
    operator_files: frozenset                 # non-directory entries under /operator, relative
    binary_sha256: str                        # sha256 of /operator/bin/tofu, "" if missing
    machine_id: str                           # /etc/machine-id, stripped
    dbus_machine_id: str                      # /var/lib/dbus/machine-id, "" if absent
    ssh_host_keys: Mapping[str, str]          # /etc/ssh/ssh_host_* name -> sha256 of contents


def recipe_key(arch, base_fp, operator_lock, recipe):
    """A digest over the schema and every input, each length-framed so that
    no two different recipes can produce the same byte stream."""
    digest = hashlib.sha256()
    for part in (SCHEMA, arch.encode(), base_fp.encode(), operator_lock, recipe):
        digest.update(len(part).to_bytes(8, "big"))
        digest.update(part)
    return digest.hexdigest()


def recipe_bytes():
    return b"".join(len(data).to_bytes(8, "big") + data
                    for data in ((SCRIPTS / name).read_bytes() for name in RECIPE_FILES))


def operator_lock_bytes():
    return (ROOT / "infra/operator/tools.lock.json").read_bytes()


def expected_binary_sha256(arch):
    """The executable pin from this checkout's lock; never from a cached image."""
    return json.loads(operator_lock_bytes())["architectures"][arch]["binary_sha256"]


def check_devices(devices):
    if set(devices) != {"root", "eth0"}:
        raise Contaminated("devices must be exactly root and eth0, found " + ", ".join(sorted(devices)))
    root, nic = devices["root"], devices["eth0"]
    if root.get("type") != "disk" or root.get("path") != "/":
        raise Contaminated("root must be the disk mounted at /")
    if nic.get("type") != "nic":
        raise Contaminated("eth0 must be a nic")


def check_contents(snapshot, expected_binary):
    check_devices(snapshot.devices)
    if snapshot.operator_files != frozenset({"bin/tofu"}):
        extra = sorted(snapshot.operator_files - {"bin/tofu"})
        raise Contaminated("/operator must hold only bin/tofu" + (", found " + ", ".join(extra) if extra else ""))
    if not expected_binary or snapshot.binary_sha256 != expected_binary:
        raise Contaminated("the OpenTofu executable does not match its pin")


def verify_clean(snapshot, expected_binary):
    """A stopped candidate that may be published: verified contents, no identity."""
    check_contents(snapshot, expected_binary)
    if snapshot.machine_id or snapshot.dbus_machine_id:
        raise Contaminated("a prepared image must not carry a machine id")
    if snapshot.ssh_host_keys:
        raise Contaminated("a prepared image must not carry SSH host keys")


def verify_launch(snapshot, expected_binary):
    """A container launched from the prepared image, before it is connected."""
    check_contents(snapshot, expected_binary)
    if not snapshot.machine_id:
        raise Contaminated("a launched operator must have generated its own machine id")


def sha256_file(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def lstat_mode(path):
    """The entry's own mode, never a link target's; None if absent."""
    try:
        return os.lstat(path).st_mode
    except FileNotFoundError:
        return None


def regular_sha256(path):
    """sha256 of a regular file; "" for a link, anything else, or absence."""
    mode = lstat_mode(path)
    return sha256_file(path) if mode is not None and stat.S_ISREG(mode) else ""


def tree_entries(root):
    """Non-directory entries under root, relative, without following links;
    raises if a required directory is a link or not a directory."""
    for name in REQUIRED_DIRECTORIES:
        if lstat_mode(root / name) is None or not stat.S_ISDIR(lstat_mode(root / name)):
            raise Contaminated("/operator/" + name + " must be a real directory")
    entries = set()
    for directory, names, files in os.walk(root, followlinks=False):
        for name in names + files:
            path = Path(directory) / name
            if not stat.S_ISDIR(os.lstat(path).st_mode):
                entries.add(str(path.relative_to(root)))
    return frozenset(entries)


class Instance:
    """Read-only inspection through the file and config APIs, which work on
    running and stopped instances alike; nothing runs inside the instance.

    A path counts as absent only when its parent's structured listing
    confirms it. Any failed listing or read raises, so an inspection error can
    never pass for a clean instance."""

    def __init__(self, incus, remote, project, name):
        self.incus, self.remote, self.project, self.name = incus, remote, project, name

    def listing(self, directory):
        """Names in a directory, from the instance file API."""
        endpoint = f"{self.remote}:/1.0/instances/{self.name}/files?path={directory}&project={self.project}"
        result = self.incus(["incus", "query", endpoint], capture_output=True, text=True)
        if result.returncode != 0:
            raise Contaminated(f"cannot list {directory}: {result.stderr.strip()}")
        names = json.loads(result.stdout)
        if not isinstance(names, list):
            raise Contaminated(f"{directory} is not a directory")
        return set(names)

    def exists(self, path):
        parent, name = os.path.split(path.rstrip("/"))
        return name in self.listing(parent or "/")

    def pull(self, source, destination, recursive=False):
        command = ["incus", "--project", self.project, "file", "pull"]
        if recursive:
            command.append("-r")
        result = self.incus(command + [f"{self.remote}:{self.name}{source}", str(destination)],
                            capture_output=True, text=True)
        if result.returncode != 0:
            raise Contaminated(f"cannot read {source}: {result.stderr.strip()}")

    def devices(self):
        out = self.incus(["incus", "query", f"{self.remote}:/1.0/instances/{self.name}?project={self.project}"],
                         check=True, capture_output=True, text=True).stdout
        return json.loads(out)["expanded_devices"]

    def text(self, path, scratch, depth=0):
        """A small file's contents; "" only when confirmed absent."""
        if not self.exists(path):
            return ""
        target = scratch / hashlib.sha256(path.encode()).hexdigest()
        self.pull(path, target)
        if target.is_symlink():
            # dbus commonly links to /etc/machine-id; read the link's target.
            linked = os.readlink(target)
            if depth > 2 or not linked.startswith("/"):
                raise Contaminated(f"{path} is an unexpected link")
            return self.text(linked, scratch, depth + 1)
        return target.read_text().strip()

    def host_keys(self, scratch):
        if not self.exists("/etc/ssh"):
            return {}
        names = sorted(n for n in self.listing("/etc/ssh") if n.startswith("ssh_host_"))
        keys = {}
        for name in names:
            target = scratch / ("key-" + name)
            self.pull("/etc/ssh/" + name, target)
            keys[name] = regular_sha256(target) or "not-a-regular-file"
        return keys


def inspect(incus, remote, project, name):
    instance = Instance(incus, remote, project, name)
    with tempfile.TemporaryDirectory() as directory:
        scratch = Path(directory)
        if not instance.exists("/operator"):
            raise Contaminated("/operator is missing")
        instance.pull("/operator", scratch, recursive=True)
        operator = scratch / "operator"
        return Snapshot(devices=instance.devices(), operator_files=tree_entries(operator),
                        binary_sha256=regular_sha256(operator / "bin/tofu"),
                        machine_id=instance.text("/etc/machine-id", scratch),
                        dbus_machine_id=instance.text("/var/lib/dbus/machine-id", scratch),
                        ssh_host_keys=instance.host_keys(scratch))


SANITIZE = ("sh", "-c", "rm -f /etc/ssh/ssh_host_* /var/lib/dbus/machine-id && : > /etc/machine-id")


class PreparedStore:
    """The run's prepared images, in a project of their own."""

    def __init__(self, remote, token, incus=subprocess.run, log=print):
        if not TOKEN.fullmatch(token):
            raise ValueError("invalid prepared-operator token")
        self.remote, self.incus, self.log = remote, incus, log
        self.project = "pairtask-operator-prepared-" + token

    def alias(self, key):
        return "operator-" + key

    def run(self, *args, **kwargs):
        return self.incus(["incus", "--project", self.project, *args], check=True, **kwargs)

    def exists(self):
        listing = self.incus(["incus", "project", "list", self.remote + ":", "--format", "json"],
                             check=True, capture_output=True, text=True).stdout
        return any(item["name"] == self.project for item in json.loads(listing))

    def find(self, key):
        if not self.exists():
            return None
        images = json.loads(self.run("image", "list", self.remote + ":", "--format", "json",
                                     capture_output=True, text=True).stdout)
        for image in images:
            if any(a["name"] == self.alias(key) for a in image.get("aliases") or []):
                return image["fingerprint"]
        return None

    def ensure_project(self):
        if not self.exists():
            self.incus(["incus", "project", "create", f"{self.remote}:{self.project}",
                        "-c", "features.images=true", "-c", "features.profiles=true",
                        "-c", "features.networks=false"], check=True)

    def publish(self, scratch, key):
        """Publish a stopped, validated scratch container under the key."""
        self.run("publish", f"{self.remote}:{scratch}", f"{self.remote}:", "--alias", self.alias(key),
                 "--compression", "none")
        fingerprint = self.find(key)
        if fingerprint is None:
            raise RuntimeError("published image is missing")
        return fingerprint

    def discard(self, key):
        fingerprint = self.find(key)
        if fingerprint:
            self.run("image", "delete", f"{self.remote}:{fingerprint}")

    def copy_into(self, fingerprint, project):
        self.incus(["incus", "image", "copy", f"{self.remote}:{fingerprint}", f"{self.remote}:",
                    "--project", self.project, "--target-project", project], check=True)

    def cleanup(self):
        """Remove only this run's project; a missing project is a no-op."""
        if not self.exists():
            return
        for kind, key in (("list", "name"), ("image list", "fingerprint")):
            items = json.loads(self.run(*kind.split(), f"{self.remote}:", "--format", "json",
                                        capture_output=True, text=True).stdout)
            for item in items:
                if kind == "list":
                    self.run("delete", f"{self.remote}:{item[key]}", "--force")
                else:
                    self.run("image", "delete", f"{self.remote}:{item[key]}")
        self.incus(["incus", "project", "delete", f"{self.remote}:{self.project}"], check=True)


def store_from_env(remote, incus=subprocess.run, environ=os.environ):
    token = environ.get(ENV, "")
    if not token:
        return None
    if not TOKEN.fullmatch(token):
        print(f"prepared: disabled (invalid {ENV})", flush=True)
        return None
    return PreparedStore(remote, token, incus)


def timed(start):
    return f"{time.monotonic() - start:.1f}s"


def main(argv=None):
    parser = argparse.ArgumentParser(description="Clean up this run's prepared operator images.")
    parser.add_argument("command", choices=["cleanup"])
    parser.add_argument("--remote", default="local")
    parser.add_argument("--token", required=True)
    args = parser.parse_args(argv)
    PreparedStore(args.remote, args.token).cleanup()
    return 0


if __name__ == "__main__":
    sys.exit(main())
