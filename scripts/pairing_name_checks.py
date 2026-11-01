"""Installed proof of the managed self-name file and systemd environment.

The initial name is provisioned on first enable by sandbox-pairing-route.
Updates and clearing stop the disposable containers, apply the provider
file, then observe the newly started setup and broker. Nothing reads or
changes an operator deployment, and only the pairing-name environment key
leaves the guest.
"""

import json
import subprocess

import sandbox_pairing

INITIAL_NAME = "First workspace"
CHANGED_NAME = '  "quoted" \\ $cash % #name;  '
SETUP_UNIT = "pairtask-pairing-setup.service"
BROKER_UNIT = "pairtask-secured-broker.service"
READ_NAME = """import json, pathlib, sys
prefix = b'PAIRTASK_PAIRING_NAME='
values = [entry[len(prefix):].decode('utf-8') for entry in
          pathlib.Path('/proc/' + sys.argv[1] + '/environ').read_bytes().split(b'\\0')
          if entry.startswith(prefix)]
if len(values) > 1:
    raise ValueError('duplicate pairing-name environment key')
print(json.dumps(values[0] if values else None))
"""
INGRESS_BLOCKED = """import errno, socket, sys
with socket.socket(socket.AF_UNIX) as connection:
    connection.settimeout(3)
    try:
        connection.connect('/mnt/pairtask-pairing/pairing.sock')
    except OSError as error:
        if error.errno not in (errno.ENOENT, errno.ECONNREFUSED):
            raise
    else:
        sys.exit('pairing ingress accepted a connection while the name barrier was held')
"""


def require(ok, message):
    if not ok:
        raise ValueError(message)


def unit_started(deployment, unit):
    value = deployment.output("secured", "systemctl", "show", "--property=ActiveEnterTimestampMonotonic",
                              "--value", unit).strip()
    require(value.isdigit() and int(value) > 0, "name proof could not observe a service start")
    return int(value)


def observe_name(deployment, expected, phase="name"):
    deployment.await_active("secured", SETUP_UNIT)
    deployment.await_active("secured", BROKER_UNIT)
    setup = unit_started(deployment, SETUP_UNIT)
    broker = unit_started(deployment, BROKER_UNIT)
    require(setup <= broker, f"{phase}: broker started before pairing-name setup finished")
    pid = deployment.main_pid("secured", BROKER_UNIT)
    require(pid.isdigit() and int(pid) > 0, "name proof could not observe the broker PID")
    actual = json.loads(deployment.output("secured", "python3", "-c", READ_NAME, pid))
    require(actual == expected, f"{phase}: installed pairing name expected {expected!r}, observed {actual!r}")
    status = deployment.operator("status", "--json")
    require(status.returncode == 0, "name proof could not read broker pairing status")
    self_name = json.loads(status.stdout)["pairing_result"]["self_name"]
    require(self_name == (expected or "workspace"),
            f"{phase}: broker self_name expected {expected or 'workspace'!r}, observed {self_name!r}")
    return setup


def require_gated(deployment, phase):
    """Attempt both local admission and pairing ingress while ready is absent."""
    start = deployment.execute("secured", "systemctl", "start", BROKER_UNIT)
    require(start.returncode != 0, f"{phase}: broker started before the name barrier was released")
    active = deployment.execute("secured", "systemctl", "is-active", "--quiet", BROKER_UNIT)
    require(active.returncode != 0, f"{phase}: broker is active while the name barrier is held")
    require(deployment.operator("status", "--json").returncode != 0,
            f"{phase}: broker admitted an operator before the name barrier was released")
    ingress = deployment.execute("connector", "python3", "-c", INGRESS_BLOCKED)
    require(ingress.returncode == 0, f"{phase}: pairing ingress was not closed: {ingress.stderr}")


def prove_name(deployment, publication):
    """Prove supported-rollout barriers, including a deliberately delayed upload."""
    require_gated(deployment, "first held")
    sandbox_pairing.NameBarrier(publication.project, publication.remote, INITIAL_NAME).release()
    previous_setup = observe_name(deployment, INITIAL_NAME, "first")
    for phase, name in (("update", CHANGED_NAME), ("clear", "")):
        publication.stop("workspace", "connector", "secured")
        barrier = sandbox_pairing.NameBarrier(publication.project, publication.remote, name)
        barrier.invalidate()  # actual file removal on a stopped secured in CI
        publication.set(pairing_name=name)
        if phase == "update":
            # Disposable fixture only: boot the OLD file after invalidating its
            # OLD ready marker, before permitting the provider's new upload.
            subprocess.run(["incus", "--project", publication.project, "start",
                            publication.remote + "secured", publication.remote + "connector"], check=True)
            require_gated(deployment, "update delayed upload")
        require(publication.apply(release=False) == 0, f"{phase}: pairing-name proof apply failed")
        require_gated(deployment, f"{phase} held after upload")
        barrier.release()
        current_setup = observe_name(deployment, name or None, phase)
        require(current_setup > previous_setup, "name change did not rerun setup before the broker")
        previous_setup = current_setup
    print("PASS: name barriers refuse pairing before first-enable, delayed update and clear; exact names reach the installed broker after release.", flush=True)
