"""The pairing route setting of one deployment: change it, and report it.

pairing_change only validates and computes; write_change writes. The
setting lives in the deployment's JSON variables, and what is applied is
read from its state, so status can tell an intent from a deployment.
"""

from dataclasses import dataclass
import json
from pathlib import Path
import re
import unicodedata

from sandbox_operator_config import LOCATION, ROOT, read_registry, state_project, worktree_directories
from sandbox_operator_state import private_write


SWITCHES = ("pairing_route", "secured_runtime", "peer_connector_probe")
PORT = "pairing_host_port"
NAME = "pairing_name"  # the name this workspace pairs under (#277); "" for none
NAME_BYTES = 64
CONTROLS = SWITCHES + (PORT, NAME)
DEFAULT_FILE = "operator.auto.tfvars.json"
JSON_PATTERNS = ("*.auto.tfvars.json", "terraform.tfvars.json")
HCL_PATTERNS = ("*.auto.tfvars", "terraform.tfvars")
# Any mention counts: HCL allows comments and quoting this can't parse safely.
HCL_CONTROL = re.compile(r"\b(%s)\b" % "|".join(CONTROLS))
CONNECTOR = ("pairing_connector", "connector", "pairing-connector")  # resource, instance, role
RELAY_PORT = 7445
PORTS = range(1024, 65536)  # or 0: not published
DEVICE = "pairing-relay"
RELAY_NOTE = (f"connector's relay listens on port {RELAY_PORT} inside the deployment and isn't "
              "published on this host's addresses, so another machine can't reach it")
FORWARD_NOTE = ("forwarding applied on all host addresses (LAN and tailnet), port {port}; on macOS, "
                "Colima forwards the VM's port to the Mac. Reachability isn't checked here")
STATES = {
    (True, True): "on",
    (False, False): "off",
    (True, False): "configured on, not applied; run: pairtask pairing on",
    (False, True): "configured off, still applied; run: pairtask pairing off",
}


@dataclass
class PairingChange:
    path: Path
    variables: dict


def variable_files(directory, patterns):
    paths = sorted({path for pattern in patterns for path in directory.glob(pattern)})
    for path in paths:
        if path.is_symlink() or not path.is_file():
            raise ValueError(f"Expected a regular variables file: {path}")
    return paths


def json_object(path):
    data = json.loads(path.read_text())
    if not isinstance(data, dict):
        raise ValueError(f"{path.name} must hold a JSON object")
    for key in SWITCHES:
        if key in data and not isinstance(data[key], bool):
            raise ValueError(f"{path.name}: {key} must be true or false")
    check_values(path, data)
    return data


def check_values(path, data):
    """The saved port and name, when present, as on and the broker take them."""
    for key, check in ((PORT, valid_port), (NAME, valid_name)):
        if key in data:
            check(data[key], f"{path.name}: {key}")


def valid_port(value, label=PORT):
    """A publishing port: 0 (none) or a whole number from 1024 to 65535."""
    if type(value) is not int or not publishable(value):
        raise ValueError(f"{label} must be 0 or a whole port from 1024 to 65535")
    return value


def valid_name(value, label="--name"):
    """A pairing name as the broker takes it (Go's peer.CheckPairingName): at
    most 64 bytes of UTF-8, every character printable as Go's unicode.IsPrint
    has it, a letter, mark, number, punctuation, symbol or the ASCII space.
    The tables are Python's, so an edge of the newest Unicode may differ."""
    if not isinstance(value, str) or len(value.encode("utf-8", "surrogatepass")) > NAME_BYTES:
        raise ValueError(f"{label} must be at most {NAME_BYTES} bytes of printable text")
    if not all(printable(c) for c in value):
        raise ValueError(f"{label} must be at most {NAME_BYTES} bytes of printable text")
    return value


def printable(c):
    return c == " " or unicodedata.category(c)[0] in "LMNPS"


def publishable(port):
    return port == 0 or port in PORTS


def settings(directory):
    """Each JSON variables file's object. A control set in an HCL file is
    refused: it can't be changed here without parsing HCL."""
    for path in variable_files(directory, HCL_PATTERNS):
        if HCL_CONTROL.search(path.read_text()):
            raise ValueError(f"{path.name} mentions a pairing control; move it to one JSON variables file")
    return {path: json_object(path) for path in variable_files(directory, JSON_PATTERNS)}


def control_file(directory, files):
    """The one file that sets the controls, or the operator's own file."""
    owners = [path for path, data in files.items() if any(key in data for key in CONTROLS)]
    if len(owners) > 1:
        raise ValueError("Pairing controls are set in more than one file (" +
                         ", ".join(path.name for path in owners) + "); keep them in one")
    return owners[0] if owners else directory / DEFAULT_FILE


def pairing_change(directory, enabled, host_port=None, name=None):
    """The variables with the route turned on or off; nothing is written.
    On publishes on host_port, else on the saved port, else on 7445; off
    keeps the saved port for the next on. A name replaces the saved one, ""
    clears it; None keeps whatever is saved, or nothing."""
    files = settings(directory)
    path = control_file(directory, files)
    variables = dict(files.get(path, {}))
    if not enabled:
        if host_port is not None or name is not None:
            raise ValueError("--host-port and --name are only for pairing on")
        variables["pairing_route"] = False
        return PairingChange(path, variables)
    if name is not None:
        variables[NAME] = valid_name(name)  # "" is valid: it clears the name
    if variables.get("peer_connector_probe"):
        raise ValueError("pairing_route and peer_connector_probe can't both be on; turn the probe off first")
    port = variables.get(PORT, RELAY_PORT) if host_port is None else valid_port(host_port, "--host-port")
    variables.update(secured_runtime=True, pairing_route=True)
    variables[PORT] = port
    return PairingChange(path, variables)


def write_change(change):
    private_write(change.path, (json.dumps(change.variables, indent=2) + "\n").encode())


def configured(directory):
    files = settings(directory)
    return files.get(control_file(directory, files), {}).get("pairing_route", False)


def connector_resource(resource):
    return (resource.get("mode", "managed"), resource["type"], resource.get("name")) == ("managed", "incus_instance", CONNECTOR[0])


def pairing_connector(resource, attrs, project):
    """Whether a managed instance is this deployment's own pairing connector."""
    role = (attrs.get("config") or {}).get("user.pairtask.role")
    return (resource.get("name"), attrs.get("name"), role, attrs.get("project")) == CONNECTOR + (project,)


def applied(directory, project):
    """Whether the state holds the pairing connector; None without a deployed state."""
    state = deployed_state(directory, project)
    return None if state is None else bool(connector_instances(state, project))


def deployed_state(directory, project):
    path = directory / "terraform.tfstate"
    if path.is_symlink():
        raise ValueError(f"Refusing symlinked state: {path}")
    actual = state_project(directory)
    if actual is None:
        return None
    if actual != project:
        raise ValueError(f"State belongs to {actual}, not {project}")
    return json.loads(path.read_text())


def connector_instances(state, project):
    """The state's pairing connectors, refusing any that isn't this deployment's own."""
    owned = [(resource, instance["attributes"]) for resource in state.get("resources", [])
             if connector_resource(resource) for instance in resource.get("instances", [])]
    if not all(pairing_connector(resource, attrs, project) for resource, attrs in owned):
        raise ValueError("Unexpected pairing connector in state; use explicit plan/apply")
    return owned


# Exactly the forwarder the deployment defines; the port is checked apart.
FORWARDER = {"type": "proxy", "connect": f"tcp:127.0.0.1:{RELAY_PORT}", "bind": "host", "nat": "false"}
LISTEN = re.compile(r"tcp:0\.0\.0\.0:([0-9]{1,5})")


def published_port(directory, project):
    """The applied publishing port: None without a connector in a deployed
    state, 0 when the connector publishes nothing."""
    state = deployed_state(directory, project)
    owned = [] if state is None else connector_instances(state, project)
    if not owned:
        return None
    return relay_port(owned[0][1])


def relay_port(attrs):
    """The connector's publishing port: 0 without the forwarder device."""
    devices = [device for device in attrs.get("device") or [] if device.get("name") == DEVICE]
    if len(devices) > 1:
        raise ValueError(f"Connector has more than one {DEVICE} device; use explicit plan/apply")
    return forwarder_port(devices[0]) if devices else 0


def forwarder_port(device):
    """The port of exactly the forwarder the deployment defines."""
    shape = dict(device.get("properties") or {}, type=device.get("type"))
    listen = LISTEN.fullmatch(str(shape.pop("listen", "")))
    port = int(listen.group(1)) if listen else 0
    if shape != FORWARDER or port == 0:
        raise ValueError(f"Unexpected {DEVICE} device on connector; use explicit plan/apply")
    return valid_port(port, f"{DEVICE} port")


def find_deployment(state_dir, project, root=ROOT, location=LOCATION):
    """The deployment directory, discovered without creating or remembering
    anything; None when there is none."""
    directory = state_dir or discovered(project, root, location)
    if directory is None:
        return None
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("State directory must be an existing, non-symlinked provisioning directory")
    return directory.resolve()


def discovered(project, root, location):
    saved = read_registry(location)
    if project in saved:
        return Path(saved[project])
    matches = [path for path in worktree_directories(root) if state_project(path) == project]
    if len(matches) > 1:
        raise ValueError("Multiple deployment states found; select one with --state-dir")
    return matches[0] if matches else None


def describe(directory, project):
    """The status lines for a deployment directory."""
    on, live = configured(directory), applied(directory, project)
    word = "on" if on else "off"
    state = f"configured {word}; not deployed yet" if live is None else STATES[(on, live)]
    files = settings(directory)
    path = control_file(directory, files)
    lines = [f"Pairing route: {state}", f"Settings: {path}"]
    if on:
        lines.append(f"Configured host port: {files.get(path, {}).get(PORT, 0) or 'none (not published)'}")
    if live:
        lines.append(publication(published_port(directory, project)))
    return lines


def publication(port):
    return FORWARD_NOTE.format(port=port) if port else RELAY_NOTE


def status(args):
    """Print the pairing route of the deployment, changing nothing; 1 if there is none."""
    directory = find_deployment(args.state_dir, args.project)
    if directory is None:
        print(f"Pairing route: no deployment found for {args.project}")
        return 1
    print("\n".join(describe(directory, args.project)))
    return 0


def report(enabled, directory, project):
    """The closing line of a pairing rollout, from the applied state."""
    if enabled:
        print("Pairing route on: " + publication(published_port(directory, project)) + ".", flush=True)
    else:
        print("Pairing route off: connector and its forwarding removed.", flush=True)
