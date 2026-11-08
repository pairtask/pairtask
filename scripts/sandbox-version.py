#!/usr/bin/env python3
"""pairtask version: the app release the containers run, the host tooling's
release, the latest published one, and the manual command that would update
them. It observes and reports; it never upgrades anything.

The result also goes to the broker as the chat's update notice, best-effort:
an older broker that does not know the notice is reported and skipped.
"""

import argparse
import json
import re
import sys

from sandbox_releases import repository
from sandbox_remote import default_remote
import sandbox_version


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def parser():
    p = argparse.ArgumentParser(prog="pairtask version", description=__doc__)
    p.add_argument("--json", action="store_true", help="print the report as JSON (schema 1)")
    p.add_argument("--refresh", action="store_true", help="ask GitHub for the latest release now")
    p.add_argument("--summary", action="store_true",
                   help="one line from cached release metadata; no network and no chat notice (pairtask status)")
    p.add_argument("--quiet", action="store_true", help="print nothing (the background refresh before a chat)")
    p.add_argument("--remote", default=default_remote(), type=identifier)
    p.add_argument("--project", default="pairtask", type=identifier)
    p.add_argument("--repo", default="pairtask/pairtask", type=repository)
    return p


def main(argv=None):
    args = parser().parse_args(argv)
    scope = sandbox_version.Scope(args.project, args.remote, args.repo)
    if args.summary:
        data = sandbox_version.best_effort(lambda: sandbox_version.report(scope, network=False))
        known = data is not None and sandbox_version.observed_deployment(data)
        print(sandbox_version.summary_line(data) if known else "version: unknown")
        return 0
    data = sandbox_version.report(scope, refresh=args.refresh)
    notified = sandbox_version.publish(data)
    if args.quiet:
        return 0
    if args.json:
        print(json.dumps(data, sort_keys=True))
        return 0
    for line in sandbox_version.lines(data):
        print(line)
    if not notified:
        print("chat:      update notice not delivered (the broker may predate it)")
    if data["scope"] == "custom":
        print("scope:     pass the same --project/--remote/--repo to pk version again; without them it reads the default deployment")
    return 0


if __name__ == "__main__":
    sys.exit(main())
