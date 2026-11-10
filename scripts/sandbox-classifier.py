#!/usr/bin/env python3
"""pairtask classifier on | off | status: the shadow classifier (#339) from
the host, without environment variables.

`on` takes the provider (jev, or clef with --account), reads the key from a
hidden prompt or --key-file, never from the command line, and hands it on
stdin to the secured container's pairtask-broker-setup, which stores it for
the broker alone and restarts the broker in shadow mode. `off` removes the
key and the settings and restarts the broker. `status` shows the settings
(never the key) and what the classifier recorded lately.
"""

import argparse
import getpass
import os
from pathlib import Path
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import sandbox_host_user  # noqa: E402

SECURED_OPERATOR = "/run/collab-ai-operator/operator.sock"
SETUP = "/usr/local/lib/pairtask/pairtask-broker-setup"
PROVIDERS = ("jev", "clef")
KEY_MAX = 512
ACCOUNT_MAX = 64
TIMEOUT = 120


def valid_account(account):
    """A Cloudflare account ID: ASCII letters, digits, - and _, bounded."""
    return 0 < len(account) <= ACCOUNT_MAX and account.isascii() and all(ch.isalnum() or ch in "-_" for ch in account)


class Secured:
    """Commands in the secured container, through incus exec."""

    def __init__(self, project, remote, run=subprocess.run):
        self.base = ["incus", "--project", project, "exec", remote.rstrip(":") + ":secured", "-T", "--"]
        self.run = run

    def execute(self, *command, stdin=None, timeout=TIMEOUT):
        return self.run(self.base + list(command), input=stdin, capture_output=True, text=True, timeout=timeout, check=False)


def read_key(args, stdin=sys.stdin, prompt=getpass.getpass):
    """The key from --key-file, else from a hidden prompt on a terminal."""
    if args.key_file:
        path = Path(args.key_file)
        if path.is_symlink() or not path.is_file():
            raise ValueError("--key-file must be a regular file")
        key = path.read_text().strip()
    elif stdin.isatty():
        key = prompt("API key for %s (not echoed): " % args.provider).strip()
    else:
        raise ValueError("no terminal for the key prompt; use --key-file PATH")
    if not key or "\n" in key or len(key.encode()) > KEY_MAX or not all(32 < ord(ch) < 127 for ch in key):
        raise ValueError("the key must be one line of printable ASCII, at most %d bytes" % KEY_MAX)
    return key


def turn_on(secured, args, key):
    if args.provider == "clef" and not args.account:
        raise ValueError("clef needs --account ID (the Cloudflare account)")
    if args.provider != "clef" and args.account:
        raise ValueError("--account applies to clef only")
    if args.account and not valid_account(args.account):
        raise ValueError("--account must be letters, digits, - or _, at most %d characters" % ACCOUNT_MAX)
    command = [SETUP, "classifier", "on", args.provider] + ([args.account] if args.account else [])
    result = secured.execute(*command, stdin=key + "\n")
    if result.returncode != 0:
        raise ValueError(failure("turning the classifier on", result))
    print("classifier on: %s, shadow mode (records which agent an untagged message was for; applies nothing)" % args.provider)


def turn_off(secured):
    result = secured.execute(SETUP, "classifier", "off")
    if result.returncode != 0:
        raise ValueError(failure("turning the classifier off", result))
    print("classifier off")


def status(secured, args):
    result = secured.execute(SETUP, "classifier", "status")
    if result.returncode != 0:
        raise ValueError(failure("reading the classifier settings", result))
    settings = dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
    if settings.get("classifier", "off") == "off":
        print("Classifier: off")
        return
    print("Classifier: %s, %s%s, key %s" % (settings.get("classifier"), settings.get("provider"),
                                           " (account %s)" % settings["account"] if settings.get("account") else "",
                                           settings.get("key", "missing")))
    report = secured.execute("pairtask", "operator", "classifier", "report", "--socket", SECURED_OPERATOR,
                             "--since", args.since, *(["--json"] if args.json else []))
    if report.returncode != 0:
        print("Report unavailable: " + last_line(report.stderr))
        return
    sys.stdout.write(report.stdout)


def failure(what, result):
    return "%s failed: %s" % (what, last_line(result.stderr) or "exit %d" % result.returncode)


def last_line(text):
    lines = [line for line in text.strip().splitlines() if line.strip()]
    return lines[-1] if lines else ""


def parser():
    top = argparse.ArgumentParser(prog="pairtask classifier", description=__doc__.splitlines()[0])
    actions = top.add_subparsers(dest="action", required=True)
    on = actions.add_parser("on", help="store the key and run the broker in shadow mode")
    on.add_argument("--provider", choices=PROVIDERS, default="jev", help="jev (TypeSafe) or clef (Cloudflare Workers AI)")
    on.add_argument("--account", default="", help="the Cloudflare account ID, for clef")
    on.add_argument("--key-file", help="read the key from this file instead of prompting")
    actions.add_parser("off", help="remove the key and the settings")
    show = actions.add_parser("status", help="the settings (never the key) and what was recorded lately")
    show.add_argument("--since", default="24h", help="how far back the report looks (default 24h)")
    show.add_argument("--json", action="store_true")
    for each in (on, show, actions.choices["off"]):
        each.add_argument("--project", default="pairtask")
        each.add_argument("--remote", default=os.environ.get("PAIRTASK_REMOTE", "pairtask"))
    return top


def main(argv=None):
    sandbox_host_user.refuse_root("pairtask classifier")
    args = parser().parse_args(argv)
    secured = Secured(args.project, args.remote)
    try:
        if args.action == "on":
            turn_on(secured, args, read_key(args))
        elif args.action == "off":
            turn_off(secured)
        else:
            status(secured, args)
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        sys.exit("pairtask classifier: %s" % error)
    return 0


if __name__ == "__main__":
    sys.exit(main())
