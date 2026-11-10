#!/usr/bin/env python3
"""Decide whether a CI run needs the workspace image jobs, and whether it
needs any code job at all.

Prints exactly seven lines for $GITHUB_OUTPUT:

    images=true|false
    reason=<token>
    docs_only=true|false
    go=true|false
    scripts=true|false
    pi=true|false
    infra=true|false

docs_only is true only for a pull request whose full diff touches nothing
but safe documentation (the images decision is then "safe-docs-only"). The
four flags say which test jobs a pull request needs, from the kinds of
paths it changes (see kind): Go changes run the Go suites and, since the
script suite reads Go test data, the script suite; script or image changes
run the script suite; Pi changes its tests; image recipe and infrastructure
changes the mirror check and the OpenTofu checks. Anything outside the known kinds, a change to the
workflow or to this classifier, every other event, --force-full and a diff
failure set every flag: the run does everything. The chat TUI exception
never makes a run docs-only.

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


# Top-level documents and the contribution policy, which no image ships.
POLICY_DOCS = ("README.md", "CLA.md", "CONTRIBUTING.md", ".github/PULL_REQUEST_TEMPLATE.md")


def safe_doc(path):
    """A policy document, or a Markdown page directly under docs/, that no image ships."""
    if shipped(path):
        return False
    if path in POLICY_DOCS:
        return True
    parts = path.split("/")
    return len(parts) == 2 and parts[0] == "docs" and parts[1].endswith(".md")


def docs_only(decision):
    """Whether the run may skip every code job: only the plain docs skip."""
    return "true" if decision == SKIP else "false"


# Kinds of changed paths, each a fixed token. A root ending in "/" is a
# directory, matched by prefix; any other root is one exact file. CI is the
# workflow, this classifier with its test, and the module that lists what
# an image is built from: a change there runs everything.
CI_PATHS = (".github/", "scripts/ci-classify.py", "scripts/tests/test_ci_classify_acceptance.py", "scripts/sandbox_source.py")
GO_PATHS = ("go.mod", "go.sum", "cmd/", "internal/", "testdata/")
INFRA_PATHS = ("infra/", "docs/skills/", "scripts/base-image-mirror.py")  # the mirror check is its own test
PI_PATHS = ("integrations/",)
FLAGS = ("go", "scripts", "pi", "infra")


def under(path, roots):
    return any(path.startswith(root) if root.endswith("/") else path == root for root in roots)


def kind(path):
    """The kind of one path: docs, ci, go, infra, pi, scripts, or other."""
    if under(path, CI_PATHS):
        return "ci"
    if safe_doc(path):
        return "docs"
    if under(path, GO_PATHS):
        return "go"
    if under(path, INFRA_PATHS):
        return "infra"
    if under(path, PI_PATHS):
        return "pi"
    if path.startswith("scripts/"):
        return "scripts"
    return "other"


def kinds(paths):
    return sorted({kind(path) for path in paths})


def flags(decision, found):
    """Which test jobs run, by the kinds found; everything unless the run is
    a pull request the diff of which was read and holds only known kinds."""
    everything = {flag: "true" for flag in FLAGS}
    if decision in (ALWAYS, FORCED, FAILED) or "ci" in found or "other" in found:
        return everything
    return {"go": token("go" in found), "scripts": token(bool({"go", "scripts", "infra"} & set(found))),
            "pi": token("pi" in found), "infra": token("infra" in found)}


def token(value):
    return "true" if value else "false"


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
    """The decision for one run and the kinds it changed: its event, refs,
    repository and force flag. The kinds are empty when the diff was not
    read."""
    if run.event != "pull_request":
        return ALWAYS, []
    if run.force_full:
        return FORCED, []
    try:
        paths = changed_paths(run.repo, run.base, run.head)
    except (subprocess.CalledProcessError, OSError, ValueError):
        return FAILED, []
    return skip_for(paths), kinds(paths)


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
    decision, found = decide(args)
    images, reason = decision
    lines = ["images=" + images, "reason=" + reason, "docs_only=" + docs_only(decision)]
    lines += [flag + "=" + value for flag, value in flags(decision, found).items()]
    sys.stdout.write("".join(line + "\n" for line in lines))
    return 0


if __name__ == "__main__":
    sys.exit(main())
