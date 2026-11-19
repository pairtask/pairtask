#!/usr/bin/env python3
"""pairtask update: move the agent clients (Claude Code, Codex and the
CodeScene MCP server) in the running sandbox to selected versions, without
replacing the containers. Claude Code's stable channel can be older than
the version installed: an update then moves it back to stable.

Both containers change together or neither does; sessions already running
keep the client they started with, and no new session starts while the two
are being switched. `--rollback` returns to the client set from before the
last update, with no download. `pairtask upgrade` ends an update: a new
sandbox starts from its release's own versions.
"""

import argparse
from pathlib import Path
import re
import subprocess
import sys

from sandbox_host_user import refuse_root
from sandbox_operator_config import deployment_directory, existing_deployment
from sandbox_remote import default_remote
from sandbox_tools_install import Container
from sandbox_tools_plan import Instance, Resolver, retained_lines
import sandbox_tools_update as update

CLIENT_FLAGS = {"claude": "--claude", "codex": "--codex", "cs-mcp": "--cs-mcp"}
# What each client moves to when its flag is left out.
DEFAULTS = {"claude": "its publisher's stable channel", "codex": "its latest release", "cs-mcp": "its latest release"}


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_-]*", value):
        raise argparse.ArgumentTypeError("Expected an Incus name without spaces or a colon")
    return value


def arguments(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name, flag in CLIENT_FLAGS.items():
        parser.add_argument(flag, dest=name, metavar="VERSION", help="this exact version of {}; {} if left out".format(name, DEFAULTS[name]))
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--rollback", action="store_true", help="return to the clients from before the last update; no download")
    mode.add_argument("--status", action="store_true", help="what the last update did and whether sessions can start; changes nothing")
    mode.add_argument("--repair", action="store_true", help="finish or revert an interrupted update, a failed cleanup included")
    mode.add_argument("--retained", action="store_true",
                      help="only what each container's kept client generations cost; changes nothing (pairtask status)")
    parser.add_argument("--remote", default=default_remote())
    parser.add_argument("--project", default="pairtask", type=identifier)
    parser.add_argument("--state-dir", type=Path, help="the operator checkout; discovered when left out")
    args = parser.parse_args(argv)
    args.requested = {name: getattr(args, name) for name in CLIENT_FLAGS if getattr(args, name)}
    if args.requested and any((args.rollback, args.status, args.repair, args.retained)):
        parser.error("a version goes with a plain update, not with --rollback, --status, --repair or --retained")
    return args


def instances_of(args):
    return {role: Instance(Container(args.remote, args.project, role)) for role in update.ROLES}


def transaction(args, directory):
    instances = instances_of(args)
    work = update.Update(update.journal_in(directory), instances, Resolver(instances))
    return work.of({"project": args.project, "remote": args.remote})


def report_retained(args):
    """Print what each container keeps. Generations are never pruned by an
    update, so this is how an operator sees when an upgrade is due."""
    for line in retained_lines(instances_of(args)):
        print(line)


def run(args, directory):
    """The record the chosen action ends with, or None when there has been
    no update."""
    work = transaction(args, directory)
    if args.status:
        return work.status()["journal"]
    if args.repair:
        return work.recover(repair=True)
    if args.rollback:
        return work.rollback()
    return work.run(args.requested)


def describe(record):
    """One paragraph a person can act on, and the exit code."""
    if record is None:
        return "No client update has been run in this sandbox.", 0
    tell = DESCRIPTIONS.get(record["phase"], unfinished)
    return tell(record)


def done(record):
    if record["outcome"] == "candidate":
        return "{}: both containers now run {}.".format(kind_of(record), versions_of(record)), 0
    return ("The update was undone: both containers are back on the clients they had. Reason: {}."
            .format(record.get("detail") or "a container didn't verify")), 1


def aborted(record):
    detail = record.get("detail")
    return "Nothing was changed: {}.".format(detail or "the update stopped before any container was switched"), aborted_code(detail)


def superseded(_):
    return "The last client update ended with an upgrade; the sandbox runs its release's own clients.", 0


def failed(record):
    return ("The update failed ({}): {}. New sessions are held back. Run `pairtask update --rollback`, "
            "or `pairtask upgrade` to replace the sandbox.".format(record.get("failure"), record.get("detail") or "no detail")), 1


def unfinished(record):
    return "A client update is unfinished (phase {}). Run `pairtask update --repair`.".format(record["phase"]), 1


DESCRIPTIONS = {"done": done, "aborted": aborted, "superseded": superseded, "failed": failed}


def aborted_code(detail):
    return 0 if detail and "already run" in detail else 1


def kind_of(record):
    return "Rolled back" if record.get("kind") == "rollback" else "Updated"


def versions_of(record):
    requested = record.get("requested") or {}
    if not requested:
        return "the earlier client set"
    return ", ".join("{} {}".format(name, requested[name]) for name in sorted(requested))


def located(args):
    """The deployment's state directory. Nothing is ever created here: an
    update is for a sandbox that exists. Without one, --status has nothing
    to report and every other action refuses."""
    if args.state_dir:
        return deployment_directory(args.state_dir, args.project)
    directory = existing_deployment(args.project)
    if directory is None and not args.status:
        raise ValueError("no sandbox is installed for project {}; run pairtask install first".format(args.project))
    return directory


def main(argv=None):
    if refuse_root("pairtask update"):
        return 1
    args = arguments(argv)
    if args.retained:  # asks the containers only: no deployment is looked up, none is made
        report_retained(args)
        return 0
    directory = located(args)
    try:
        record = run(args, directory) if directory else None
    except update.Blocked as blocked:
        print("{}. Nothing more was changed; run `pairtask update --repair` when the container is back.".format(blocked))
        return 1
    message, code = describe(record)
    print(message)
    if args.status:
        report_retained(args)
    return code


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        sys.exit("pairtask update: {}".format(error))
    except KeyboardInterrupt:
        sys.exit("pairtask update: interrupted; run `pairtask update --repair` to finish or revert it")
