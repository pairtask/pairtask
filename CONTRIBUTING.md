# Contributing to pairtask

Thanks for your interest in contributing to pairtask. The project is
licensed under MIT or Apache 2.0, at your choice.

Before a contribution can be merged, it must be covered by an accepted
[Contributor License Agreement (CLA)](CLA.md).

## Before your first pull request

Read the [agreement](CLA.md). To agree, post this comment on your pull
request, with its full head commit SHA filled in:

```
I have read CLA.md version 1 (2026-11-09) and agree to it for this pull request's head commit <SHA>.
```

A pull request is merged only with a consent record that covers it. Each
record holds:

- the agreement version and date;
- the repository, the pull request number, its full head commit SHA and the
  files it changes;
- your confirmation, posted the same way, for each later revision of that
  pull request;
- privately, who agreed and on what authority (an employer's permission
  where one could hold rights); publicly, the pull request shows only the
  agreement version and the covered commit.

A new agreement version, a different recipient or a wider scope needs a
fresh record; nothing commits your future work in advance.

## In every pull request

- Say what the change does and why; link the issue with `Refs #N` unless the
  change finishes it.
- Name anything in it you did not write yourself (third-party code, images,
  text, data) with its licence, and say when a tool generated part of it.
- Run the checks that apply to your change:

```sh
go test -race ./... -timeout=1800s      # Go changes
python3 -m unittest discover -s scripts/tests   # host script changes
```

The pull request template repeats these points as a checklist.

## How changes land

A maintainer reviews the pinned commit and sends findings back to the
author; the owner decides the merge. Merges happen on green CI with the
agreement posted. Issues: https://github.com/pairtask/pairtask/issues.
