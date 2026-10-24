#!/usr/bin/env python3
"""pairtask: the one host command to install, run and recover the sandbox.

Every subcommand runs one of the host scripts beside this file with the
matching action, or an Incus command; flags after the subcommand pass through
unchanged. The reference is docs/host-commands.md.
"""

import argparse
import json
import platform
import re
import runpy
import shlex
import subprocess
import sys
from pathlib import Path

from sandbox_remote import REMOTE

SCRIPTS = Path(__file__).resolve().parent
PROJECT = "pairtask"

# subcommand: (script, leading arguments, one-line help). Scripts with an
# action word take it first; everything the user adds follows.
SCRIPTED = {
    "upgrade": ("sandbox-provision.py", ["rollout"], "download the release, replace the containers, keep data; ends sessions"),
    "plan": ("sandbox-provision.py", ["plan"], "preview a provisioning change (--replace, --destroy)"),
    "apply": ("sandbox-provision.py", ["apply"], "apply the saved plan; ends sessions when it replaces"),
    "recover": ("sandbox-provision.py", ["recover"], "repair provisioning state without changing data"),
    "tools": ("sandbox-provision.py", ["tools-retry"], "retry the pinned agent tools install"),
    "ssh": ("sandbox-ssh.py", [], "create the key-only SSH path into the workspace"),
    "download": ("sandbox-download.py", [], "fetch and verify a release image without provisioning"),
    "shell": ("sandbox-session.py", ["shell"], "SSH into the workspace; restores your terminal after"),
    "chat": ("sandbox-session.py", ["chat"], "the operator chat; restores your terminal after"),
}
ACTIONS = {
    "vm": ("sandbox-host.py", {"plan": "plan", "apply": "apply"}, "the macOS Colima VM: plan | apply"),
    "mounts": ("sandbox-host.py", {"plan": "mounts-plan", "apply": "mounts-apply"}, "host directories in the workspace: plan | apply --mounts-file FILE"),
    "share": ("sandbox-share.py", {"plan": "plan", "setup": "setup", "run": "run"}, "macOS writable sharing: plan | setup | run --mounts-file FILE"),
}
COMPOSITE = {
    "install": "first install: start the VM on macOS, roll the release out, show status",
    "start": "start the containers (and the VM on macOS)",
    "stop": "stop the containers; --vm also stops the macOS VM; ends sessions",
    "status": "list the containers and their state",
}


class Step:
    """One thing the command will do: a script with argv, or a command. A
    step whose targets depend on the live Incus state resolves them when it
    runs, so planning never touches the host."""

    def __init__(self, kind, argv, resolve=None):
        self.kind, self.argv, self.resolve = kind, list(argv), resolve

    def __repr__(self):
        return "%s: %s" % (self.kind, shlex.join(self.argv))

    def __eq__(self, other):
        return isinstance(other, Step) and (self.kind, self.argv) == (other.kind, other.argv)


def script(name, *args):
    return Step("script", [name] + list(args))


def incus(project, *args):
    return Step("incus", ["incus", "--project", project] + list(args))


def identifier(value):
    """An Incus remote or project name: no colon, whitespace or shell syntax."""
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def composite_parser(name):
    """The flags a composite command takes itself; install hands everything
    else to the rollout."""
    parser = argparse.ArgumentParser(prog="pairtask " + name, description=COMPOSITE[name], add_help=(name != "install"))
    parser.add_argument("--remote", default=None, type=identifier)
    parser.add_argument("--project", default=PROJECT, type=identifier)
    if name == "stop":
        parser.add_argument("--vm", action="store_true", help="also stop the macOS VM")
    return parser


def plan(argv, system=None, instances=None):
    """The steps for argv, without running anything. instances(remote,
    project) lists the containers when a step needs to know whether
    `secured` exists; None defers that to run time."""
    system = system or platform.system()
    name, rest = argv[0], list(argv[1:])
    if name in SCRIPTED:
        file, leading, _ = SCRIPTED[name]
        return [script(file, *leading, *rest)]
    if name in ACTIONS:
        return [action_step(name, rest)]
    if name in COMPOSITES:
        return COMPOSITES[name](rest, system, instances)
    raise SystemExit("unknown subcommand %s" % name)


def action_step(name, rest):
    file, actions, _ = ACTIONS[name]
    if not rest or rest[0] not in actions:
        raise SystemExit("pairtask %s needs one of: %s" % (name, ", ".join(actions)))
    return script(file, actions[rest[0]], *rest[1:])


def composite_options(name, rest, system):
    """The parsed flags of a composite command and the remote it targets;
    install keeps the rest for the rollout."""
    parser = composite_parser(name)
    opts = parser.parse_known_args(rest)[0] if name == "install" else parser.parse_args(rest)
    opts.remote = opts.remote or (REMOTE if system == "Darwin" else "local")
    opts.mac = system == "Darwin"
    return opts


def vm_steps(opts):
    return [script("sandbox-host.py", "apply")] if opts.mac else []


def install_steps(rest, system, instances):
    opts = composite_options("install", rest, system)
    return vm_steps(opts) + [script("sandbox-provision.py", "rollout", *rest), incus(opts.project, "list", opts.remote + ":")]


def start_steps(rest, system, instances):
    opts = composite_options("start", rest, system)
    return vm_steps(opts) + containers("start", opts.remote, opts.project, instances)


def stop_steps(rest, system, instances):
    opts = composite_options("stop", rest, system)
    steps = containers("stop", opts.remote, opts.project, instances)
    if opts.vm and opts.mac:
        steps.append(Step("command", ["colima", "stop", PROJECT]))
    return steps


def status_steps(rest, system, instances):
    opts = composite_options("status", rest, system)
    return [incus(opts.project, "list", opts.remote + ":")]


COMPOSITES = {"install": install_steps, "start": start_steps, "stop": stop_steps, "status": status_steps}


def containers(action, remote, project, instances):
    """One Incus command per container that is not already in the wanted
    state: secured starts before workspace and stops after it, and each waits
    for the previous. The states come from the live list, at run time unless
    given (a mapping name → status, or a set of names when states are unknown)."""
    wanted = "Running" if action == "start" else "Stopped"

    def resolve(lookup):
        present = lookup(remote, project)
        states = present if isinstance(present, dict) else {n: None for n in present}
        names = [n for n in ("secured", "workspace") if n in states and states[n] != wanted]
        if action == "stop":
            names.reverse()
        return [incus(project, action, remote + ":" + n) for n in names]
    if instances is not None:
        return resolve(instances)
    return [Step("incus", ["incus", "--project", project, action, remote + ":<each container not yet " + wanted.lower() + ">"], resolve)]


def list_instances(remote, project):
    """The containers of the project with their status, from Incus."""
    out = subprocess.run(["incus", "--project", project, "list", remote + ":", "--format=json"],
                         check=True, capture_output=True, text=True).stdout
    return {item["name"]: item.get("status") for item in json.loads(out)}


# The scripts' own argument parsers, by file, so every script step is parsed
# before the first step changes anything on the host.
PARSERS = {"sandbox-provision.py": "arguments", "sandbox-host.py": "parse_args",
           "sandbox-share.py": "arguments", "sandbox-session.py": "parser"}


def preflight(steps):
    """Parse every script step's arguments up front: a typo in the flags for
    the second step must not run the first."""
    for step in steps:
        if step.kind != "script" or step.argv[0] not in PARSERS:
            continue
        sys.argv = [str(SCRIPTS / step.argv[0])] + step.argv[1:]
        module = runpy.run_path(sys.argv[0], run_name="pairtask_preflight")
        parser = module[PARSERS[step.argv[0]]]()
        if hasattr(parser, "parse_args"):
            parser.parse_args(step.argv[1:])


def run(step):
    if step.kind == "script":
        sys.argv = [str(SCRIPTS / step.argv[0])] + step.argv[1:]
        runpy.run_path(sys.argv[0], run_name="__main__")
        return 0
    return subprocess.run(step.argv, check=False).returncode


def usage():
    lines = ["usage: pairtask <command> [flags]", "", "Commands (flags after the command pass to the script it runs):"]
    for name, help_text in list(COMPOSITE.items()) + [(n, v[2]) for n, v in SCRIPTED.items()] + [(n, v[2]) for n, v in ACTIONS.items()]:
        lines.append("  %-10s %s" % (name, help_text))
    lines += ["", "Reference: docs/host-commands.md. `pairtask --dry-run <command>` prints the steps without running them."]
    return "\n".join(lines)


def main(argv=None):
    argv = sys.argv[1:] if argv is None else list(argv)
    argv, dry = own_dry_run(argv)
    if not argv or argv[0] in ("-h", "--help", "help"):
        print(usage())
        return 0
    if argv[0] not in set(COMPOSITE) | set(SCRIPTED) | set(ACTIONS):
        print(usage(), file=sys.stderr)
        return 2
    steps = plan(argv)
    if dry:
        for step in steps:
            print(step)
        return 0
    preflight(steps)
    return execute(steps)


def execute(steps):
    """Run the steps in order, resolving a lazy one right before its turn
    (so the VM is up when the containers are listed); stop at the first failure."""
    for step in steps:
        for each in step.resolve(list_instances) if step.resolve else [step]:
            code = run(each)
            if code:
                return code
    return 0


def own_dry_run(argv):
    """`pairtask --dry-run <command> …`: only in front is it pairtask's own;
    after the command every argument belongs to the step."""
    if argv and argv[0] == "--dry-run":
        return argv[1:], True
    return argv, False


if __name__ == "__main__":
    try:
        sys.exit(main())
    except KeyboardInterrupt:
        sys.exit(130)
