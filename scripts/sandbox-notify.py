#!/usr/bin/env python3
"""pairtask notify on | off | status: tell you in Slack when an agent needs
a decision (#352), from the host, without environment variables.

`on --slack` reads a Slack incoming webhook URL from a hidden prompt or
--secret-file, never from the command line, and hands it on stdin to the
secured container's pairtask-broker-setup, which stores it for the broker
alone and restarts the broker with notifications on. Slack gets one line
per request: the workspace, the agent and its one-line summary, or with
--hidden only that a decision is waiting. The answer is always given in the
pairtask chat; the notice is a pointer, not an approval. `off` removes the
URL and the settings and restarts the broker. `status` shows the settings,
never the URL.
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
from sandbox_secured import (SETUP, Secured, failure, identifier, pairing_route_on, route_needed,  # noqa: E402
                             secret_from_file, secret_from_prompt, settings_of, valid_webhook)

ROUTE_NEEDED = route_needed("Slack")
SHAPE = "the secret must be a Slack incoming webhook URL (https://hooks.slack.com/services/...), one line"


def read_secret(args, stdin=sys.stdin, prompt=getpass.getpass):
    """The webhook URL from --secret-file, else from a hidden prompt on a terminal."""
    if args.secret_file:
        secret = secret_from_file(args.secret_file, "--secret-file")
    else:
        secret = secret_from_prompt("Slack incoming webhook URL (not echoed): ", stdin, prompt, "--secret-file")
    if not valid_webhook(secret):
        raise ValueError(SHAPE)
    return secret


def turn_on(secured, args, secret):
    command = [SETUP, "notify", "on", "slack"] + (["--hidden"] if args.hidden else [])
    result = secured.execute(*command, stdin=secret + "\n")
    if result.returncode != 0:
        raise ValueError(failure("turning notifications on", result))
    print("notify on: slack%s; agents' decision requests are posted there, answers stay in the pairtask chat"
          % (", hidden (only that a decision is waiting)" if args.hidden else ""))


def turn_off(secured):
    result = secured.execute(SETUP, "notify", "off")
    if result.returncode != 0:
        raise ValueError(failure("turning notifications off", result))
    print("notify off")


def status(secured, args):
    """The settings, never the URL; --json gives them as one object."""
    result = secured.execute(SETUP, "notify", "status")
    if result.returncode != 0:
        raise ValueError(failure("reading the notification settings", result))
    settings = settings_of(result)
    if args.json:
        print(json.dumps({"notify": settings}, sort_keys=True))
        return
    if settings.get("notify", "off") == "off":
        print("Notifications: off")
        return
    hidden = ", hidden (only that a decision is waiting)" if settings.get("hidden") == "1" else ""
    print("Notifications: %s%s, secret %s" % (settings.get("notify"), hidden, settings.get("secret", "missing")))


def parser():
    top = argparse.ArgumentParser(prog="pairtask notify", description=__doc__.splitlines()[0])
    actions = top.add_subparsers(dest="action", required=True)
    on = actions.add_parser("on", help="store the webhook URL and run the broker with notifications on")
    on.add_argument("--slack", action="store_true", required=True, help="Slack, through an incoming webhook (the only kind)")
    on.add_argument("--hidden", action="store_true", help="say only that a decision is waiting, not who asks what")
    on.add_argument("--secret-file", help="read the webhook URL from this file instead of prompting")
    on.add_argument("--state-dir", help="the deployment's provisioning directory (default: the registry's)")
    actions.add_parser("off", help="remove the webhook URL and the settings")
    show = actions.add_parser("status", help="the settings (never the URL)")
    show.add_argument("--json", action="store_true")
    for each in (on, show, actions.choices["off"]):
        each.add_argument("--project", default="pairtask", type=identifier)
        each.add_argument("--remote", default=sandbox_remote.default_remote(), type=identifier)
    return top


def main(argv=None, route=pairing_route_on):
    if sandbox_host_user.refuse_root("pairtask notify"):
        return 1
    args = parser().parse_args(argv)
    secured = Secured(args.project, args.remote)
    try:
        if args.action == "on":
            if not route(args):
                raise ValueError(ROUTE_NEEDED)
            turn_on(secured, args, read_secret(args))
        elif args.action == "off":
            turn_off(secured)
        else:
            status(secured, args)
    except (ValueError, OSError, subprocess.TimeoutExpired) as error:
        sys.exit("pairtask notify: %s" % error)
    return 0


if __name__ == "__main__":
    sys.exit(main())
