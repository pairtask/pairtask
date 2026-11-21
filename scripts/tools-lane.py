#!/usr/bin/env python3
"""Check a client-only tool lock change in minutes: publisher checksums for
every architecture, then the agent clients downloaded, verified and run for
this machine. See scripts/tools_lane.py. Exits 1 with one FAIL line per
finding, or 0 with one PASS line.
"""

import argparse
from pathlib import Path
import platform
import sys

import tools_lane


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--lock-file", type=Path, default=tools_lane.LOCK)
    parser.add_argument("--architecture", default=platform.machine(), help="a lock architecture key, such as aarch64 or x86_64")
    parser.add_argument("--workdir", type=Path, required=True,
                        help="where downloads, the unpacked clients and their private home go; not under /tmp, which Codex refuses for helpers")
    args = parser.parse_args(argv)
    try:
        findings = tools_lane.run(args.lock_file, args.architecture, args.workdir)
    except tools_lane.LaneError as error:
        print("FAIL: {}".format(error))
        return 1
    for finding in findings:
        print("FAIL: {}".format(finding))
    if findings:
        return 1
    print("PASS: tools lane for {}: publisher checksums match the lock, and {} download, verify and run as pinned.".format(
        args.architecture, ", ".join(tools_lane.CLIENTS)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
