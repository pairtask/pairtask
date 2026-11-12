#!/usr/bin/env python3
"""Build pairtask-desktop's Claude Desktop extension (.mcpb) for one platform."""

import argparse
from pathlib import Path
import sys

from desktop_extension import FORMATS, Target, build


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True, help="The pairtask-desktop executable for this platform")
    parser.add_argument("--goos", choices=sorted(FORMATS), required=True)
    parser.add_argument("--goarch", choices=["amd64", "arm64"], required=True)
    parser.add_argument("--version", required=True, help="The extension's semver version")
    parser.add_argument("--out", type=Path, required=True, help="The directory to write the bundle into")
    args = parser.parse_args()
    print(build(args.binary, Target(args.goos, args.goarch, args.version), args.out), flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError) as error:
        print(f"build-mcpb: {error}", file=sys.stderr)
        sys.exit(1)
