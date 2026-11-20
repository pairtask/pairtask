"""Resolve one pinned artifact of tools.lock.json for a machine architecture.

Shared by the image build (inside the builder) and the host scripts, so both
derive a tool's URL, entry path and checksum from the same table.
"""

import copy
import re


def artifact(lock, name, machine):
    spec = lock["artifacts"][name]
    per_arch = spec["architectures"][machine]
    values = {"version": spec["version"], "arch": per_arch["arch"]}
    item = {"name": name, "kind": spec["kind"], "version": spec["version"], "arch": per_arch["arch"],
            "url": spec["url"].format(**values), "entry": spec["entry"].format(**values),
            "sha256": per_arch["sha256"]}
    if not re.fullmatch(r"[0-9a-f]{64}", item["sha256"]):
        raise ValueError(f"Artifact {name} has no full lowercase SHA256 for {machine}")
    if not item["url"].startswith("https://"):
        raise ValueError(f"Artifact {name} must be downloaded over HTTPS")
    return item


# The agent clients: the only tools a lock override, or a fast-lane lock
# change, may differ in.
LANE_TOOLS = ("claude", "codex", "cs-mcp")


def clients_only(before, after):
    """The two locks are equal once each lane tool's version and
    per-architecture checksums are set aside; a lane tool missing or
    malformed on either side is not a client-only change."""
    before, after = copy.deepcopy(before), copy.deepcopy(after)
    for lock in (before, after):
        for name in LANE_TOOLS:
            if not blank_client(lock, name):
                return False
    return before == after


def blank_client(lock, name):
    """Remove the version and checksums of one lane tool in place; false
    when the entry isn't shaped as the lock schema has it."""
    spec = lock.get("artifacts", {}).get(name) if isinstance(lock, dict) else None
    architectures = spec.get("architectures") if isinstance(spec, dict) else None
    if not isinstance(architectures, dict) or not all(isinstance(a, dict) for a in architectures.values()):
        return False
    spec.pop("version", None)
    for per_arch in architectures.values():
        per_arch.pop("sha256", None)
    return True
