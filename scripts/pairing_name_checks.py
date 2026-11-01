"""Installed proof of the managed self-name file and systemd environment.

The initial name is provisioned on first enable by sandbox-pairing-route.
Updates and clearing stop the disposable containers, apply the provider
file, then observe the newly started setup and broker. Nothing reads or
changes an operator deployment, and only the pairing-name environment key
leaves the guest.
"""

import json

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


def require(ok, message):
    if not ok:
        raise ValueError(message)


def unit_started(deployment, unit):
    value = deployment.output("secured", "systemctl", "show", "--property=ActiveEnterTimestampMonotonic",
                              "--value", unit).strip()
    require(value.isdigit() and int(value) > 0, "name proof could not observe a service start")
    return int(value)


def observe_name(deployment, expected):
    deployment.await_active("secured", SETUP_UNIT)
    deployment.await_active("secured", BROKER_UNIT)
    setup = unit_started(deployment, SETUP_UNIT)
    broker = unit_started(deployment, BROKER_UNIT)
    require(setup <= broker, "broker started before pairing-name setup finished")
    pid = deployment.main_pid("secured", BROKER_UNIT)
    require(pid.isdigit() and int(pid) > 0, "name proof could not observe the broker PID")
    actual = json.loads(deployment.output("secured", "python3", "-c", READ_NAME, pid))
    require(actual == expected, "installed broker did not receive the exact pairing name")
    status = deployment.operator("status", "--json")
    require(status.returncode == 0, "name proof could not read broker pairing status")
    self_name = json.loads(status.stdout)["pairing_result"]["self_name"]
    require(self_name == (expected or "workspace"), "broker status did not use the provisioned name")
    return setup


def prove_name(deployment, publication):
    """Observe first enable, a special-character update, and default after clear."""
    previous_setup = observe_name(deployment, INITIAL_NAME)
    for name in (CHANGED_NAME, ""):
        publication.stop("workspace", "connector", "secured")
        publication.set(pairing_name=name)
        require(publication.apply() == 0, "pairing-name proof apply failed")
        current_setup = observe_name(deployment, name or None)
        require(current_setup > previous_setup, "name change did not rerun setup before the broker")
        previous_setup = current_setup
    print("PASS: first-enable, updated and cleared names reach the installed broker exactly.", flush=True)
