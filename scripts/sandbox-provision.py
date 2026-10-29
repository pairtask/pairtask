#!/usr/bin/env python3
"""Provision through disposable pinned OpenTofu; no host Terraform installation."""

import argparse
import json
from pathlib import Path
import re
import signal
import subprocess
import sys

from sandbox_operator_config import deployment_directory, remember
from sandbox_operator_workflow import run_operation
from sandbox_operator_recovery import recover
import sandbox_pairing
from sandbox_releases import release_tag, repository
from sandbox_remote import REMOTE, colima_profile, default_remote
from sandbox_rollout import rollout, tools_retry


# The rollouts, and the pairing route each pairing action sets (None keeps it).
ROLLOUTS = {"rollout": None, "pairing-on": True, "pairing-off": False}


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", value):
        raise argparse.ArgumentTypeError("Expected an Incus name without spaces or a colon")
    return value


def arguments():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["rollout", "plan", "apply", "recover", "tools-retry",
                                           "pairing-on", "pairing-off", "pairing-status"])
    parser.add_argument("--remote", default=default_remote())
    parser.add_argument("--image-dir", type=Path, help="Verified workspace image directory; remembered after first selection")
    parser.add_argument("--state-dir", type=Path, help="Select an existing operator checkout once; otherwise discover it")
    parser.add_argument("--project", default="pairtask", type=identifier)
    parser.add_argument("--storage-pool", default="default", type=identifier)
    parser.add_argument("--network", default="incusbr0", type=identifier)
    parser.add_argument("--release", type=release_tag, help="Rollout release tag; defaults to latest")
    parser.add_argument("--repo", type=repository, default="pairtask/pairtask")
    parser.add_argument("--ssh-state-dir", type=Path, default=Path(__file__).resolve().parents[1] / "infra/incus/ssh")
    parser.add_argument("--tools", nargs="*", default=["cs-mcp", "playwright", "docker", "pi"], metavar="TOOL",
                        help="Optional tools to enable (default: all); required tools are always installed")
    changes = parser.add_mutually_exclusive_group()
    changes.add_argument("--replace", action="store_true", help="Plan replacement of the dev container")
    changes.add_argument("--destroy", action="store_true", help="Explicitly plan deletion; volume guards must be removed first")
    args = parser.parse_args()
    if args.action != "plan" and any((args.replace, args.destroy)):
        parser.error("--replace and --destroy are plan options; apply uses the saved plan")
    if args.release:
        if args.action not in ROLLOUTS or args.image_dir:
            parser.error("--release is for rollout and cannot be combined with --image-dir")
    args.release = args.release or "latest"
    args.pairing = ROLLOUTS.get(args.action)
    return args


def main():
    args = arguments()
    if args.action == "pairing-status":
        return sandbox_pairing.status(args)
    if args.remote != "local" and colima_profile(args.remote) is None:
        raise ValueError(f"Use Linux local or macOS {REMOTE}")
    metadata = json.loads(subprocess.check_output(["incus", "query", args.remote + ":/1.0"]))
    directory = deployment_directory(args.state_dir, args.project)
    remember(directory, args.project)
    operation = {"recover": recover, "tools-retry": tools_retry, **dict.fromkeys(ROLLOUTS, rollout)}.get(args.action, run_operation)
    operation(args, directory, metadata["environment"])


def interrupted(*_):
    raise KeyboardInterrupt


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    try:
        sys.exit(main())
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        sys.exit(f"sandbox-provision: {error}")
    except KeyboardInterrupt:
        sys.exit("sandbox-provision: interrupted; inspect the reported state/operator before retrying")
