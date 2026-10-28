#!/usr/bin/env python3
"""Disposable proof of the installed pairing route (#178).

Provisions a throwaway project with secured, workspace and the route's
connector from the built image, lets the image's own units start, and runs
real redemptions both ways against a standalone broker on this host:

- outbound: secured's operator redeems the host's code, through connector's
  egress forwarder;
- inbound: the host redeems secured's code, through connector's relay into
  secured's shared pairing socket.

It then runs pairing_route_checks on the deployment and checks that neither
code reached a journal or log. It never touches an operator sandbox and
always destroys what it created. The bridge is a disposable test network on
this host: it proves the container boundary and the installed route, not
reachability between real home and work machines.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import socket
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import pairing_route_checks  # noqa: E402

SECURED_OPERATOR = "/run/collab-ai-operator/operator.sock"
SECURED_DB = "/var/lib/collab-ai-secured/broker/broker.db"
JOIN_QUERY = ("SELECT operation_id, invitation_id, inviter_operation_id, peer, joiner, hex(transcript_id), state "
              "FROM peer_joins WHERE operation_id = ?")
OUTCOME_QUERY = "SELECT operation_id, invitation_id, identity_key, hex(transcript_id) FROM peer_pairing_outcomes WHERE operation_id = ?"
READ_ROW = """import json, sqlite3, sys
db = sqlite3.connect('file:' + sys.argv[1] + '?mode=ro', uri=True)
row = db.execute(sys.argv[2], (sys.argv[3],)).fetchone()
print(json.dumps(row))"""
RELAY_PORT = 7445
WAIT = "40s"


def load_smoke():
    """sandbox-smoke.py has a hyphen; load it for its state helpers."""
    spec = importlib.util.spec_from_file_location("sandbox_smoke", ROOT / "scripts" / "sandbox-smoke.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def require(condition, message):
    if not condition:
        raise ValueError(message)


def build(directory):
    """The host fixture's broker and CLI; the containers use the image's."""
    env = dict(os.environ, CGO_ENABLED="0")
    binaries = {}
    for name, package in (("broker", "./cmd/broker"), ("pairtask", "./cmd/collab")):
        binaries[name] = directory / name
        run(["go", "build", "-o", str(binaries[name]), package], cwd=ROOT, env=env)
    return binaries


def enable_route(directory):
    path = directory / "sandbox.auto.tfvars.json"
    variables = json.loads(path.read_text())
    variables.update({"secured_runtime": True, "pairing_route": True})
    path.write_text(json.dumps(variables))


class Deployment:
    def __init__(self, remote, project):
        self.remote = remote.rstrip(":") + ":"
        self.base = ["incus", "--project", project]

    def execute(self, role, *command, stdin=None):
        result = subprocess.run(self.base + ["exec", self.remote + role, "-T", "--", *command],
                                input=stdin, capture_output=True, text=True, timeout=120)
        return result

    def output(self, role, *command, stdin=None):
        result = self.execute(role, *command, stdin=stdin)
        require(result.returncode == 0, f"{role}: {command[0]} failed: {result.stderr}")
        return result.stdout

    def lease(self, role):
        data = json.loads(run(self.base + ["list", self.remote, "--format", "json"], capture_output=True, text=True).stdout)
        instance = next((i for i in data if i["name"] == role), {})
        addresses = (((instance.get("state") or {}).get("network") or {}).get("eth0") or {}).get("addresses", [])
        return next((a["address"] for a in addresses if a["family"] == "inet" and a["scope"] == "global"), None)

    def address(self, role, timeout=60):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            address = self.lease(role)
            if address:
                return address
            time.sleep(2)
        raise ValueError(f"{role} got no IPv4 address within {timeout}s")

    def await_active(self, role, unit, timeout=90):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self.execute(role, "systemctl", "is-active", "--quiet", unit).returncode == 0:
                return
            time.sleep(2)
        status = self.execute(role, "systemctl", "status", "--no-pager", unit).stdout
        raise ValueError(f"{role}: {unit} did not become active:\n{status}")

    def row(self, query, key):
        """One row of secured's database, read-only, as root."""
        return json.loads(self.output("secured", "python3", "-c", READ_ROW, SECURED_DB, query, key))

    def main_pid(self, role, unit):
        return self.output(role, "systemctl", "show", "--property=MainPID", "--value", unit).strip()

    def crash(self, role, unit):
        """Kills a unit's process so it leaves its socket behind, then waits
        for systemd to restart it on its own."""
        before = self.main_pid(role, unit)
        self.output(role, "systemctl", "kill", "--signal=SIGKILL", unit)
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            pid = self.main_pid(role, unit)
            if pid not in ("0", before) and self.execute(role, "systemctl", "is-active", "--quiet", unit).returncode == 0:
                return
            time.sleep(2)
        raise ValueError(f"{role}: {unit} did not come back after a crash")

    def operator(self, *args, stdin=None):
        """secured's own CLI against its operator socket, as root."""
        return self.execute("secured", "pairtask", "operator", "pair", *args, "--socket", SECURED_OPERATOR, stdin=stdin)


class HostBroker:
    """A standalone broker on this host: a private pairing socket behind a
    relay on the bridge address, and direct egress."""

    def __init__(self, binaries, workdir, bridge_ip):
        self.binaries, self.workdir = binaries, workdir
        self.operator_socket = str(workdir / "operator.sock")
        self.processes = []
        private = workdir / "pairing"
        private.mkdir(mode=0o700)
        self.pairing_socket = str(private / "pairing.sock")
        self.log = open(workdir / "host-broker.log", "w")
        try:
            self.start(bridge_ip)
        except BaseException:
            self.stop()  # nothing started here outlives a failed startup
            raise

    def start(self, bridge_ip):
        binaries, workdir = self.binaries, self.workdir
        env = dict(os.environ, PAIRTASK_SOCKET_PATH=str(workdir / "agent.sock"), PAIRTASK_DB_PATH=str(workdir / "host.db"),
                   PAIRTASK_OPERATOR_SOCKET_PATH=self.operator_socket, PAIRTASK_OPERATOR_UID=str(os.getuid()),
                   PAIRTASK_PAIRING_SOCKET=self.pairing_socket, PAIRTASK_PAIRING_EGRESS="direct",
                   PAIRTASK_PAIRING_NAME="host-fixture")
        self.processes.append(subprocess.Popen([str(binaries["broker"])], env=env, stdout=self.log, stderr=self.log))
        wait_for_socket(self.pairing_socket)
        self.relay_address = f"{bridge_ip}:{free_port(bridge_ip)}"
        self.processes.append(subprocess.Popen([str(binaries["pairtask"]), "peer", "relay", "--listen", self.relay_address,
                                                "--dial-unix", self.pairing_socket], stdout=self.log, stderr=self.log))
        wait_for_tcp(self.relay_address)

    def row(self, query, key):
        with sqlite3.connect(f"file:{self.workdir / 'host.db'}?mode=ro", uri=True) as db:
            return db.execute(query, (key,)).fetchone()

    def pair(self, *args, stdin=None):
        return subprocess.run([str(self.binaries["pairtask"]), "operator", "pair", *args, "--socket", self.operator_socket],
                              input=stdin, capture_output=True, text=True, timeout=120)

    def stop(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=10)
        self.log.close()


def wait_for_socket(path, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if os.path.exists(path):
            return
        time.sleep(0.2)
    raise ValueError(f"{path} never appeared")


def free_port(host):
    with socket.socket() as probe:
        probe.bind((host, 0))
        return probe.getsockname()[1]


def wait_for_tcp(address, timeout=30):
    host, port = address.rsplit(":", 1)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            socket.create_connection((host, int(port)), timeout=1).close()
            return
        except OSError:
            time.sleep(0.2)
    raise ValueError(f"{address} never accepted connections")


def opened_code(result):
    require(result.returncode == 0, f"pair open failed: {result.stderr}")
    snapshot = json.loads(result.stdout)["pairing_result"]
    return snapshot["invitation"]["code"], snapshot["self_identity"]


def snapshot(result):
    require(result.returncode == 0, f"pair status failed: {result.stderr}")
    return json.loads(result.stdout)["pairing_result"]


def require_pending(snap, identity, side):
    keys = {r["identity_key"]: r["state"] for r in snap["requests"]}
    require(keys.get(identity) == "pending_acceptance", f"{side} has no pending request from {identity}: {keys}")


def require_receipt(join, outcome, joiner):
    """The joiner's durable join and the inviter's durable outcome name the
    same invitation, operation, identity and transcript."""
    require(join is not None and outcome is not None, "a durable join or outcome is missing")
    _, invitation, inviter_op, _, join_joiner, transcript, state = join
    require(state == "awaiting_acceptance", f"the stored join is {state}")
    require((outcome[0], outcome[1], outcome[2], outcome[3]) == (inviter_op, invitation, joiner, transcript),
            f"the join and the inviter's outcome disagree: {join} / {outcome}")
    require(join_joiner == joiner, "the join names a different joiner")


def outbound(deployment, host):
    """secured redeems the host's code through connector's egress."""
    code, host_identity = opened_code(host.pair("open", "--json"))
    result = deployment.operator("redeem", host.relay_address, "--name", "secured", "--wait", WAIT, "--json", stdin=code + "\n")
    require(result.returncode == 0, f"secured's redemption did not settle: {result.stdout} {result.stderr}")
    join = json.loads(result.stdout)
    require(join["state"] == "awaiting_acceptance", f"secured's redemption is {join['state']}")
    require(join["peer"] == host_identity, "secured authenticated a different inviter")
    secured = snapshot(deployment.operator("status", "--json"))
    require_pending(snapshot(host.pair("status", "--json")), secured["self_identity"], "the host")
    stored = deployment.row(JOIN_QUERY, join["operation_id"])
    require_receipt(stored, host.row(OUTCOME_QUERY, stored[2]), secured["self_identity"])
    print("PASS: secured redeemed through connector's egress; the host holds its pending request and matching outcome.", flush=True)
    return code


def inbound(deployment, host, connector_ip):
    """The host redeems secured's code through connector's relay."""
    code, secured_identity = opened_code(deployment.operator("open", "--json"))
    result = host.pair("redeem", f"{connector_ip}:{RELAY_PORT}", "--wait", WAIT, "--json", stdin=code + "\n")
    require(result.returncode == 0, f"the host's redemption did not settle: {result.stdout} {result.stderr}")
    join = json.loads(result.stdout)
    require(join["state"] == "awaiting_acceptance", f"the host's redemption is {join['state']}")
    require(join["peer"] == secured_identity, "the host authenticated a different inviter")
    host_identity = snapshot(host.pair("status", "--json"))["self_identity"]
    require_pending(snapshot(deployment.operator("status", "--json")), host_identity, "secured")
    stored = host.row(JOIN_QUERY, join["operation_id"])
    require_receipt(stored, deployment.row(OUTCOME_QUERY, stored[2]), host_identity)
    print("PASS: the host redeemed through connector's relay into secured; secured holds its pending request and matching outcome.", flush=True)
    return code


def codes_absent(deployment, host, codes):
    """Neither code, grouped or bare, is in any journal or the host's log."""
    needles = set()
    for code in codes:
        needles.update({code, code.replace("-", "")})
    for role in ("secured", "connector"):
        journal = deployment.output(role, "journalctl", "--no-pager", "-o", "cat")
        require(not any(n in journal for n in needles), f"a pairing code reached {role}'s journal")
    log = (host.workdir / "host-broker.log").read_text()
    require(not any(n in log for n in needles), "a pairing code reached the host fixture's log")
    print("PASS: no pairing code in either journal or the host's log.", flush=True)


def exercise(args, deployment, binaries, workdir):
    for role, unit in (("secured", "pairtask-secured-broker.service"), ("connector", "pairtask-peer-egress.service"),
                       ("connector", "pairtask-peer-relay.service")):
        deployment.await_active(role, unit)
    connector_ip = deployment.address("connector")
    bridge_ip = connector_ip.rsplit(".", 1)[0] + ".1"
    host = HostBroker(binaries, workdir, bridge_ip)
    try:
        codes = [outbound(deployment, host), inbound(deployment, host, connector_ip)]
        # Crashes leave the broker's and the forwarder's sockets behind; the
        # installed units must clean their own and serve again.
        deployment.crash("secured", "pairtask-secured-broker.service")
        deployment.crash("connector", "pairtask-peer-egress.service")
        deployment.output("connector", "systemctl", "restart", "pairtask-peer-relay.service")
        deployment.await_active("connector", "pairtask-peer-relay.service")
        codes += [outbound(deployment, host), inbound(deployment, host, connector_ip)]
        print("PASS: both directions again after crashing the broker and the forwarder.", flush=True)
        pairing_route_checks.check_all(args.remote, deployment.base[2])
        codes_absent(deployment, host, codes)
    finally:
        host.stop()


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", default="local")
    parser.add_argument("--image-dir", type=Path, required=True)
    parser.add_argument("--tofu", default="tofu")
    return parser.parse_args()


def main():
    args = parse_args()
    smoke = load_smoke()
    project = "pairtask-smoke-" + uuid.uuid4().hex[:12]
    workdir = Path(tempfile.mkdtemp(prefix="pairtask-pairing-route-"))
    directory = None
    initialized = False
    deployment = Deployment(args.remote, project)
    try:
        binaries = build(workdir)  # before any Incus state exists
        directory = Path(tempfile.mkdtemp(prefix="pairtask-smoke-"))
        args.network_check = True  # a disposable bridge owned by this state
        smoke.prepare_state(directory, args, project)
        enable_route(directory)
        tofu = [args.tofu, f"-chdir={directory}"]
        run(tofu + ["init", "-input=false", "-lockfile=readonly"])
        initialized = True
        run(tofu + ["apply", "-auto-approve", "-input=false"])
        exercise(args, deployment, binaries, workdir)
        print("PASS: pairing route proof.", flush=True)
    finally:
        shutil.rmtree(workdir, ignore_errors=True)
        if initialized:
            smoke.destroy_deployment(tofu, directory, project)
        elif directory is not None:
            shutil.rmtree(directory, ignore_errors=True)  # nothing was provisioned


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-pairing-route: {error}", file=sys.stderr)
        sys.exit(1)
