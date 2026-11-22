#!/usr/bin/env python3
"""Stream an operator's policy file to the secured Pairbook client."""
import argparse
from contextlib import nullcontext
from pathlib import Path
import subprocess
import sys


def main(argv=None):
    parser = argparse.ArgumentParser(prog="pairtask space admin policy-set")
    parser.add_argument("space")
    parser.add_argument("file")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--pairbook")
    args = parser.parse_args(argv)
    command = [sys.executable, str(Path(__file__).with_name("sandbox-session.py")), "secured",
               "pairtask", "operator", "space", "admin", "policy-set", args.space, "-"]
    if args.json:
        command.append("--json")
    if args.pairbook:
        command.extend(["--pairbook", args.pairbook])
    try:
        with (nullcontext(sys.stdin.buffer) if args.file == "-" else open(args.file, "rb")) as policy:
            return subprocess.run(command, stdin=policy).returncode
    except OSError as error:
        print(str(error), file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
