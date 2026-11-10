"""What the host commands that set a secret for the secured broker share:
the way into the secured container, the pairing-route check, the secret's
sources and the error wording."""

import argparse
from pathlib import Path
import re
import subprocess

import sandbox_pairing

SETUP = "/usr/local/lib/pairtask/pairtask-broker-setup"
SECRET_MAX = 512
TIMEOUT = 120


def plain_name(value, limit):
    """ASCII letters, digits, - and _, between 1 and limit characters."""
    return 0 < len(value) <= limit and value.isascii() and all(ch.isalnum() or ch in "-_" for ch in value)


def valid_secret(secret):
    """One line of printable ASCII, at most SECRET_MAX bytes."""
    return 0 < len(secret) <= SECRET_MAX and all(32 < ord(ch) < 127 for ch in secret)


# A Slack incoming webhook URL, the one shape the broker accepts: checked
# before anything leaves the host.
SLACK_WEBHOOK = re.compile(r"https://hooks\.slack\.com/services/[A-Za-z0-9_-]+(/[A-Za-z0-9_-]+)*")


def valid_webhook(secret):
    return valid_secret(secret) and SLACK_WEBHOOK.fullmatch(secret) is not None


class Secured:
    """Commands in the secured container, through incus exec."""

    def __init__(self, project, remote, run=subprocess.run):
        self.base = ["incus", "--project", project, "exec", remote.rstrip(":") + ":secured", "-T", "--"]
        self.run = run

    def execute(self, *command, stdin=None, timeout=TIMEOUT):
        return self.run(self.base + list(command), input=stdin, capture_output=True, text=True, timeout=timeout, check=False)


def secret_from_file(name, flag):
    """A regular file readable by its owner only; the copy the broker gets is just as private."""
    path = Path(name)
    if path.is_symlink() or not path.is_file():
        raise ValueError(flag + " must be a regular file")
    if path.stat().st_mode & 0o077:
        raise ValueError(flag + " must be readable by you only (chmod 600)")
    return path.read_text().strip()


def secret_from_prompt(question, stdin, prompt, flag):
    if not stdin.isatty():
        raise ValueError("no terminal for the prompt; use " + flag + " PATH")
    return prompt(question).strip()


def pairing_route_on(args, registry=None):
    """Whether the deployment runs the pairing route, which gives the secured
    broker its way out: the connector in the deployed state where there is
    one, else the saved pairing settings (whichever variables file holds
    them). The deployment is found as `pairtask pairing on` finds it
    (--state-dir, the registry, else a worktree's state); nothing is created."""
    try:
        directory = sandbox_pairing.find_deployment(Path(args.state_dir) if args.state_dir else None, args.project,
                                                    location=registry or sandbox_pairing.LOCATION)
        if directory is None:
            return False
        deployed = sandbox_pairing.applied(directory, args.project)
        return bool(deployed) if deployed is not None else bool(sandbox_pairing.configured(directory))
    except (OSError, ValueError):
        return False


def route_needed(target):
    return ("the pairing route is off: the sandbox broker has no network of its own and reaches %s "
            "through the connector that `pairtask pairing on` provides; run that first, then this again" % target)


def failure(what, result):
    return "%s failed: %s" % (what, last_line(result.stderr) or "exit %d" % result.returncode)


def last_line(text):
    lines = [line for line in text.strip().splitlines() if line.strip()]
    return lines[-1] if lines else ""


def identifier(value):
    """An Incus project or remote name: letters, digits, - and _, bounded."""
    if not plain_name(value, 64):
        raise argparse.ArgumentTypeError("must be letters, digits, - or _")
    return value


def settings_of(result):
    """The KEY=VALUE lines a status command printed."""
    return dict(line.split("=", 1) for line in result.stdout.splitlines() if "=" in line)
