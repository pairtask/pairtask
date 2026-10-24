#!/usr/bin/env python3
"""pairtask: the one host command to install, run and recover the sandbox.

Every subcommand runs one of the host scripts beside this file with the
matching action, or an Incus command; flags after the subcommand pass through
unchanged. The reference is docs/host-commands.md.
"""

import argparse
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
        file, actions, _ = ACTIONS[name]
        if not rest or rest[0] not in actions:
            raise SystemExit("pairtask %s needs one of: %s" % (name, ", ".join(actions)))
        return [script(file, actions[rest[0]], *rest[1:])]
    if name not in COMPOSITE:
        raise SystemExit("unknown subcommand %s" % name)
    return composite(name, rest, system, instances)


def composite(name, rest, system, instances):
    parser = composite_parser(name)
    if name == "install":
        opts, _ = parser.parse_known_args(rest) # the rollout parses the rest before any step runs
    else:
        opts = parser.parse_args(rest)
    remote = opts.remote or (REMOTE if system == "Darwin" else "local")
    mac = system == "Darwin"
    vm = [script("sandbox-host.py", "apply")] if mac else []
    if name == "install":
        return vm + [script("sandbox-provision.py", "rollout", *rest), incus(opts.project, "list", remote + ":")]
    if name == "start":
        return vm + containers("start", remote, opts.project, instances)
    if name == "stop":
        steps = containers("stop", remote, opts.project, instances)
        if opts.vm and mac:
            steps.append(Step("command", ["colima", "stop", PROJECT]))
        return steps
    return [incus(opts.project, "list", remote + ":")]


def containers(action, remote, project, instances):
    """One Incus command per container: secured starts before workspace and
    stops after it, and each waits for the previous. The targets come from
    the live list, at run time unless given."""
    def resolve(lookup):
        present = lookup(remote, project)
        names = [n for n in ("secured", "workspace") if n in present]
        if action == "stop":
            names.reverse()
        return [incus(project, action, remote + ":" + n) for n in names]
    if instances is not None:
        return resolve(instances)
    return [Step("incus", ["incus", "--project", project, action, remote + ":<each container>"], resolve)]


def list_instances(remote, project):
    import json
    out = subprocess.run(["incus", "--project", project, "list", remote + ":", "--format=json"],
                         check=True, capture_output=True, text=True).stdout
    return {item["name"] for item in json.loads(out)}


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
    known = set(COMPOSITE) | set(SCRIPTED) | set(ACTIONS)
    if argv[0] not in known:
        print(usage(), file=sys.stderr)
        return 2
    steps = plan(argv)
    if dry:
        for step in steps:
            print(step)
        return 0
    preflight(steps)
    for step in steps:
        resolved = step.resolve(list_instances) if step.resolve else [step] # after the steps before it, so the VM is up
        for each in resolved:
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
