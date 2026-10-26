#!/usr/bin/env python3
"""Disposable topology probe for peer workspaces (#178), check 1.

Provisions a throwaway project with secured, workspace and an online
connector, pushes a freshly built peerprobe binary into secured and the
connector, starts the TLS gate in secured and the opaque relay in the
connector, sends a contact request over the real bridge from this host, and
runs the independent assertions in peer_probe_checks. It never touches an
operator sandbox and always destroys what it created.

The bridge here is a disposable test network on this host. It proves the
container boundary and the IPC path, not reachability over a real LAN or
tailnet; that stays an environment validation on real machines.
"""

import argparse
import importlib.util
import json
import os
from pathlib import Path
import queue
import shutil
import subprocess
import sys
import tempfile
import threading
import time
import uuid

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT / "scripts"))

import peer_probe_checks  # noqa: E402
import sandbox_network_checks  # noqa: E402

PEER_DIR = peer_probe_checks.PEER_DIR
PEER_SOCKET = peer_probe_checks.PEER_SOCKET
PROBE_BIN = "/usr/local/bin/peerprobe"
PEER_GID = 1050          # collab-peer, the same number in both containers
GATE_UID = 1051          # peergate, in secured
CONNECTOR_UID = 1052     # peer, in the connector
RELAY_PORT = 7444


def load_smoke():
    """sandbox-smoke.py has a hyphen; load it for its state helpers."""
    spec = importlib.util.spec_from_file_location("sandbox_smoke", ROOT / "scripts" / "sandbox-smoke.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def build_probe(directory):
    """Build peerprobe for this runner's architecture; nothing is installed."""
    output = directory / "peerprobe"
    env = dict(os.environ, CGO_ENABLED="0")
    run(["go", "build", "-o", str(output), "./cmd/peerprobe"], cwd=ROOT, env=env)
    return output


def enable_probe(directory):
    path = directory / "sandbox.auto.tfvars.json"
    variables = json.loads(path.read_text())
    variables.update({"secured_runtime": True, "peer_connector_probe": True})
    path.write_text(json.dumps(variables))


class Deployment:
    def __init__(self, remote, project):
        self.remote = remote.rstrip(":") + ":"
        self.project = project
        self.base = ["incus", "--project", project]
        self.processes = []

    def execute(self, role, *command, uid=0, gid=0):
        return self.base + ["exec", self.remote + role, "-T", "--user", str(uid), "--group", str(gid), "--", *command]

    def sh(self, role, script):
        run(self.execute(role, "sh", "-euc", script))

    def push(self, binary, role):
        run(self.base + ["file", "push", str(binary), self.remote + role + PROBE_BIN, "--mode", "0755"])

    def start(self, role, *command, uid, gid):
        """Start a long-running process; stdout lines go to a queue, stderr is
        drained continuously so a chatty process can't block on its pipe."""
        process = subprocess.Popen(self.execute(role, *command, uid=uid, gid=gid),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        lines = queue.Queue()
        process.stderr_lines = []

        def pump_out():
            for line in process.stdout:
                lines.put(line)

        def pump_err():
            for line in process.stderr:
                process.stderr_lines.append(line)
                del process.stderr_lines[:-200]  # keep the tail for error reports
        threading.Thread(target=pump_out, daemon=True).start()
        threading.Thread(target=pump_err, daemon=True).start()
        self.processes.append(process)
        return process, lines

    def stop_all(self):
        for process in self.processes:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()

    def address(self, role, timeout=60):
        """The instance's bridge IPv4 address, waiting for its DHCP lease."""
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            address = self.lease(role)
            if address:
                return address
            time.sleep(2)
        raise ValueError(f"{role} got no IPv4 address on its bridge within {timeout}s")

    def lease(self, role):
        """The instance's global IPv4 address on eth0 now, or None."""
        data = json.loads(run(self.base + ["list", self.remote, "--format", "json"],
                              capture_output=True, text=True).stdout)
        instance = next((i for i in data if i["name"] == role), {})
        network = (instance.get("state") or {}).get("network") or {}
        addresses = (network.get("eth0") or {}).get("addresses", [])
        return next((a["address"] for a in addresses if a["family"] == "inet" and a["scope"] == "global"), None)


def await_event(lines, name, process, timeout=30):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if process.poll() is not None:
            raise ValueError(f"process exited before {name}: {''.join(process.stderr_lines)}")
        try:
            line = lines.get(timeout=1)
        except queue.Empty:
            continue
        try:
            record = json.loads(line)
        except json.JSONDecodeError:
            continue
        if record.get("event") == name:
            return record
    raise ValueError(f"timed out waiting for {name}: {''.join(process.stderr_lines[-20:])}")


def prepare_users(deployment):
    """Fixture users, created for this run only; nothing is baked into images."""
    deployment.sh("secured", f"""
groupadd --gid {PEER_GID} collab-peer
useradd --system --uid {GATE_UID} --gid collab-peer --no-create-home --shell /usr/sbin/nologin peergate
install -d -o peergate -g collab-peer -m 0750 {PEER_DIR}
install -d -o peergate -g collab-peer -m 0700 /var/lib/peerprobe
""")
    deployment.sh("connector", f"""
groupadd --gid {PEER_GID} collab-peer
useradd --system --uid {CONNECTOR_UID} --gid collab-peer --no-create-home --shell /usr/sbin/nologin peer
""")


class Client:
    """The asking side, on this host: one identity kept under its state
    directory, and the gate's exact certificate as its only trust."""

    def __init__(self, binary, workdir, gate_cert_pem):
        self.binary = binary
        self.state = workdir / "client"
        self.server_cert = workdir / "gate.crt"
        self.server_cert.write_text(gate_cert_pem)

    def send(self, address, request_id):
        result = subprocess.run([str(self.binary), "send", "--addr", f"{address}:{RELAY_PORT}",
                                 "--server-cert", str(self.server_cert), "--state", str(self.state),
                                 "--request-id", request_id, "--note", "probe"],
                                capture_output=True, text=True, timeout=30)
        if result.returncode not in (0, 2):
            raise ValueError(f"send failed ({result.returncode}): {result.stderr}")
        return json.loads(result.stdout.strip().splitlines()[-1])


def exercise(args, deployment, binary, workdir):
    prepare_users(deployment)
    for role in ("secured", "connector"):
        deployment.push(binary, role)
    gate, gate_lines = deployment.start("secured", PROBE_BIN, "gate", "--socket", PEER_SOCKET,
                                        "--state", "/var/lib/peerprobe", uid=GATE_UID, gid=PEER_GID)
    ready = await_event(gate_lines, "gate_ready", gate)
    relay, relay_lines = deployment.start("connector", PROBE_BIN, "relay", "--listen", f"0.0.0.0:{RELAY_PORT}",
                                          "--dial", PEER_SOCKET, uid=CONNECTOR_UID, gid=PEER_GID)
    await_event(relay_lines, "relay_ready", relay)
    address = deployment.address("connector")
    client = Client(binary, workdir, ready["cert_pem"])
    first = client.send(address, "probe-1")
    if first["status"] != "pending":
        raise ValueError(f"first contact request not pending: {first}")
    again = client.send(address, "probe-1")
    if again["status"] != "duplicate":
        raise ValueError(f"a retry with the same identity was not recognised: {again}")
    print("PASS: contact request through bridge → connector relay → peer socket → TLS gate in secured; retry recognised.",
          flush=True)
    peer_probe_checks.check_uid(await_event(gate_lines, "peer_credentials", gate))
    peer_probe_checks.check_topology(args.remote, deployment.project)
    peer_probe_checks.check_mounts(args.remote, deployment.project)
    peer_probe_checks.check_connect(args.remote, deployment.project)


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
    workdir = Path(tempfile.mkdtemp(prefix="pairtask-peer-probe-"))
    directory = None
    initialized = False
    deployment = Deployment(args.remote, project)
    try:
        binary = build_probe(workdir)  # before any Incus state exists
        directory = Path(tempfile.mkdtemp(prefix="pairtask-smoke-"))
        args.network_check = True  # a disposable bridge owned by this state
        smoke.prepare_state(directory, args, project)
        enable_probe(directory)
        tofu = [args.tofu, f"-chdir={directory}"]
        run(tofu + ["init", "-input=false", "-lockfile=readonly"])
        initialized = True
        run(tofu + ["apply", "-auto-approve", "-input=false"])
        exercise(args, deployment, binary, workdir)
        print("PASS: peer probe topology checks.", flush=True)
    finally:
        deployment.stop_all()
        shutil.rmtree(workdir, ignore_errors=True)
        if initialized:
            smoke.destroy_deployment(tofu, directory, project)
        elif directory is not None:
            shutil.rmtree(directory, ignore_errors=True)  # nothing was provisioned


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-peer-probe: {error}", file=sys.stderr)
        sys.exit(1)
