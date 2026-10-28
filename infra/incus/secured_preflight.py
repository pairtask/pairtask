"""Read-only host preflight: attach protected IPC mounts only to stopped runtimes."""

import http.client
import json
import os
import socket
from urllib.parse import quote


class IncusConnection(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.host)


def require_stopped(status, payload, role="workspace", change="enabling or upgrading secured_runtime"):
    if status == 404:
        return  # Fresh project/instance: the volume will be present at first boot.
    if status != 200:
        raise ValueError(f"Cannot inspect {role} power state (Incus HTTP {status})")
    if payload.get("metadata", {}).get("status") != "Stopped":
        raise ValueError(f"Stop {role} before {change}; hot-added read-only mounts can be remounted writable. "
                         "Keep it stopped until the apply succeeds. The apply starts it again when running=true.")


def main():
    connection = IncusConnection(os.environ["PAIRTASK_INCUS_SOCKET"], timeout=10)
    try:
        project = quote(os.environ["PAIRTASK_INCUS_PROJECT"], safe="")
        # The pairing route (#178) reuses this check for its own instances.
        roles = os.environ.get("PAIRTASK_PREFLIGHT_ROLES", "workspace,secured").split(",")
        change = os.environ.get("PAIRTASK_PREFLIGHT_CHANGE", "enabling or upgrading secured_runtime")
        for role in roles:
            connection.request("GET", f"/1.0/instances/{role}?project={project}", headers={"Host": "incus"})
            response = connection.getresponse()
            data = response.read(1048577)
            if len(data) > 1048576:
                raise ValueError("Incus instance metadata exceeds the preflight limit")
            payload = json.loads(data) if response.status == 200 else {}
            require_stopped(response.status, payload, role, change)
    finally:
        connection.close()


if __name__ == "__main__":
    main()
