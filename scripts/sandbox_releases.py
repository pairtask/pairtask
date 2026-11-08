"""Resolve public workspace releases once, then download from the selected tag."""

import json
import os
import re
from urllib.error import HTTPError
from urllib.parse import quote
from urllib.request import Request

from sandbox_artifacts import BundleSource, fetch
from sandbox_https import urlopen
from sandbox_image_cache import prepare_image, record_bundles


def release_tag(value):
    if value != "latest" and not re.fullmatch(r"workspace-v[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?", value):
        raise ValueError("Use latest or a published workspace-vX.Y.Z tag")
    return value


def repository(value):
    if not re.fullmatch(r"[a-zA-Z0-9_.-]+/[a-zA-Z0-9_.-]+", value):
        raise ValueError("Use OWNER/REPOSITORY")
    return value


def resolve_release(repo, selection):
    repository(repo)
    release_tag(selection)
    endpoint = "latest" if selection == "latest" else "tags/" + quote(selection, safe="")
    request = Request(f"https://api.github.com/repos/{repo}/releases/{endpoint}", headers=api_headers())
    try:
        with urlopen(request, timeout=30) as response:
            release = json.load(response)
    except HTTPError as error:
        raise ValueError(release_error(error, repo, selection)) from error
    return published_tag(release, selection)


def api_headers():
    """Unauthenticated GitHub API calls share 60 requests per hour per address;
    GITHUB_TOKEN or GH_TOKEN lifts that limit."""
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "collab-ai-sandbox"}
    token = os.environ.get("GITHUB_TOKEN") or os.environ.get("GH_TOKEN")
    if token:
        headers["Authorization"] = "Bearer " + token
    return headers


def release_error(error, repo, selection):
    if error.code in (403, 429) and error.headers.get("X-RateLimit-Remaining") == "0":
        return (f"GitHub API rate limit reached while looking up release {selection} in {repo}. "
                "Retry later, or set GITHUB_TOKEN (for example from gh auth token) to use your own limit.")
    if 500 <= error.code < 600:
        return (f"GitHub release lookup failed with a server error (HTTP {error.code}) "
                f"for {selection} in {repo}. Retry shortly.")
    return (f"Cannot find published release {selection} in {repo} (HTTP {error.code}). "
            "A successful main release build must finish before rollout.")


def published_tag(release, selection):
    tag = release_tag(release["tag_name"])
    if tag == "latest" or release.get("draft"):
        raise ValueError("Expected a published, versioned workspace release")
    if selection != "latest" and tag != selection:
        raise ValueError("Expected a published, versioned workspace release")
    return tag


def download_release(architecture, selection="latest", repo="pairtask/pairtask", output=None):
    tag = resolve_release(repo, selection)
    print(f"Downloading {tag} for {architecture}…", flush=True)
    base = f"https://github.com/{repo}/releases/download/{quote(tag, safe='')}/"
    obtain = lambda name, target: fetch(base + name, target)
    directory, manifest = prepare_image(architecture, obtain, output)
    print(f"Verified image: {manifest['image_fingerprint']}", flush=True)
    # Freeze the selection; a concurrent download may move the convenience link.
    selected = directory.parent / manifest["image_fingerprint"] if output is None else directory
    record_bundles(selected, architecture, obtain, BundleSource(base, repo))
    return selected
