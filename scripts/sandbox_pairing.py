"""The pairing route setting of one deployment: change it, and report it.

pairing_change only validates and computes; write_change writes. The
setting lives in the deployment's JSON variables, and what is applied is
read from its state, so status can tell an intent from a deployment.
"""

from dataclasses import dataclass
import json
from pathlib import Path
import re

from sandbox_operator_config import LOCATION, ROOT, read_registry, state_project, worktree_directories
from sandbox_operator_state import private_write


CONTROLS = ("pairing_route", "secured_runtime", "peer_connector_probe")
DEFAULT_FILE = "operator.auto.tfvars.json"
JSON_PATTERNS = ("*.auto.tfvars.json", "terraform.tfvars.json")
HCL_PATTERNS = ("*.auto.tfvars", "terraform.tfvars")
# Any mention counts: HCL allows comments and quoting this can't parse safely.
HCL_CONTROL = re.compile(r"\b(%s)\b" % "|".join(CONTROLS))
CONNECTOR = ("pairing_connector", "connector", "pairing-connector")  # resource, instance, role
RELAY_PORT = 7445
RELAY_NOTE = (f"connector's relay listens on port {RELAY_PORT} inside the deployment; "
              "it isn't published on this host's addresses yet, so another machine can't reach it")
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
    for key in CONTROLS:
        if key in data and not isinstance(data[key], bool):
            raise ValueError(f"{path.name}: {key} must be true or false")
    return data


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


def pairing_change(directory, enabled):
    """The variables with the route turned on or off; nothing is written."""
    files = settings(directory)
    path = control_file(directory, files)
    variables = dict(files.get(path, {}))
    if not enabled:
        variables["pairing_route"] = False
        return PairingChange(path, variables)
    if variables.get("peer_connector_probe"):
        raise ValueError("pairing_route and peer_connector_probe can't both be on; turn the probe off first")
    variables.update(secured_runtime=True, pairing_route=True)
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
    path = directory / "terraform.tfstate"
    if path.is_symlink():
        raise ValueError(f"Refusing symlinked state: {path}")
    actual = state_project(directory)
    if actual is None:
        return None
    if actual != project:
        raise ValueError(f"State belongs to {actual}, not {project}")
    found = False
    for resource in json.loads(path.read_text()).get("resources", []):
        if not connector_resource(resource):
            continue
        for instance in resource.get("instances", []):
            if not pairing_connector(resource, instance["attributes"], project):
                raise ValueError("Unexpected pairing connector in state; use explicit plan/apply")
            found = True
    return found


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
    lines = [f"Pairing route: {state}", f"Settings: {control_file(directory, settings(directory))}"]
    if live:
        lines.append(RELAY_NOTE)
    return lines


def status(args):
    """Print the pairing route of the deployment, changing nothing; 1 if there is none."""
    directory = find_deployment(args.state_dir, args.project)
    if directory is None:
        print(f"Pairing route: no deployment found for {args.project}")
        return 1
    print("\n".join(describe(directory, args.project)))
    return 0


def report(enabled):
    """The closing line of a pairing rollout."""
    if enabled:
        print("Pairing route on: " + RELAY_NOTE + ".", flush=True)
    else:
        print("Pairing route off: connector removed.", flush=True)
