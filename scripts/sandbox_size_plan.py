"""Grow the workspace and its volumes (#382): the operator's sizes go in
the deployment's own size.auto.tfvars.json, checked first, and the existing
plan/apply carries them out. Nothing shrinks, and the workspace never asks
for more than the VM can give it."""

import json

from sandbox_operator_state import private_write
import sandbox_size

SIZE_FILE = "size.auto.tfvars.json"
TFVARS_PATTERNS = ("*.auto.tfvars", "*.auto.tfvars.json", "terraform.tfvars", "terraform.tfvars.json")

# Command-line flag → tofu variable, and each variable's allowed range.
REQUEST_FLAGS = {"cpu": "cpu_count", "memory": "memory_gib", "disk": "disk_gib", "workspace": "workspace_gib",
                 "agent_home": "agent_home_gib", "docker": "docker_gib", "tools": "tools_gib"}
RANGES = {"cpu_count": (1, 8), "memory_gib": (1, 16), "disk_gib": (2, 100), "workspace_gib": (1, 100),
          "agent_home_gib": (1, 100), "docker_gib": (1, 100), "tools_gib": (1, 100)}
# Each volume's name in Incus, by its variable.
VOLUMES = {"workspace_gib": "workspace-data", "agent_home_gib": "agent-home",
           "docker_gib": "docker-data", "tools_gib": "workspace-tools"}
# What the VM keeps for itself, and what the other containers are given
# (their CPU and RAM limits in the tofu configuration).
VM_RESERVE_GIB = 1
RESERVED = {"secured": {"cpu": 1, "memory_gib": 2}, "connector": {"cpu": 1, "memory_gib": 1}}
# Beside the workspace, the containers whose root disk disk_gib sets too.
ROOT_SHARERS = ("secured", "connector")


def request_from(args):
    """The variables the flags set, as {variable: value}."""
    return {var: getattr(args, flag) for flag, var in REQUEST_FLAGS.items() if getattr(args, flag) is not None}


def live_sizes(report):
    """What the sandbox runs with now, as {variable: value}, from size show's
    report; a value Incus didn't give is left out."""
    workspace = report.get("workspace") or {}
    live = {"cpu_count": as_int(workspace.get("cpu")), "memory_gib": gib(workspace.get("memory_bytes")),
            "disk_gib": gib(workspace.get("root_bytes"))}
    by_name = {v["name"]: v["size_bytes"] for v in report.get("volumes", [])}
    live.update({var: gib(by_name.get(name)) for var, name in VOLUMES.items()})
    return {var: value for var, value in live.items() if value is not None}


def shared_root(query, project, live):
    """live with disk_gib as the largest root disk the variable now sets: the
    workspace's and that of each other container that exists. It's left out
    when one can't be read, so a shrink can't be ruled out."""
    sizes = [live.get("disk_gib")] + [root_gib(query, name, project) for name in ROOT_SHARERS]
    rest = {var: value for var, value in live.items() if var != "disk_gib"}
    return rest if None in sizes else {**rest, "disk_gib": max(sizes)}


def root_gib(query, name, project):
    """A container's root disk in GiB: 0 when it doesn't exist, None when its
    size can't be read."""
    instance = query(f"/1.0/instances/{name}?project={project}")
    if instance is None:
        return 0
    root = (instance.get("expanded_devices") or {}).get("root") or {}
    return gib(sandbox_size.parse_size(root.get("size")))


def as_int(value):
    try:
        return int(value)
    except (TypeError, ValueError):
        return None


def gib(n):
    return None if n is None else -(-n // sandbox_size.GIB)


def capacity(query, report, system):
    """The CPUs and GiB of RAM the workspace shares: the VM on macOS, the host
    elsewhere; None when unknown."""
    if system == "Darwin":
        return vm_capacity(report.get("vm") or {})
    return host_capacity(query("/1.0/resources") or {})


def vm_capacity(vm):
    if not vm.get("cpu"):
        return None
    return {"cpu": vm["cpu"], "memory_gib": gib(vm.get("memory_bytes"))}


def host_capacity(resources):
    cpu, memory = (resources.get("cpu") or {}).get("total"), (resources.get("memory") or {}).get("total")
    if not cpu or not memory:
        return None
    return {"cpu": cpu, "memory_gib": memory // sandbox_size.GIB}


def reserved(query, project):
    """The CPUs and RAM the other containers in the project are given."""
    names = {item.get("name") for item in query(f"/1.0/instances?project={project}&recursion=1") or []}
    present = [limits for name, limits in RESERVED.items() if name in names]
    return {"cpu": sum(r["cpu"] for r in present), "memory_gib": sum(r["memory_gib"] for r in present)}


class Facts:
    """What a request is checked against: the live sizes, the room the VM or
    host has, what the other containers keep, and the tfvars files that set
    a requested size already."""

    def __init__(self, live, room, reserved, conflicts):
        self.live, self.room, self.reserved, self.conflicts = live, room, reserved, conflicts


def validate(request, facts):
    """Why the request can't go ahead; empty when it can."""
    errors = [range_error(var, value) for var, value in request.items()]
    errors += [shrink_error(var, value, facts.live) for var, value in request.items()]
    errors += headroom_errors(request, facts.room, facts.reserved)
    errors += [f"{var} is already set in {name}; change it there or remove it, so one file decides"
               for var, name in facts.conflicts.items()]
    return [error for error in errors if error]


def shrink_error(var, value, live):
    """Why value can't replace the live size: smaller, or the live size is
    unknown, so a shrink can't be ruled out."""
    if var not in live:
        return f"{var}: the current size can't be read, so a shrink can't be ruled out; check pairtask size show"
    if value < live[var]:
        return f"{var} {value} would shrink it from {live[var]}; sizes only grow"
    return None


def range_error(var, value):
    low, high = RANGES[var]
    if not isinstance(value, int) or not low <= value <= high:
        return f"{var} must be a whole number from {low} to {high}"
    return None


def headroom_errors(request, room, reserved):
    """The workspace's CPU and RAM against what the VM or host has, checked
    when the request changes them. A request that only grows disks is never
    held back by how CPU and RAM are already set: refusing it would leave a
    full volume unfixable, and the CPU and RAM it would judge aren't new."""
    if room is None:
        return ["the VM's size is unknown, so the workspace's CPU and RAM can't be checked; try pairtask size show"
                ] if {"cpu_count", "memory_gib"} & set(request) else []
    errors = []
    cpus = room["cpu"] - reserved["cpu"]
    if request.get("cpu_count", 0) > cpus:
        errors.append(f"cpu_count {request['cpu_count']} leaves no room: {room['cpu']} CPUs, less "
                      f"{reserved['cpu']} for the other containers, leaves {cpus}")
    free = room["memory_gib"] - VM_RESERVE_GIB - reserved["memory_gib"]
    if request.get("memory_gib", 0) > free:
        errors.append(f"memory_gib {request['memory_gib']} leaves no room: {room['memory_gib']} GiB, less "
                      f"{VM_RESERVE_GIB} for the VM and {reserved['memory_gib']} for the other containers, leaves {free}")
    return errors


def conflicts(directory, keys):
    """Each requested variable another tfvars file already sets: {var: file}."""
    found = {}
    for path in sorted(p for pattern in TFVARS_PATTERNS for p in directory.glob(pattern)):
        if path.name != SIZE_FILE:
            found.update({key: path.name for key in keys if key not in found and sets(path, key)})
    return found


def sets(path, key):
    """path sets key: a JSON file by its keys, an HCL file by any mention."""
    text = path.read_text()
    if path.name.endswith(".json"):
        try:
            return key in json.loads(text)
        except ValueError:
            return False
    return key in text


def size_file(directory):
    """The deployment's size file, refused when it is a symlink: what it
    points at isn't the operator's to overwrite."""
    path = directory / SIZE_FILE
    if path.is_symlink():
        raise ValueError(f"refusing symlinked {path}; replace it with a regular file")
    return path


def write_sizes(directory, request):
    """The deployment's size file with the request merged over it."""
    path = size_file(directory)
    current = json.loads(path.read_text()) if path.exists() else {}
    private_write(path, (json.dumps({**current, **request}, indent=2, sort_keys=True) + "\n").encode())
    return path
