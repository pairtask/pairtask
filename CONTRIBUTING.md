# Contributing to pairtask

Thanks for the interest. pairtask is published under MIT or Apache 2.0, at
your choice. Contributions are accepted under the
[Contributor License Agreement](CLA.md) with Siamion Makarski: you keep the
copyright in your work and grant him directly, in explicit terms, the
rights to use, modify, distribute, host, sublicense and relicense it, and
you confirm that you may grant them. The public licences stay as they are
and keep every right they already give to everyone; the agreement makes the
grants and their scope explicit for the covered contributions.

**The agreement is a draft until a lawyer has reviewed it.** Until then,
outside pull requests are welcome for discussion and review but are not
merged.

## Before your first pull request

Read the [agreement](CLA.md). The way to agree will be stated here once a
lawyer has approved it. A pull request is merged only with a consent record
that covers it. Each record holds:

- the agreement version and date;
- the repository, the pull request number, its full head commit SHA and the
  files it changes;
- your confirmation, for each later revision of that pull request, that it
  has the same provenance;
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
agreement recorded. Issues: https://github.com/pairtask/pairtask/issues.
