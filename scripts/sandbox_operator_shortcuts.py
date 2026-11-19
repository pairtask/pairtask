"""Literal one-shot operator commands, executed only in the secured runtime."""

import argparse
import subprocess
import unicodedata


def parser(name, identifier, remote):
    flags = argparse.ArgumentParser(prog="pairtask " + name)
    flags.add_argument("--remote", default=remote, type=identifier)
    flags.add_argument("--project", default="pairtask", type=identifier)
    flags.add_argument("--secured", default="secured", type=identifier)
    return flags


def limit(value):
    number = int(value)
    if not 1 <= number <= 50:
        raise argparse.ArgumentTypeError("limit must be 1 to 50")
    return number


def command(name, rest, identifier, remote):
    flags = parser(name, identifier, remote)
    opts, words = {"send": send, "inbox": inbox}[name](flags, rest)
    return secured(opts.remote, opts.project, opts.secured, words)


def send(flags, rest):
    flags.add_argument("target")
    flags.add_argument("text", nargs=argparse.REMAINDER)
    opts = flags.parse_args(rest)
    if not valid_target(opts.target):
        flags.error("use one @AGENT target or @all")
    text = " ".join(opts.text)
    if not text.strip():
        flags.error("message text is required")
    target = ["--all"] if opts.target == "@all" else ["--to", opts.target[1:]]
    return opts, ["send", *target, "--", text]


def inbox(flags, rest):
    flags.add_argument("--limit", type=limit)
    flags.add_argument("--json", action="store_true")
    opts = flags.parse_args(rest)
    words = ["inbox", "summary"]
    if opts.limit is not None:
        words += ["--limit", str(opts.limit)]
    if opts.json:
        words.append("--json")
    return opts, words


def secured(remote, project, instance, words):
    return ["incus", "--project", project, "exec", remote + ":" + instance,
            "-T", "--", "pairtask", "operator", *words]


def valid_target(target):
    name = target[1:] if target.startswith("@") else ""
    return (0 < len(name.encode()) <= 128 and name not in ("*", "operator", "pairtask")
            and not any(char.isspace() or ord(char) < 32 or char in ",@" for char in name))


def optional(argv):
    """A failed agents report does not hide the other host status sections."""
    try:
        result = subprocess.run(argv, capture_output=True, text=True, timeout=15)
        if result.returncode == 0:
            print(result.stdout, end="")
            return 0
        lines = result.stderr.strip().splitlines()
        reason = lines[-1] if lines else "command exited with status " + str(result.returncode)
    except subprocess.TimeoutExpired:
        reason = "agent status request timed out"
    except OSError as error:
        reason = str(error)
    safe = "".join(char for char in reason if not unicodedata.category(char).startswith("C"))[:240]
    print("agents: unavailable (" + safe + ")")
    return 0
