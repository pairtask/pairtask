#!/usr/bin/env python3
"""Open a shell, the operator chat or an operator command in the sandbox;
restore this terminal after. Operator commands run as root in the secured
runtime, where the operator's keys and socket are."""

import argparse
from pathlib import Path
import re
import sys

from sandbox_remote import default_remote
from sandbox_terminal import run_session
import sandbox_version

SSH_CONFIG = Path(__file__).resolve().parents[1] / "infra/incus/ssh/config"


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def shell_command(args):
    tty = ["-t"] if args.tty else [] # a remote TUI such as the dashboard asks for one; pipes stay bytes
    return ["ssh"] + tty + ["-F", str(args.ssh_config), "workspace"] + args.command


def secured_exec(args, command, tty=True):
    """incus exec of command in the secured runtime, with a terminal or not."""
    target = "%s:%s" % (args.remote, args.secured)
    return ["incus", "--project", args.project, "exec"] + (["-t"] if tty else []) + [target, "--"] + command


def chat_command(args):
    return secured_exec(args, ["pairtask", "operator", "chat"])


def identity_command(action):
    return lambda args: secured_exec(args, ["pairtask", "operator", "identity", action])


def secured_command(args):
    """A login shell in the secured runtime, or one command there."""
    command = args.command[1:] if args.command[:1] == ["--"] else args.command
    if not command:
        return secured_exec(args, ["/bin/bash", "-l"])
    return secured_exec(args, command, tty=args.tty)


def add_secured_target(p):
    p.add_argument("--remote", default=default_remote(), type=identifier)
    p.add_argument("--project", default="pairtask", type=identifier)
    p.add_argument("--secured", default="secured", type=identifier)


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="session", required=True)
    shell = sub.add_parser("shell", help="SSH into the workspace as agent (ssh -F infra/incus/ssh/config workspace)")
    shell.add_argument("--config", "--ssh-config", dest="ssh_config", type=Path, default=SSH_CONFIG)
    shell.add_argument("--tty", action="store_true", help="allocate a terminal for the remote command (TUIs such as the dashboard)")
    shell.add_argument("command", nargs=argparse.REMAINDER, help="optional remote command")
    shell.set_defaults(build=shell_command)
    chat = sub.add_parser("chat", help="the operator chat in the secured runtime (incus exec -t … pairtask operator chat)")
    add_secured_target(chat)
    chat.set_defaults(build=chat_command)
    for action in ("init", "status"):
        identity = sub.add_parser("identity-" + action,
                                  help="pairtask operator identity %s in the secured runtime" % action)
        add_secured_target(identity)
        identity.set_defaults(build=identity_command(action))
    secured = sub.add_parser("secured", help="a shell in the secured runtime as root, where operator commands run; or one command there")
    add_secured_target(secured)
    secured.add_argument("--tty", action="store_true", help="allocate a terminal for the command")
    secured.add_argument("command", nargs=argparse.REMAINDER, help="optional command")
    secured.set_defaults(build=secured_command)
    return p


def main(argv=None):
    args = parser().parse_args(argv)
    if args.session == "chat" and args.secured == "secured":
        sandbox_version.announce(args.project, args.remote)  # bounded; never waits on GitHub
    return run_session(args.build(args))


if __name__ == "__main__":
    sys.exit(main())
