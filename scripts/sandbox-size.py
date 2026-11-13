#!/usr/bin/env python3
"""pairtask size: how big the sandbox is, and growing it.

`show` reports the macOS VM, the workspace container and each volume, with a
warning for any volume at 90% or more; it only reads. `plan` checks the sizes
you ask for (never smaller, within the VM), records them in the deployment's
size.auto.tfvars.json and previews the change; `apply` carries out that plan."""

import argparse
import platform
import re
import subprocess
import sys
from pathlib import Path

from sandbox_operator_config import deployment_directory
from sandbox_remote import colima_profile, default_remote
import sandbox_size
import sandbox_size_plan

PROVISION = Path(__file__).resolve().parent / "sandbox-provision.py"


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def add_target(p):
    p.add_argument("--remote", default=default_remote(), type=identifier)
    p.add_argument("--project", default="pairtask", type=identifier)


def parser():
    p = argparse.ArgumentParser(prog="pairtask size", description=__doc__)
    sub = p.add_subparsers(dest="action", required=True)
    show = sub.add_parser("show", help="sizes and how full each volume is")
    add_target(show)
    show.add_argument("--json", action="store_true", help="print the report as JSON")
    plan = sub.add_parser("plan", help="check, record and preview new sizes (GiB, CPUs)")
    add_target(plan)
    for flag, var in sandbox_size_plan.REQUEST_FLAGS.items():
        plan.add_argument("--" + flag.replace("_", "-"), dest=flag, type=int, metavar="N", help=var)
    apply = sub.add_parser("apply", help="carry out the plan `size plan` previewed")
    add_target(apply)
    return p


def profile_dir(remote):
    """The Colima profile directory behind remote; never created here."""
    return Path.home() / ".colima" / (colima_profile(remote) or "pairtask")


def provision(action, args):
    """The existing provisioning command for action on this deployment."""
    return [sys.executable, str(PROVISION), action, "--project", args.project, "--remote", args.remote]


class Context:
    """One run's arguments and what it reaches the world through: the Incus
    query, the operating system, the runner for provisioning commands and
    the deployment directory (found when None)."""

    def __init__(self, p, args, seams):
        self.parser, self.args = p, args
        self.query = seams.get("query") or sandbox_size.incus_query(args.remote)
        self.system = seams.get("system") or platform.system()
        self.run = seams.get("run") or (lambda argv: subprocess.run(argv).returncode)
        self.directory = seams.get("directory")


def main(argv=None, **seams):
    """Seams for tests: query, system, run, directory."""
    p = parser()
    ctx = Context(p, p.parse_args(argv), seams)
    if ctx.args.action == "apply":
        return ctx.run(provision("apply", ctx.args))
    try:
        return act(ctx)
    except sandbox_size.Unreachable as error:
        print(f"pairtask size: Incus on {ctx.args.remote} didn't answer: {error}. Is the sandbox running? pairtask start", file=sys.stderr)
    except ValueError as error:
        print(f"pairtask size: {error}", file=sys.stderr)
    return 1


def act(ctx):
    """show or plan, from one report."""
    report = sandbox_size.collect(ctx.query, profile_dir(ctx.args.remote), ctx.system, ctx.args.project)
    if ctx.args.action == "show":
        print(sandbox_size.as_json(report) if ctx.args.json else sandbox_size.render(report))
        return 0
    return plan(ctx, report)


def plan(ctx, report):
    """Check the request, record it and preview the change."""
    request = sandbox_size_plan.request_from(ctx.args)
    if not request:
        ctx.parser.error("size plan: give at least one size, e.g. --agent-home 10")
    if report["workspace"] is None:
        print("pairtask size: the workspace isn't provisioned yet; pairtask install first", file=sys.stderr)
        return 1
    directory = ctx.directory or deployment_directory(None, ctx.args.project)
    sandbox_size_plan.size_file(directory)
    errors = sandbox_size_plan.validate(request, facts(ctx, report, directory, request))
    for error in errors:
        print(f"pairtask size: {error}", file=sys.stderr)
    if errors:
        return 1
    path = sandbox_size_plan.write_sizes(directory, request)
    print(f"Recorded {', '.join(f'{k}={v}' for k, v in sorted(request.items()))} in {path}")
    return ctx.run(provision("plan", ctx.args))


def facts(ctx, report, directory, request):
    """What the request is checked against."""
    return sandbox_size_plan.Facts(
        sandbox_size_plan.live_sizes(report), sandbox_size_plan.capacity(ctx.query, report, ctx.system),
        sandbox_size_plan.reserved(ctx.query, ctx.args.project), sandbox_size_plan.conflicts(directory, request))


if __name__ == "__main__":
    sys.exit(main())
