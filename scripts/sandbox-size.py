#!/usr/bin/env python3
"""pairtask size: how big the sandbox is and how full. `show` reports the
macOS VM, the workspace container and each volume, with a warning for any
volume at 90% or more. It reads only; nothing is created or changed."""

import argparse
import platform
import re
import sys
from pathlib import Path

from sandbox_remote import colima_profile, default_remote
import sandbox_size


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def parser():
    p = argparse.ArgumentParser(prog="pairtask size", description=__doc__)
    sub = p.add_subparsers(dest="action", required=True)
    show = sub.add_parser("show", help="sizes and how full each volume is")
    show.add_argument("--remote", default=default_remote(), type=identifier)
    show.add_argument("--project", default="pairtask", type=identifier)
    show.add_argument("--json", action="store_true", help="print the report as JSON")
    return p


def profile_dir(remote):
    """The Colima profile directory behind remote; never created here."""
    return Path.home() / ".colima" / (colima_profile(remote) or "pairtask")


def main(argv=None, query=None, system=None):
    args = parser().parse_args(argv)
    query = query or sandbox_size.incus_query(args.remote)
    try:
        report = sandbox_size.collect(query, profile_dir(args.remote), system or platform.system(), args.project)
    except sandbox_size.Unreachable as error:
        print(f"pairtask size: Incus on {args.remote} didn't answer: {error}. Is the sandbox running? pairtask start", file=sys.stderr)
        return 1
    print(sandbox_size.as_json(report) if args.json else sandbox_size.render(report))
    return 0


if __name__ == "__main__":
    sys.exit(main())
