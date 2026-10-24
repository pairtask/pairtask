#!/usr/bin/env python3
"""Open a shell or the operator chat in the sandbox; restore this terminal after."""

import argparse
from pathlib import Path
import re
import sys

from sandbox_remote import default_remote
from sandbox_terminal import run_session

SSH_CONFIG = Path(__file__).resolve().parents[1] / "infra/incus/ssh/config"


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def shell_command(args):
    return ["ssh", "-F", str(args.ssh_config), "workspace"] + args.command


def chat_command(args):
    target = "%s:%s" % (args.remote, args.secured)
    return ["incus", "--project", args.project, "exec", "-t", target, "--", "pairtask", "operator", "chat"]


def parser():
    p = argparse.ArgumentParser(description=__doc__)
    sub = p.add_subparsers(dest="session", required=True)
    shell = sub.add_parser("shell", help="SSH into the workspace as agent (ssh -F infra/incus/ssh/config workspace)")
    shell.add_argument("--config", "--ssh-config", dest="ssh_config", type=Path, default=SSH_CONFIG)
    shell.add_argument("command", nargs=argparse.REMAINDER, help="optional remote command")
    shell.set_defaults(build=shell_command)
    chat = sub.add_parser("chat", help="the operator chat in the secured runtime (incus exec -t … pairtask operator chat)")
    chat.add_argument("--remote", default=default_remote(), type=identifier)
    chat.add_argument("--project", default="pairtask", type=identifier)
    chat.add_argument("--secured", default="secured", type=identifier)
    chat.set_defaults(build=chat_command)
    return p


def main(argv=None):
    args = parser().parse_args(argv)
    return run_session(args.build(args))


if __name__ == "__main__":
    sys.exit(main())
