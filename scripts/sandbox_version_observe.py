"""What runs here, observed rather than remembered, for pairtask version:
each container's creation image (Incus volatile.base_image) and the build
source baked into it, and the release tag of the host tooling's checkout.
"""

import json
from pathlib import Path
import subprocess

from sandbox_version_lookup import DOCUMENT_MAX, FINGERPRINT_RE, SHA_RE, Proof, is_tag, now

INSTANCES = ("secured", "workspace")
ARCHITECTURES = {"x86_64": "amd64", "aarch64": "arm64"}
BUILD_SOURCE = "/usr/local/share/pairtask/build-source.json"
INCUS_TIMEOUT = 10
OBSERVE_BUDGET = 10  # every Incus and git read of one observation together


def host_checkout():
    """The checkout the launcher runs, not whatever PAIRTASK_HOME names."""
    return Path(__file__).resolve().parents[1]


def proof_of(instance):
    """The container's identity to prove against releases, when it was
    fully observed."""
    if not all(instance[key] for key in ("image", "architecture", "commit")):
        return None
    return Proof(instance["image"], instance["architecture"], instance["source"], instance["commit"])


def source_commit(source):
    collab = (source or {}).get("collab")
    commit = collab.get("commit") if isinstance(collab, dict) else None
    return commit if SHA_RE.match(str(commit)) else None


class Observer:
    """Incus and git reads under one deadline: however many there are, the
    whole observation ends by it, and a read it cuts short counts as failed."""

    def __init__(self, project, remote, budget=OBSERVE_BUDGET):
        self.project, self.remote, self.deadline = project, remote, now() + budget

    def run(self, argv):
        remaining = min(INCUS_TIMEOUT, self.deadline - now())
        if remaining <= 0:
            return None
        try:
            result = subprocess.run(argv, capture_output=True, text=True, timeout=remaining, check=False)
        except (OSError, subprocess.TimeoutExpired):
            return None
        return result.stdout if result.returncode == 0 else None

    def incus(self, *args):
        return self.run(["incus", "--project", self.project] + list(args))

    def git(self, checkout, *args):
        out = self.run(["git", "-C", str(checkout)] + list(args))
        return out.strip() if out is not None else None

    def app(self):
        """Each container's image and build source, as Incus reports them now."""
        try:
            items = json.loads(self.incus("list", self.remote + ":", "--format=json") or "[]")
        except ValueError:
            items = []
        by_name = {item.get("name"): item for item in items if isinstance(item, dict)}
        return {name: self.instance(by_name[name]) for name in INSTANCES if name in by_name}

    def instance(self, item):
        image = (item.get("config") or {}).get("volatile.base_image")
        observed = {"status": item.get("status"), "image": image if FINGERPRINT_RE.match(str(image)) else None,
                    "commit": None, "source": None, "architecture": ARCHITECTURES.get(item.get("architecture"))}
        if observed["status"] == "Running":
            observed["source"] = self.build_source(item["name"])
            observed["commit"] = source_commit(observed["source"])
        return observed

    def build_source(self, name):
        out = self.incus("exec", "%s:%s" % (self.remote, name), "-T", "--", "cat", BUILD_SOURCE)
        if out is None or len(out.encode()) > DOCUMENT_MAX:
            return None
        try:
            source = json.loads(out)
        except ValueError:
            return None
        return source if isinstance(source, dict) else None

    def host(self, checkout=None):
        """The launcher checkout's release: exactly one release tag on a HEAD
        that git confirms is clean. Any read that fails leaves it unknown."""
        checkout = checkout or host_checkout()
        commit = self.git(checkout, "rev-parse", "HEAD")
        tags = self.git(checkout, "tag", "--points-at", "HEAD")
        status = self.git(checkout, "status", "--porcelain", "--untracked-files=no")
        releases = [t for t in (tags or "").split() if is_tag(t)]
        commit = commit if SHA_RE.match(str(commit)) else None
        known = commit is not None and status == "" and len(releases) == 1
        return {"state": "known" if known else "unknown", "release": releases[0] if known else None,
                "commit": commit, "dirty": bool(status)}
