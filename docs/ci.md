# CI checks and image builds

The [CI workflow](../.github/workflows/ci.yml) runs Go, Python, OpenTofu,
macOS sharing, CodeQL, and CodeScene checks on pull requests. Most changes
also build and test the workspace image on AMD64 and ARM64. Those image jobs
prove provisioning, offline SSH, control boundaries, persistence, and upgrade
compatibility; they take much longer than the other checks.

## When a pull request skips image builds

The `classify` job compares the pull request's base and head trees with
[`scripts/ci-classify.py`](../scripts/ci-classify.py). It skips image work
only when **every changed path** is `README.md` or a Markdown file directly
under `docs/` that the image does not ship. For example, a change to
`docs/ci.md` qualifies; one to `docs/skills/collab-ai/references/operator.md`
does not. A change to code, tests, scripts, the workflow, or any unknown path
also builds both images.

The decision is conservative. Renames check both old and new paths. A failed
diff lookup selects image builds. Pushes, tags, and manual runs always build,
so a pull request's docs-only skip never weakens the main-branch image proof.
Publishing requires an explicit build decision.

For a qualifying pull request, both architecture check names still appear.
Each job prints `No image input changed (safe-docs-only)` and skips its image
steps. The other checks still run. A skipped image job therefore means the
classifier found only unshipped documentation in that PR; it does not claim
the image was built.

## Measuring the result

Use the PR timeline's push or force-push time as the start, and the last
required check's completion time as green. The workflow run's creation and
completion times provide a reproducible CI-only interval. Job durations below
are each job's `startedAt` to `completedAt`, so parallel jobs must not be
added together. GitHub queue and runner variation can affect any one run.

Before the classifier, the [#144 docs PR run](https://github.com/pairtask/pairtask/actions/runs/40188708805)
at `e813ec4` rebuilt both images even though its changed files were docs and
a chat test. It is a **full-build reference**, not a docs-only comparison:
the test file under `internal/` is shipped to image builds. The force-push was
recorded at 2026-10-25 11:24:04 UTC; the run completed at 11:39:58 UTC.

| Reference | Push to green | CI run | Checks | mac-sharing | AMD64 images | ARM64 images |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| #144, full build before #145 | 15m 54s | 15m 51s | 1m 38s | 1m 05s | 13m 52s | 13m 44s |

A docs-only PR created after [#145](https://github.com/pairtask/pairtask/pull/145)
will provide the measured fast-path row and the explicit per-architecture skip
evidence before this guide is finalized. The goal in [#135](https://github.com/pairtask/pairtask/issues/135)
is under five minutes to a green docs-only PR; code-PR speed still needs a
separate improvement.
