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
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
if HERE not in sys.path:
    sys.path.insert(0, HERE)

import sandbox_host_user  # noqa: E402
import sandbox_pairing  # noqa: E402,F401  (the route check's module, patched by tests)
import sandbox_remote  # noqa: E402
from sandbox_secured import (SETUP, Secured, failure, identifier, pairing_route_on, plain_name,  # noqa: E402
                             route_needed, secret_from_file, secret_from_prompt, settings_of, valid_secret)

SECURED_OPERATOR = "/run/collab-ai-operator/operator.sock"
PROVIDERS = ("jev", "clef")
ACCOUNT_MAX = 64


def valid_account(account):
    """A Cloudflare account ID."""
    return plain_name(account, ACCOUNT_MAX)


def read_key(args, stdin=sys.stdin, prompt=getpass.getpass):
    """The key from --key-file, else from a hidden prompt on a terminal."""
    if args.key_file:
        key = secret_from_file(args.key_file, "--key-file")
    else:
        key = secret_from_prompt("API key for %s (not echoed): " % args.provider, stdin, prompt, "--key-file")
    if not valid_secret(key):
        raise ValueError("the key must be one line of printable ASCII, at most 512 bytes")
    return key


ROUTE_NEEDED = route_needed("the provider")


def check_on(args):
    """The provider and account go together: clef needs one, jev takes none."""
    wants, has = args.provider == "clef", bool(args.account)
    if wants != has:
        raise ValueError("clef needs --account ID (the Cloudflare account)" if wants else "--account applies to clef only")
    if has and not valid_account(args.account):
        raise ValueError("--account must be letters, digits, - or _, at most %d characters" % ACCOUNT_MAX)


def turn_on(secured, args, key):
    check_on(args)
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
    """The settings (never the key), then the broker's report; --json gives both as one object."""
    result = secured.execute(SETUP, "classifier", "status")
    if result.returncode != 0:
        raise ValueError(failure("reading the classifier settings", result))
    settings = settings_of(result)
    enabled = settings.get("classifier", "off") != "off"
    report = report_of(secured, args) if enabled else None
    if args.json:
        print(json.dumps({"classifier": settings, "report": report}, sort_keys=True))
    elif not enabled:
        print("Classifier: off")
    else:
        print(describe(settings))
        print(report if isinstance(report, str) else "Report unavailable: " + report.get("error", ""))


def describe(settings):
    """One line of the settings: mode, provider, account, whether a key is stored."""
    account = " (account %s)" % settings["account"] if settings.get("account") else ""
    return "Classifier: %s, %s%s, key %s" % (settings.get("classifier"), settings.get("provider"), account, settings.get("key", "missing"))


def report_of(secured, args):
    """The broker's report: its text, or for --json the decoded object; an
    error is a {"error": …} object either way."""
    extra = ["--json"] if args.json else []
    result = secured.execute("pairtask", "operator", "classifier", "report", "--socket", SECURED_OPERATOR,
                             "--since", args.since, *extra)
    if result.returncode != 0:
        return {"error": failure("the report", result)}
    if not args.json:
        return result.stdout.rstrip("\n")
    try:
        return json.loads(result.stdout)
    except ValueError:
        return {"error": "report was not JSON"}


def parser():
    top = argparse.ArgumentParser(prog="pairtask classifier", description=__doc__.splitlines()[0])
    actions = top.add_subparsers(dest="action", required=True)
    on = actions.add_parser("on", help="store the key and run the broker in shadow mode")
    on.add_argument("--provider", choices=PROVIDERS, default="jev", help="jev (TypeSafe) or clef (Cloudflare Workers AI)")
    on.add_argument("--account", default="", help="the Cloudflare account ID, for clef")
    on.add_argument("--key-file", help="read the key from this file instead of prompting")
    on.add_argument("--state-dir", help="the deployment's provisioning directory (default: the registry's)")
    actions.add_parser("off", help="remove the key and the settings")
    show = actions.add_parser("status", help="the settings (never the key) and what was recorded lately")
    show.add_argument("--since", default="24h", help="how far back the report looks (default 24h)")
    show.add_argument("--json", action="store_true")
    for each in (on, show, actions.choices["off"]):
        each.add_argument("--project", default="pairtask", type=identifier)
        each.add_argument("--remote", default=sandbox_remote.default_remote(), type=identifier)
    return top


def main(argv=None, route=pairing_route_on):
    if sandbox_host_user.refuse_root("pairtask classifier"):
        return 1
    args = parser().parse_args(argv)
    secured = Secured(args.project, args.remote)
    try:
        if args.action == "on":
            check_on(args)  # before the prompt: nothing is asked for that could not be used
            if not route(args):
                raise ValueError(ROUTE_NEEDED)
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
