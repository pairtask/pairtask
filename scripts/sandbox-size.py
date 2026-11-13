#!/usr/bin/env python3
"""pairtask size: how big the sandbox is, and growing it.

`show` reports the macOS VM, the workspace container and each volume, with a
warning for any volume at 90% or more; it only reads. `plan` checks the sizes
you ask for (never smaller, within the VM), records them in the deployment's
size.auto.tfvars.json and previews the change; `apply` carries out that plan."""

import argparse
import importlib.util
import platform
import re
import subprocess
import sys
from pathlib import Path

from sandbox_operator_config import deployment_directory
from sandbox_remote import REMOTE, colima_profile, default_remote
import sandbox_size
import sandbox_size_plan
import sandbox_vm_size

PROVISION = Path(__file__).resolve().parent / "sandbox-provision.py"
HOST = Path(__file__).resolve().parent / "sandbox-host.py"


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
    vm = sub.add_parser("vm", help="the macOS VM's CPUs, RAM (GiB) and disk (GiB); --apply to resize")
    add_target(vm)
    for key in sandbox_vm_size.KEYS:
        vm.add_argument("--" + key, dest="vm_" + key, type=int, metavar="N")
    vm.add_argument("--apply", action="store_true", help="stop the VM, resize it and start it again")
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
        self.choice = seams.get("choice") or sandbox_vm_size.VM_SIZE_FILE


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
    """show, plan or vm. vm reads Incus only when it needs to: a resize that
    stopped the VM resumes without it."""
    if ctx.args.action == "vm":
        return resize_vm(ctx)
    report = collect(ctx)
    if ctx.args.action == "show":
        print(sandbox_size.as_json(report) if ctx.args.json else sandbox_size.render(report))
        return 0
    return plan(ctx, report)


def collect(ctx):
    return sandbox_size.collect(ctx.query, profile_dir(ctx.args.remote), ctx.system, ctx.args.project)


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
    """What the request is checked against. The root disk is shared, so a
    new one is checked against every container's."""
    live = sandbox_size_plan.live_sizes(report)
    if "disk_gib" in request:
        live = sandbox_size_plan.shared_root(ctx.query, ctx.args.project, live)
    return sandbox_size_plan.Facts(
        live, sandbox_size_plan.capacity(ctx.query, report, ctx.system),
        sandbox_size_plan.reserved(ctx.query, ctx.args.project), sandbox_size_plan.conflicts(directory, request))


def resize_vm(ctx):
    """Preview, or with --apply carry out, the VM's new sizes."""
    if ctx.system != "Darwin":
        print("pairtask size: Incus runs on this host; there's no VM to resize")
        return 0
    current = owned_profile(ctx)
    request, target, errors = vm_request(ctx, current)
    for error in errors:
        print(f"pairtask size: {error}", file=sys.stderr)
    if errors:
        return 1
    plan = sandbox_vm_size.steps(current, request, target, host_seams(ctx))
    describe_vm(current, sandbox_vm_size.overlay(current, request), target)
    if not ctx.args.apply:
        print("Steps: " + "; ".join(step.name for step in plan))
        print("Preview only. --apply stops the VM, which ends running sessions, then starts it again.")
        return 0
    return run_steps(ctx, plan)


def owned_profile(ctx):
    """The VM's settings, once its profile is checked to be the pairtask one,
    as installed and unchanged by hand; ValueError otherwise."""
    if ctx.args.remote != REMOTE:
        raise ValueError(f"size vm resizes the pairtask VM behind remote {REMOTE}, not remote {ctx.args.remote}")
    marker = profile_dir(ctx.args.remote) / "pairtask-owner.json"
    if not marker.exists():
        raise ValueError("no pairtask VM yet; pairtask install first")
    current = sandbox_vm_size.read_profile(marker)
    sandbox_vm_size.check_profile(marker.parent, current, sandbox_vm_size.read_choice(ctx.choice),
                                   load_host().installed_config())
    return current


def describe_vm(current, desired, target):
    print(f"VM: {format_vm(current)} → {format_vm(desired)}")
    if target is None and desired.get("disk", 0) > current.get("disk", 0):
        print("The Incus pool is not grown with the disk: it isn't the Incus-managed ZFS pool the sandbox "
              "creates. Grow it manually; pairtask size show reports its size.")


def load_host():
    """sandbox-host.py, whose installed settings the VM must keep."""
    spec = importlib.util.spec_from_file_location("pairtask_sandbox_host", HOST)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def vm_request(ctx, current):
    """The sizes to apply, the pool target and why they can't be applied.
    An unfinished resize resumes with what it recorded, checked when it was,
    without Incus: the VM, and Incus with it, may be down."""
    request = {key: getattr(ctx.args, "vm_" + key) for key in sandbox_vm_size.KEYS
               if getattr(ctx.args, "vm_" + key) is not None}
    earlier = sandbox_vm_size.read_pending(ctx.choice)
    if earlier:
        return resume(ctx, request, earlier)
    if not request:
        ctx.parser.error("size vm: give at least one of --cpu, --memory, --disk")
    report = reachable_report(ctx)
    if report is None:
        return request, None, offline_errors(request, current)
    target = sandbox_vm_size.pending_target(current, request, workspace_pool(ctx, report), None)
    return request, target, sandbox_vm_size.validate(request, current, vm_needs(ctx, report))


def resume(ctx, request, earlier):
    """The recorded resize, or why new sizes must wait for it to finish."""
    choice = sandbox_vm_size.read_choice(ctx.choice)
    if any(choice.get(key) != value for key, value in request.items()):
        return request, earlier, [
            f"an earlier resize is pending (pool {earlier['pool']} to {earlier['size_gib']} GiB); "
            "finish it with pairtask size vm --apply, then ask for new sizes"]
    return choice, earlier, []


def reachable_report(ctx):
    """The live report, or None while Incus doesn't answer."""
    try:
        return collect(ctx)
    except sandbox_size.Unreachable:
        return None


def offline_errors(request, current):
    """Without Incus, increases pass; a decrease can't be checked, and a disk
    grow can't size the pool."""
    errors = sandbox_vm_size.validate(request, current, None)
    if request.get("disk", 0) > current.get("disk", 0):
        errors.append("a disk grow resizes the Incus pool, which doesn't answer; pairtask start, then try again")
    return errors


def workspace_pool(ctx, report):
    name = (report.get("workspace") or {}).get("pool")
    if not name:
        return None
    pool = ctx.query(f"/1.0/storage-pools/{name}") or {}
    return {**pool, "name": name}


def vm_needs(ctx, report):
    """The CPUs and GiB the containers take, or None when Incus can't say."""
    live = sandbox_size_plan.live_sizes(report)
    if "cpu_count" not in live or "memory_gib" not in live:
        return None
    reserved = sandbox_size_plan.reserved(ctx.query, ctx.args.project)
    return {"cpu": live["cpu_count"] + reserved["cpu"],
            "memory": live["memory_gib"] + reserved["memory_gib"] + sandbox_size_plan.VM_RESERVE_GIB}


def host_seams(ctx):
    return {"directory": profile_dir(ctx.args.remote), "choice": ctx.choice,
            "profile": colima_profile(ctx.args.remote) or "pairtask", "remote": ctx.args.remote,
            "start": [sys.executable, str(HOST), "apply"], "query": ctx.query, "run": ctx.run}


def format_vm(config):
    return f"{config.get('cpu')} CPU · {config.get('memory')} GiB RAM · {config.get('disk')} GiB disk"


def run_steps(ctx, plan):
    """Each step in turn; the first that fails stops the rest."""
    for step in plan:
        print(f"• {step.name}", flush=True)
        failed = command(ctx, step.argv) if step.argv else call(step.write)
        if failed:
            print(f"pairtask size: '{step.name}' failed; fix it, then run the same pairtask size vm "
                  "command again to resume", file=sys.stderr)
            return 1
    return 0


def command(ctx, argv):
    """Run a command step; True when it failed or couldn't start."""
    try:
        return ctx.run(argv) != 0
    except OSError as error:
        print(f"pairtask size: {error}", file=sys.stderr)
        return True


def call(write):
    """Run a file step; True when it failed."""
    try:
        write()
    except (OSError, ValueError, sandbox_size.Unreachable) as error:
        print(f"pairtask size: {error}", file=sys.stderr)
        return True
    return False


if __name__ == "__main__":
    sys.exit(main())
