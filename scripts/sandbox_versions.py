"""Resolve exact development tool versions to verified publisher checksums."""

import argparse
import copy
import json
import os
from pathlib import Path
import re
import sys
import urllib.parse
import urllib.request

from sandbox_https import urlopen

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "infra/image"))
from tool_lock import artifact  # noqa: E402


def version(value):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", value):
        raise argparse.ArgumentTypeError("Use an exact version such as 0.156.1, without a v prefix")
    return value


def fingerprint(value):
    if not re.fullmatch(r"[0-9a-f]{64}", value):
        raise ValueError("Expected a full lowercase SHA256 fingerprint")
    return value


def publisher_json(url):
    headers = {"User-Agent": "collab-ai-image-builder"}
    # GitHub's API allows 60 unauthenticated calls an hour per address, which
    # shared CI runners exhaust; a token from the environment lifts that. It
    # goes to api.github.com only and is never printed.
    token = os.environ.get("GH_TOKEN")
    if token and urllib.parse.urlsplit(url).hostname == "api.github.com":
        headers["Authorization"] = "Bearer " + token
    request = urllib.request.Request(url, headers=headers)
    with urlopen(request, timeout=60) as response:
        return json.load(response)


def go_checksum(selected, arch):
    releases = publisher_json("https://go.dev/dl/?mode=json&include=all")
    name = f"go{selected}.linux-{arch}.tar.gz"
    for release in releases:
        for item in release["files"]:
            if item["filename"] == name:
                return fingerprint(item["sha256"])
    raise ValueError(f"Official Go archive unavailable: {name}")


def codex_checksum(selected, arch):
    release = publisher_json(f"https://api.github.com/repos/openai/codex/releases/tags/rust-v{selected}")
    name = f"codex-package-{arch}.tar.gz"
    for item in release["assets"]:
        if item["name"] == name:
            digest = item.get("digest") or ""
            if not digest.startswith("sha256:"):
                raise ValueError(f"No publisher SHA256 for {name}")
            return fingerprint(digest.removeprefix("sha256:"))
    raise ValueError(f"This Codex version has no supported native package: {name}")


def claude_checksum(selected, arch):
    manifest = publisher_json(f"https://downloads.claude.ai/claude-code-releases/{selected}/manifest.json")
    if manifest["version"] != selected:
        raise ValueError("Claude manifest version differs from the requested version")
    return fingerprint(manifest["platforms"][arch]["checksum"])


def resolver(name):
    """The publisher checksum lookup for a tool, or None when a new version
    needs its checksum pinned by hand. Looked up at call time."""
    return {"go": go_checksum, "codex": codex_checksum, "claude": claude_checksum}.get(name)


def resolve_lock(path, architecture, args):
    """Return the lock for one architecture, with version overrides resolved to
    publisher checksums. Overrides never leave another architecture paired with
    stale checksums, because only the built architecture is kept."""
    lock = json.loads(path.read_text())
    arch = copy.deepcopy(lock["architectures"][architecture])
    lock["architectures"] = {architecture: arch}
    for name, spec in lock["artifacts"].items():
        spec["architectures"] = {architecture: copy.deepcopy(spec["architectures"][architecture])}
        select_version(name, spec, architecture, getattr(args, f"{name}_version", None))
        artifact(lock, name, architecture)
    arch["base_image"] = fingerprint(getattr(args, "base_image", None) or arch["base_image"])
    return lock


def select_version(name, spec, architecture, requested):
    selected = version(requested or spec["version"])
    if selected == spec["version"]:
        return
    resolve = resolver(name)
    if resolve is None:
        raise ValueError(f"{name} has no publisher checksum lookup; pin its checksum in the lock file")
    per_arch = spec["architectures"][architecture]
    per_arch["sha256"] = resolve(selected, per_arch["arch"])
    spec["version"] = selected
