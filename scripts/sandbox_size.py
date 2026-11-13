"""How big the sandbox is and how full (#382): the Colima VM on macOS, the
workspace container and its volumes. Everything is read through Incus's API
and the VM's owner marker; nothing is created or changed."""

import json
import re
import subprocess
from pathlib import Path

# A volume this full is worth a warning.
WARN_PERCENT = 90
# How long one query may take before Incus counts as not answering.
QUERY_TIMEOUT = 30

UNITS = {"": 1, "B": 1, "KB": 10**3, "MB": 10**6, "GB": 10**9, "TB": 10**12,
         "KIB": 2**10, "MIB": 2**20, "GIB": 2**30, "TIB": 2**40}
GIB = 2**30


class Unreachable(Exception):
    """Incus didn't answer a query, or answered something unreadable."""


def parse_size(value):
    """Bytes in an Incus size such as "10GiB" or "512MB", or None."""
    match = re.fullmatch(r"\s*(\d+(?:\.\d+)?)\s*([KMGT]i?B|B)?\s*", str(value or ""), re.IGNORECASE)
    if not match:
        return None
    return int(float(match.group(1)) * UNITS[(match.group(2) or "").upper()])


def incus_query(remote):
    """A query function over `incus query`: the decoded JSON, None for a
    resource Incus reports not found; any other failure raises Unreachable."""
    return lambda path: answer(run_query(remote, path), path)


def run_query(remote, path):
    """`incus query remote:path`, or Unreachable when incus can't run or
    doesn't answer within QUERY_TIMEOUT seconds."""
    try:
        return subprocess.run(["incus", "query", f"{remote}:{path}"], capture_output=True, text=True,
                              timeout=QUERY_TIMEOUT)
    except subprocess.TimeoutExpired:
        raise Unreachable(f"incus query {path} took over {QUERY_TIMEOUT}s") from None
    except OSError as error:
        raise Unreachable(f"incus can't run: {error}") from None


def answer(done, path):
    """The JSON a query returned; None for a missing resource."""
    if done.returncode != 0:
        if resource_missing(done.stderr):
            return None
        raise Unreachable(done.stderr.strip() or f"incus query {path} failed")
    try:
        return json.loads(done.stdout)
    except ValueError:
        raise Unreachable(f"incus query {path} returned no JSON") from None


def resource_missing(stderr):
    """Incus's answer for a resource it doesn't have: "Error: Not Found" or
    "Error: Instance not found" and the like. A missing remote names it in
    quotes ("remote 'x' not found") and isn't this."""
    return re.fullmatch(r"error: (?:[a-z]+ )*not found", stderr.strip(), re.IGNORECASE) is not None


def collect(query, profile_dir, system, project="pairtask"):
    """The report: VM, workspace, volumes and pool, each None when unknown.
    query returns None only for what Incus doesn't have; Unreachable passes."""
    report = {"vm": vm_size(profile_dir, system), "workspace": None, "volumes": [], "pool": None}
    instance = query(f"/1.0/instances/workspace?project={project}")
    if instance is None:
        return report
    report["workspace"] = workspace_size(query, instance, project)
    pool = report["workspace"]["pool"]
    if pool:
        report["volumes"] = volumes(query, pool, project)
        report["pool"] = pool_size(query, pool)
    return report


def vm_size(profile_dir, system):
    """The VM's CPUs, RAM and disk from its owner marker; {"host": True} off
    macOS, None while there is no profile."""
    if system != "Darwin":
        return {"host": True}
    marker = Path(profile_dir) / "pairtask-owner.json"
    try:
        config = json.loads(marker.read_text()).get("config", {})
    except (OSError, ValueError, AttributeError):
        return None
    return {"cpu": config.get("cpu"), "memory_bytes": gib(config.get("memory")), "disk_bytes": gib(config.get("disk"))}


def gib(value):
    return int(value) * GIB if isinstance(value, (int, float)) else None


def workspace_size(query, instance, project):
    config = instance.get("expanded_config") or {}
    root = (instance.get("expanded_devices") or {}).get("root") or {}
    state = query(f"/1.0/instances/workspace/state?project={project}") or {}
    used = ((state.get("disk") or {}).get("root") or {}).get("usage")
    return {"cpu": config.get("limits.cpu"), "memory_bytes": parse_size(config.get("limits.memory")),
            "root_bytes": parse_size(root.get("size")), "root_used_bytes": used, "pool": root.get("pool")}


def volumes(query, pool, project):
    listed = query(f"/1.0/storage-pools/{pool}/volumes/custom?project={project}&recursion=1") or []
    return sorted((volume(query, pool, project, item) for item in listed), key=lambda v: v["name"])


def volume(query, pool, project, item):
    name = item.get("name")
    state = query(f"/1.0/storage-pools/{pool}/volumes/custom/{name}/state?project={project}") or {}
    return {"name": name, "size_bytes": parse_size((item.get("config") or {}).get("size")),
            "used_bytes": (state.get("usage") or {}).get("used")}


def pool_size(query, pool):
    space = (query(f"/1.0/storage-pools/{pool}/resources") or {}).get("space") or {}
    return {"name": pool, "total_bytes": space.get("total"), "used_bytes": space.get("used")}


def percent(used, size):
    """Whole percent of size used, or None when either is unknown; an empty
    volume is 0."""
    if used is None or not size:
        return None
    return int(used * 100 // size)


def warnings(report):
    """A line per volume, or the workspace root, at or above WARN_PERCENT."""
    full = [(v["name"], percent(v["used_bytes"], v["size_bytes"])) for v in report["volumes"]]
    workspace = report["workspace"]
    if workspace:
        full.append(("workspace root", percent(workspace["root_used_bytes"], workspace["root_bytes"])))
    return [f"{name} is {p}% full; clear space or grow it (docs/sandbox-storage.md#resize)"
            for name, p in full if p is not None and p >= WARN_PERCENT]


def human(n):
    """Bytes for people: GiB with one decimal, MiB below a GiB."""
    if n is None:
        return "?"
    if n < GIB:
        return f"{n // 2**20} MiB"
    return f"{n / GIB:.1f}".rstrip("0").rstrip(".") + " GiB"


def with_percent(used, size):
    p = percent(used, size)
    if p is None:
        return human(size)
    return f"{human(size)} ({p}%{' ⚠' if p >= WARN_PERCENT else ''})"


def render(report):
    """The report as the lines `pairtask size show` prints."""
    lines = [vm_line(report["vm"], report["pool"]), workspace_line(report["workspace"])]
    if report["volumes"]:
        lines.append("volumes:   " + " · ".join(f"{v['name']} {with_percent(v['used_bytes'], v['size_bytes'])}"
                                              for v in report["volumes"]))
    lines += [f"warning:   {w}" for w in warnings(report)]
    return "\n".join(lines)


def vm_line(vm, pool):
    if vm is None:
        head = "VM:        no pairtask profile yet (pairtask install)"
    elif vm.get("host"):
        head = "VM:        none (Incus runs on this host)"
    else:
        head = f"VM:        {vm['cpu']} CPU · {human(vm['memory_bytes'])} RAM · {human(vm['disk_bytes'])} disk"
    if pool:
        head += f"   (pool {pool['name']} {human(pool['total_bytes'])}, {human(pool['used_bytes'])} used)"
    return head


def workspace_line(workspace):
    if workspace is None:
        return "workspace: not provisioned (pairtask install)"
    return (f"workspace: {workspace['cpu'] or '?'} CPU · {human(workspace['memory_bytes'])} RAM · "
            f"root {with_percent(workspace['root_used_bytes'], workspace['root_bytes'])}")


def as_json(report):
    """The report with each volume's percent and the warnings, as JSON."""
    out = dict(report)
    out["volumes"] = [dict(v, percent=percent(v["used_bytes"], v["size_bytes"])) for v in report["volumes"]]
    out["warnings"] = warnings(report)
    return json.dumps(out, indent=2)
