"""One-shot operator-socket fixture; it never simulates broker adoption."""
import json
import os
from pathlib import Path
import socket
import subprocess
import sys
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[2]


class OperatorSocket:
    def __init__(self, lease):
        self.directory = tempfile.TemporaryDirectory(prefix="mobility-")
        self.path = str(Path(self.directory.name) / "operator.sock")
        self.lease = lease
        self.requests = []
        self.stop = threading.Event()
        self.socket = socket.socket(socket.AF_UNIX)
        self.socket.bind(self.path)
        self.socket.listen(8)
        self.socket.settimeout(0.1)
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def serve(self):
        while not self.stop.is_set():
            try:
                connection, _ = self.socket.accept()
            except socket.timeout:
                continue
            except OSError:
                return
            with connection:
                connection.settimeout(2)
                self.answer(connection)

    def answer(self, connection):
        try:
            line = connection.makefile("rb").readline(65537)
            request = json.loads(line)
            self.requests.append(request)
            response = {"type": "operator_contact_result", "message_id": request.get("message_id", "")}
            if request.get("contact_op") == "discovery":
                response["contact_discovery"] = self.lease
            else:
                response["contact_candidates_result"] = [
                    {"candidate": value, "outcome": "skipped"}
                    for value in request.get("contact_candidates", [])]
            connection.sendall(json.dumps(response).encode() + b"\n")
        except (OSError, ValueError):
            return

    def close(self):
        self.stop.set()
        self.socket.close()
        self.thread.join(2)
        self.directory.cleanup()


def lease(advertise=False, browse=False, seconds=60):
    expiry = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(time.time() + seconds))
    return {"advertise": advertise, "browse": browse, "ports": [8445], "expires_at": expiry}


def helper_command(operator, once=True, port=0):
    state = Path(operator.directory.name) / "status"
    command = [sys.executable, str(ROOT / "scripts" / "pairtask.py"), "discovery", "run",
               "--socket", operator.path, "--host-port", str(port), "--state-dir", str(state)]
    if once:
        command.append("--once")
    return command


def run_helper(test, operator, extra=None):
    command = helper_command(operator)
    environment = dict(os.environ, PAIRTASK_DISCOVERY_TAILSCALE="none")
    if extra:
        environment.update(extra)
    result = subprocess.run(command, capture_output=True, text=True, env=environment, timeout=12)
    test.assertEqual(result.returncode, 0, "known-peer discovery CLI failed: " + result.stderr)
    return result
