"""Published releases for pairtask version: the latest one, and which one,
if any, provably built a given container image.

A release counts as an image's only when its manifest for that architecture
names the exact image and the same full build provenance, and no other
release among the few at that commit does too; a commit match alone proves
nothing, because two attempts at one commit build different images. Only
such proven mappings and the latest tag are cached, never what is installed.
"""

from collections import namedtuple
import hashlib
import json
import math
import os
from pathlib import Path
import re
import time
from urllib.error import HTTPError, URLError
from urllib.parse import urlsplit
from urllib.request import Request

from sandbox_https import urlopen
from sandbox_releases import api_headers, repository

TAG_RE = re.compile(r"^workspace-v0\.0\.0-build\.([1-9][0-9]{0,8})\.([1-9][0-9]{0,3})$")
SHA_RE = re.compile(r"^[0-9a-f]{40}$")
FINGERPRINT_RE = re.compile(r"^[0-9a-f]{64}$")
PROVENANCE_KEYS = ("collab", "recipe_commit", "files_sha256")  # sandbox_tools_install's rollout check
DOCUMENT_MAX = 256 * 1024  # a build source or release manifest, with its file digests
# One page of releases with their asset lists: about 13 KB a release on
# GitHub today (626 KB for 50), so twice what even 100 would take.
PAGE_MAX = 2 * 1024 * 1024
CACHE_MAX = 64 * 1024
MAPPINGS_MAX = 32
CANDIDATES_MAX = 3
RELEASES_PAGE = 50
LATEST_TTL = 6 * 3600
HTTP_TIMEOUT = 5
LOOKUP_BUDGET = 15
DEFAULT_API = "https://api.github.com"

# What identifies one container's build: its creation image, architecture,
# baked build source and that source's commit.
Proof = namedtuple("Proof", "image arch source commit")


class LookupFailed(Exception):
    """A release lookup that could not finish; its text is shown, bounded."""


def now():
    return time.time()


def stamp(seconds):
    """UTC RFC 3339 with milliseconds, so the broker can order two reports
    made in the same second."""
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(seconds)) + ".%03dZ" % int(seconds % 1 * 1000)


def parse_build(tag):
    """(N, M) of a numbered build tag, or None for any other tag."""
    match = TAG_RE.match(tag or "")
    return (int(match.group(1)), int(match.group(2))) if match else None


def is_tag(value):
    return TAG_RE.match(str(value)) is not None


def provenance_of(manifest):
    return {key: manifest[key] for key in PROVENANCE_KEYS if key in manifest}


def digest(source):
    """The full build provenance, canonically hashed: the cache keeps the
    proof of equality, not the file digests themselves."""
    return hashlib.sha256(json.dumps(source, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def builds(manifest, proof):
    """The manifest describes exactly this image built from this source."""
    if not isinstance(manifest, dict):
        return False
    return manifest.get("image_fingerprint") == proof.image and provenance_of(manifest) == proof.source


def manifest_proof(manifest, arch):
    """The proof a release manifest describes, or None when it names no
    image or commit."""
    if not isinstance(manifest, dict) or not FINGERPRINT_RE.match(str(manifest.get("image_fingerprint"))):
        return None
    source = provenance_of(manifest)
    collab = source.get("collab")
    commit = collab.get("commit") if isinstance(collab, dict) else None
    if not SHA_RE.match(str(commit)):
        return None
    return Proof(manifest["image_fingerprint"], arch, source, commit)


# --- GitHub ------------------------------------------------------------------

def loopback(parts):
    return parts.scheme == "http" and parts.hostname in ("127.0.0.1", "::1")


def api_base():
    """The GitHub API, or a loopback stand-in for tests; nothing else."""
    base = os.environ.get("PAIRTASK_RELEASE_API", DEFAULT_API).rstrip("/")
    parts = urlsplit(base)
    if parts.scheme == "https" or loopback(parts):
        return base
    raise ValueError("PAIRTASK_RELEASE_API must be https, or http on loopback")


def published(release):
    return isinstance(release, dict) and is_tag(release.get("tag_name")) and not release.get("draft")


class Lookup:
    """GitHub reads under one deadline, each bounded in time and size. The
    release page and manifests are read at most once per lookup."""

    def __init__(self, repo, budget=LOOKUP_BUDGET):
        self.repo, self.base, self.deadline = repository(repo), api_base(), now() + budget
        self.page, self.manifests = None, {}

    def get(self, path, accept=None, limit=DOCUMENT_MAX):
        remaining = self.deadline - now()
        if remaining <= 0:
            raise LookupFailed("the release check ran out of time")
        headers = api_headers()
        if accept:
            headers["Accept"] = accept
        body = self.fetch(Request(self.base + "/repos/" + self.repo + path, headers=headers), remaining, limit)
        try:
            return json.loads(body)
        except ValueError as error:
            raise LookupFailed("GitHub sent malformed JSON") from error

    @staticmethod
    def fetch(request, remaining, limit):
        try:
            with urlopen(request, timeout=min(HTTP_TIMEOUT, remaining)) as response:
                body = response.read(limit + 1)
        except HTTPError as error:
            raise LookupFailed("GitHub answered HTTP %d" % error.code) from error
        except (URLError, OSError, ValueError) as error:
            raise LookupFailed("GitHub could not be reached") from error
        if len(body) > limit:
            raise LookupFailed("a release document exceeds %d bytes" % limit)
        return body

    def latest(self):
        release = self.get("/releases/latest")
        if not published(release):
            raise LookupFailed("the latest release is not a published numbered build")
        return release

    def releases_at(self, commit):
        """Every published build release targeting commit, from one page."""
        if self.page is None:
            page = self.get("/releases?per_page=%d" % RELEASES_PAGE, limit=PAGE_MAX)
            self.page = page if isinstance(page, list) else []
        return [r for r in self.page if published(r) and r.get("target_commitish") == commit]

    def manifest(self, release, arch):
        name = "collab-ai-workspace-linux-%s.json" % arch
        ids = [a["id"] for a in release.get("assets") or [] if a.get("name") == name and isinstance(a.get("id"), int)]
        if not ids:
            return None
        if ids[0] not in self.manifests:
            self.manifests[ids[0]] = self.get("/releases/assets/%d" % ids[0], accept="application/octet-stream")
        return self.manifests[ids[0]]

    def unique_release(self, proof):
        """The one release, among at most CANDIDATES_MAX at the proof's
        commit, built as its image from its source; None when there are more
        candidates or more than one match."""
        candidates = self.releases_at(proof.commit)
        if len(candidates) > CANDIDATES_MAX:
            return None
        matches = [r["tag_name"] for r in candidates if builds(self.manifest(r, proof.arch), proof)]
        return matches[0] if len(matches) == 1 else None


# --- cache: release metadata only --------------------------------------------

def cache_path():
    state = os.environ.get("XDG_STATE_HOME") or str(Path.home() / ".local" / "state")
    return Path(state) / "pairtask" / "version-cache.json"


def read_cache():
    try:
        data = cache_path().read_bytes()
        whole = json.loads(data) if len(data) <= CACHE_MAX else {}
    except (OSError, ValueError):
        whole = {}
    return whole if isinstance(whole, dict) else {}


def load_cache(repo):
    """This repository's entry, keeping only well-formed parts: anything else
    in the file, however it got there, is dropped as if never cached."""
    entry = read_cache().get(repo)
    entry = entry if isinstance(entry, dict) else {}
    cache = {"releases": {image: m for image, m in mappings(entry).items() if valid_mapping(image, m)}}
    if valid_latest(entry.get("latest")):
        cache["latest"] = entry["latest"]
    return cache


def mappings(entry):
    found = entry.get("releases")
    return found if isinstance(found, dict) else {}


def valid_latest(latest):
    if not isinstance(latest, dict) or not is_tag(latest.get("tag")):
        return False
    checked = latest.get("checked_at")
    return isinstance(checked, (int, float)) and not isinstance(checked, bool) and math.isfinite(checked)


def valid_mapping(image, mapping):
    if not FINGERPRINT_RE.match(str(image)) or not isinstance(mapping, dict):
        return False
    return is_tag(mapping.get("tag")) and isinstance(mapping.get("arch"), str) and \
        FINGERPRINT_RE.match(str(mapping.get("provenance"))) is not None


def save_cache(repo, entry):
    """Keep at most MAPPINGS_MAX mappings, newest last; if every repository's
    entry together is too big, keep this one's alone."""
    entry["releases"] = dict(list((entry.get("releases") or {}).items())[-MAPPINGS_MAX:])
    whole = read_cache()
    whole[repo] = entry
    data = json.dumps(whole, sort_keys=True).encode()
    if len(data) > CACHE_MAX:
        data = json.dumps({repo: entry}, sort_keys=True).encode()
    path = cache_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        temporary = path.with_suffix(".tmp")
        with os.fdopen(os.open(str(temporary), os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600), "wb") as handle:
            handle.write(data)
        os.replace(str(temporary), str(path))
    except OSError:
        pass


def remember(cache, proof, tag):
    mappings = cache.setdefault("releases", {})
    mappings.pop(proof.image, None)  # newest last, so trimming drops the oldest
    mappings[proof.image] = {"tag": tag, "arch": proof.arch, "provenance": digest(proof.source)}


def cached_tag(cache, proof):
    known = (cache.get("releases") or {}).get(proof.image)
    if not isinstance(known, dict) or known.get("arch") != proof.arch:
        return None
    return known.get("tag") if known.get("provenance") == digest(proof.source) else None


# --- the two questions ---------------------------------------------------------

def installed_release(lookup, cache, proof):
    """The release proven to have built this container, or None. A proven
    mapping is reused from the cache; otherwise only a lookup may prove one."""
    tag = cached_tag(cache, proof)
    if tag or lookup is None:
        return tag
    try:
        tag = lookup.unique_release(proof)
    except LookupFailed:
        return None
    if tag:
        remember(cache, proof, tag)
    return tag


def latest_release(lookup, cache, refresh, architectures=()):
    """The latest published release and how fresh that knowledge is. Asking
    GitHub also proves which image it ships for each architecture here, so a
    rollout to it is recognised later without asking again."""
    cached, age = cached_latest(cache)
    if asks(lookup, refresh, age):
        return ask_latest(lookup, cache, architectures)
    if not cached.get("tag"):
        return available("check_failed", {}, None, "not checked yet")
    return available("cached" if age <= LATEST_TTL else "stale", cached, age, None)


def cached_latest(cache):
    """The cached latest tag and its age in seconds, or None when unknown."""
    cached = cache.get("latest") if isinstance(cache.get("latest"), dict) else {}
    checked = cached.get("checked_at")
    return cached, (now() - checked if isinstance(checked, (int, float)) else None)


def due(age):
    return age is None or age > LATEST_TTL


def asks(lookup, refresh, age):
    """Ask GitHub: when it may, and when asked to or the answer is due."""
    if lookup is None:
        return False
    return refresh or due(age)


def ask_latest(lookup, cache, architectures):
    try:
        release = lookup.latest()
    except LookupFailed as error:
        cached, age = cached_latest(cache)
        return available("stale" if cached.get("tag") else "check_failed", cached, age, str(error)[:200])
    cache["latest"] = {"tag": release["tag_name"], "checked_at": now()}
    for arch in sorted(set(architectures)):
        prove_latest(lookup, cache, release, arch)
    return available("fresh", cache["latest"], 0, None)


def prove_latest(lookup, cache, release, arch):
    """Remember the latest release's image for arch, but only when the same
    uniqueness proof as for an installed container names it."""
    try:
        proof = manifest_proof(lookup.manifest(release, arch), arch)
        proven = lookup.unique_release(proof) if proof else None
    except LookupFailed:
        return
    if proven == release["tag_name"]:
        remember(cache, proof, proven)


def available(state, entry, age, error):
    checked = entry.get("checked_at")
    return {"state": state, "release": entry.get("tag") if is_tag(entry.get("tag")) else None,
            "checked_at": stamp(checked) if isinstance(checked, (int, float)) else None,
            "age_seconds": int(age) if age is not None else None, "error": error}
