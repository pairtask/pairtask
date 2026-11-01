"""The pairing name's barrier (#277): the secured broker starts only once
the host has seen the name file provisioning wrote and marked it ready.

The Incus provider starts secured before it uploads files, so a broker that
started at once could pair under an old or missing name. Instead, before an
apply that starts secured, the host removes the old marker; after it, the
host reads the file the provider wrote, checks it holds the name asked for,
marks those exact bytes ready, and restarts the route's setup and the broker.
Until then, setup refuses and the broker doesn't run. Only pairtask's own
rollout does this: a direct tofu apply doesn't.
"""

import hashlib
import json
import subprocess
import time
import uuid

INSTANCE = "secured"
DIRECTORY = "/etc/pairtask"
NAME_FILE = DIRECTORY + "/pairing-name.json"
READY = "pairing-name.ready"
SETUP = "/usr/local/lib/pairtask/pairtask-pairing-setup"
UNITS = ("pairtask-pairing-setup.service", "pairtask-secured-broker.service")
OPERATOR_SOCKET = "/run/collab-ai-operator/operator.sock"
DEFAULT_NAME = "workspace"
WAIT = 120


class NameBarrier:
    """One deployment's barrier, for the name it asked for ("" for none)."""

    def __init__(self, project, remote, name, run=subprocess.run, clock=time.monotonic, sleep=time.sleep):
        self.project, self.remote, self.name = project, remote.rstrip(":") + ":", name
        self.run, self.clock, self.sleep = run, clock, sleep

    def incus(self, *args, stdin=None):
        result = self.run(["incus", "--project", self.project, *args], input=stdin, capture_output=True, check=False)
        if result.returncode != 0:
            raise ValueError(f"incus {args[0]} failed: {decoded(result.stderr).strip()}")
        return result.stdout

    def status(self):
        """secured's state, or None when there is no such instance."""
        for item in json.loads(self.incus("list", self.remote, "--format=json")):
            if item["name"] == INSTANCE:
                return item["status"]
        return None

    def invalidate(self):
        """Before an apply: drop the marker of a stopped secured, so it can't
        start under the old name; a missing instance or marker has none."""
        status = self.status()
        if status is None:
            return
        if status != "Stopped":
            raise ValueError(f"{INSTANCE} is {status}; stop it before changing its pairing name")
        if READY in self.listing(DIRECTORY):
            self.query("DELETE", DIRECTORY + "/" + READY)

    def listing(self, path):
        """The names in a directory of secured; none when it doesn't exist,
        which its parent's listing tells, never an error's text."""
        parent, base = path.rsplit("/", 1)
        if base not in json.loads(self.query("GET", parent or "/")):
            return []
        return json.loads(self.query("GET", path))

    def query(self, method, path):
        url = f"{self.remote}/1.0/instances/{INSTANCE}/files?path={path}&project={self.project}"
        return self.incus("query", "--request", method, url)

    def release(self):
        """After an apply: mark the name file ready once it holds the name
        asked for, then start setup and the broker with it. secured must be
        running: nothing is marked or restarted otherwise."""
        status = self.status()
        if status != "Running":
            raise ValueError(f"{INSTANCE} is {status or 'absent'}; start it before releasing its pairing name")
        data = self.incus("file", "pull", f"{self.remote}{INSTANCE}{NAME_FILE}", "-")
        if parsed_name(data) != self.name:
            raise ValueError(f"{NAME_FILE} doesn't hold the requested pairing name; nothing released")
        self.publish(hashlib.sha256(data).hexdigest())
        self.incus("exec", f"{self.remote}{INSTANCE}", "--", "systemctl", "restart", *UNITS)
        self.await_name()

    def publish(self, digest):
        """The marker, pushed under a unique name and renamed into place in
        secured, where a refused one is removed."""
        temporary = f"{DIRECTORY}/.{READY}.{uuid.uuid4().hex}"
        self.incus("file", "push", "--uid", "0", "--gid", "0", "--mode", "0600", "-", f"{self.remote}{INSTANCE}{temporary}",
                   stdin=(digest + "\n").encode())
        try:
            self.incus("exec", f"{self.remote}{INSTANCE}", "--", SETUP, "publish-ready", temporary)
        except BaseException:
            self.discard(temporary)
            raise

    def discard(self, temporary):
        """Best effort: a pushed marker that wasn't published goes; its
        removal failing never hides why publishing failed."""
        try:
            self.query("DELETE", temporary)
        except (OSError, ValueError):
            pass

    def await_name(self):
        """Both units active, and the broker pairing under the name."""
        deadline = self.clock() + WAIT
        while not self.serving():
            if self.clock() >= deadline:
                raise ValueError(f"{INSTANCE} didn't start its broker under the released pairing name")
            self.sleep(1)

    def serving(self):
        for unit in UNITS:
            if self.run(["incus", "--project", self.project, "exec", f"{self.remote}{INSTANCE}", "--",
                         "systemctl", "is-active", "--quiet", unit], capture_output=True, check=False).returncode != 0:
                return False
        status = self.run(["incus", "--project", self.project, "exec", f"{self.remote}{INSTANCE}", "--",
                           "pairtask", "operator", "pair", "status", "--json", "--socket", OPERATOR_SOCKET],
                          capture_output=True, check=False)
        if status.returncode != 0:
            return False
        return json.loads(status.stdout)["pairing_result"].get("self_name") == (self.name or DEFAULT_NAME)


def parsed_name(data):
    """The name in a name file, or None when it holds none."""
    try:
        parsed = json.loads(decoded(data))
    except ValueError:
        return None
    name = parsed.get("name") if isinstance(parsed, dict) else None
    return name if isinstance(name, str) else None


def decoded(data):
    return data.decode("utf-8", "replace") if isinstance(data, bytes) else data
