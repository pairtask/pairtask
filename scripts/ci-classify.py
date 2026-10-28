#!/usr/bin/env python3
"""Decide whether a CI run needs the workspace image jobs.

Prints exactly two lines for $GITHUB_OUTPUT:

    images=true|false
    reason=<token>

The reason is one of a closed set of tokens, never a file name, so nothing
from the diff reaches the workflow's shell. A pull request may skip images
only when its full diff touches nothing but unshipped top-level
documentation, or that and the operator chat TUI (internal/chat/): the
installed-image proofs never run the TUI, it reaches the broker only through
internal/protocol, which still builds, and the host Go suites test it on
both architectures. This is a TUI-only exception, not "presentation".
Every other event, any unknown or shipped path, --force-full (the pull
request's ci:full-images label), and any failure to read the diff build
images. A usage error exits non-zero and prints no decision.
"""

import argparse
import subprocess
import sys

import sandbox_source

BUILD = ("true", "shipped-or-unknown-path")
SKIP = ("false", "safe-docs-only")
SKIP_CHAT = ("false", "safe-chat-only")
ALWAYS = ("true", "event-always-builds")
FORCED = ("true", "forced-full")
FAILED = ("true", "diff-failed")

# The operator chat TUI; see the module docstring for why it may skip.
CHAT = "internal/chat/"

# Every path an image is built from: a change to any of them builds.
SHIPPED = tuple(sandbox_source.APP_PATHS) + tuple(sandbox_source.RECIPE_PATHS)


def shipped(path):
    return any(path == root or path.startswith(root + "/") for root in SHIPPED)


def safe_doc(path):
    """README.md, or a Markdown page directly under docs/, that no image ships."""
    if shipped(path):
        return False
    if path == "README.md":
        return True
    parts = path.split("/")
    return len(parts) == 2 and parts[0] == "docs" and parts[1].endswith(".md")


def chat(path):
    return path.startswith(CHAT) and path != CHAT


def git(repo, *args):
    return subprocess.run(["git", "-C", repo, *args], check=True, capture_output=True).stdout


def commit(repo, ref):
    """Resolve a ref to a commit; an option-like or missing ref raises."""
    if not ref:
        raise ValueError("empty ref")
    out = git(repo, "rev-parse", "--verify", "--quiet", "--end-of-options", ref + "^{commit}")
    return out.decode().strip()


def changed_paths(repo, base, head):
    """Both sides of every difference between the base and head trees.

    A direct tree diff, not a merge-base one: it can only report more paths
    (changes on the base branch too), never hide a change in head, so its
    error is building more often. --no-renames reports a rename as a deletion
    plus an addition, so the old and the new path are both checked; -z keeps
    whitespace inside names.
    """
    base_sha, head_sha = commit(repo, base), commit(repo, head)
    out = git(repo, "diff", "--name-only", "-z", "--no-renames", base_sha, head_sha)
    return [name.decode("utf-8", "surrogateescape") for name in out.split(b"\0") if name]


def decide(run):
    """The decision for one run: its event, refs, repository and force flag."""
    if run.event != "pull_request":
        return ALWAYS
    if run.force_full:
        return FORCED
    try:
        paths = changed_paths(run.repo, run.base, run.head)
    except (subprocess.CalledProcessError, OSError, ValueError):
        return FAILED
    return skip_for(paths)


def skip_for(paths):
    """Docs alone skip as docs; docs and chat, with some chat, skip as chat."""
    if all(safe_doc(path) for path in paths):
        return SKIP
    if all(safe_doc(path) or chat(path) for path in paths):
        return SKIP_CHAT
    return BUILD


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--repo", default=".")
    parser.add_argument("--event", default="")
    parser.add_argument("--base", default="")
    parser.add_argument("--head", default="")
    parser.add_argument("--force-full", action="store_true", help="build images whatever the diff (the ci:full-images label)")
    args = parser.parse_args(argv)
    images, reason = decide(args)
    sys.stdout.write("images=%s\nreason=%s\n" % (images, reason))
    return 0


if __name__ == "__main__":
    sys.exit(main())
