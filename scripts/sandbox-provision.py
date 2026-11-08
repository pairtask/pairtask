#!/usr/bin/env python3
"""Provision through disposable pinned OpenTofu; no host Terraform installation."""

import argparse
from pathlib import Path
import re
import signal
import subprocess
import sys

from sandbox_operator_config import deployment_directory, remember
from sandbox_operator_workflow import run_operation, saved_deletion
from sandbox_operator_recovery import recover
import sandbox_discovery_service
import sandbox_version
import sandbox_pairing
from sandbox_releases import release_tag, repository
from sandbox_incus_version import IncusVersionError, check as check_server_version, query_server
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
    parser.add_argument("--host-port", type=int, help="pairing-on: publish the relay on this port of every host address (0: none; default: the saved port, else 7445)")
    parser.add_argument("--name", help="pairing-on: the name this workspace pairs under, at most 64 bytes; \"\" clears it; default: the saved name")
    discovery = parser.add_mutually_exclusive_group()
    discovery.add_argument("--no-discovery", dest="discovery", action="store_false", default=None,
                           help="pairing-on: don't run the known-peer discovery helper; remembered for later upgrades")
    discovery.add_argument("--discovery", dest="discovery", action="store_true", default=None,
                           help="pairing-on: run the discovery helper again after --no-discovery")
    changes = parser.add_mutually_exclusive_group()
    changes.add_argument("--replace", action="store_true", help="Plan replacement of the dev container")
    changes.add_argument("--destroy", action="store_true", help="Explicitly plan deletion; volume guards must be removed first")
    args = parser.parse_args()
    check_combinations(parser, args)
    args.release = args.release or "latest"
    args.pairing = ROLLOUTS.get(args.action)
    return args


def release_allowed(args):
    return args.action in ROLLOUTS and not args.image_dir


def pairing_on_flags(args):
    """The pairing-on flags given to another action."""
    if args.action == "pairing-on":
        return []
    given = {"--host-port": args.host_port, "--name": args.name}
    flags = [flag for flag, value in given.items() if value is not None]
    if args.discovery is not None:
        flags.append("--discovery" if args.discovery else "--no-discovery")
    return flags


def check_combinations(parser, args):
    """Flags that only some actions take."""
    if args.action != "plan" and any((args.replace, args.destroy)):
        parser.error("--replace and --destroy are plan options; apply uses the saved plan")
    if args.release and not release_allowed(args):
        parser.error("--release is for rollout and cannot be combined with --image-dir")
    for flag in pairing_on_flags(args):
        parser.error(f"{flag} is for pairing-on")


def main():
    args = arguments()
    if args.action == "pairing-status":
        return sandbox_pairing.status(args)
    if args.remote != "local" and colima_profile(args.remote) is None:
        raise ValueError(f"Use Linux local or macOS {REMOTE}")
    metadata = query_server(args.remote)
    directory = deployment_directory(args.state_dir, args.project)
    check_version(args, metadata, directory)
    remember(directory, args.project)
    operation = {"recover": recover, "tools-retry": tools_retry, **dict.fromkeys(ROLLOUTS, rollout)}.get(args.action, run_operation)
    operation(args, directory, metadata["environment"])
    if args.action in ROLLOUTS and hasattr(args, "discovery"):
        sync_discovery(args, directory)
        report_versions(args)


def report_versions(args):
    """After a rollout from this command line: what the containers run now,
    observed rather than assumed, told to the chat and printed."""
    scope = sandbox_version.Scope(args.project, args.remote, args.repo)
    data = sandbox_version.best_effort(lambda: sandbox_version.report(scope, refresh=True))
    if data is None:
        print("version: unknown (the release check failed; pk version retries)")
        return
    sandbox_version.publish(data)
    print(sandbox_version.summary_line(data))


def sync_discovery(args, directory):
    """After a rollout from this command line, the host's discovery helper
    follows the route: installed and (re)started while pairing is on and not
    opted out, removed otherwise. Callers that build their own arguments
    (proofs, tests) manage no service."""
    if args.action == "pairing-on" and args.discovery is not None:
        sandbox_discovery_service.remember(directory, args.discovery)
    wanted = (args.action != "pairing-off" and sandbox_pairing.configured(directory)
              and sandbox_discovery_service.remembered(directory))
    sandbox_discovery_service.sync(wanted, args.project, args.remote)


# VERSIONED are the actions that create or change containers, which need a
# supported server (#312); plan and recover report, and applying a saved
# deletion-only --destroy plan goes on.
VERSIONED = {"apply", "rollout", "pairing-on", "pairing-off", "tools-retry"}


def check_version(args, metadata, directory):
    """Refuse a container-changing action on an unsupported server; a plan
    only says what apply would refuse."""
    try:
        check_server_version(metadata, args.remote)
    except IncusVersionError as err:
        if needs_version(args, directory):
            raise
        print(f"Warning: {err}", file=sys.stderr)


def needs_version(args, directory):
    if args.action not in VERSIONED:
        return False
    return not (args.action == "apply" and saved_deletion(directory))


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
