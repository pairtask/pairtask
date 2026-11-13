"""Resize the macOS VM (#382). The operator's sizes live in the host state,
outside the installed tree, and are laid over colima.json, so an update never
resets them. Applying stops the VM, rewrites its Colima profile and owner
marker together, starts it through the usual path and grows the Incus pool
by what the disk grew."""

import json
import re
from pathlib import Path

from sandbox_colima_config import check_settings
from sandbox_mounts import write_json

VM_SIZE_FILE = Path.home() / ".local/state/pairtask/host/vm-size.json"
KEYS = ("cpu", "memory", "disk")
BOUNDS = {"cpu": (1, 64), "memory": (2, 256), "disk": (20, 2048)}
GIB = 2**30
POOL_NAME = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*")


def read_file(path=VM_SIZE_FILE):
    """The size file's content, {} when there is none."""
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f"refusing symlinked {path}")
    if not path.exists():
        return {}
    data = json.loads(path.read_text())
    check_size_file(path, data)
    return data


def check_size_file(path, data):
    if not schema_one(data):
        raise ValueError(f"{path} isn't a schema 1 VM size file")
    unknown = set(data) - set(KEYS) - {"schema", "pending"}
    if unknown:
        raise ValueError(f"{path} has unknown keys: {', '.join(sorted(unknown))}")


def schema_one(data):
    return isinstance(data, dict) and data.get("schema") == 1


def read_choice(path=VM_SIZE_FILE):
    """The operator's VM sizes, {} when none were chosen; a stored size that
    isn't a whole number within BOUNDS is refused."""
    data = read_file(path)
    choice = {key: data[key] for key in KEYS if key in data}
    errors = [bound_error(key, value) for key, value in choice.items()]
    errors = [error for error in errors if error]
    if errors:
        raise ValueError(f"{path}: {'; '.join(errors)}")
    return choice


def read_pending(path=VM_SIZE_FILE):
    """A resize not yet finished: the pool and its absolute target in GiB,
    recorded before anything changed; None when there is none. A target that
    isn't a pool name and a whole number of GiB is refused."""
    pending = read_file(path).get("pending")
    if pending is not None and not valid_target(pending):
        raise ValueError(f"{path}: the pending pool target isn't a pool name and a size in GiB")
    return pending


def valid_target(pending):
    if not isinstance(pending, dict) or set(pending) != {"pool", "size_gib"}:
        return False
    pool, size = pending["pool"], pending["size_gib"]
    return (isinstance(pool, str) and POOL_NAME.fullmatch(pool) is not None
            and isinstance(size, int) and not isinstance(size, bool) and size > 0)


def write_choice(choice, path=VM_SIZE_FILE, pending=None):
    """Record the operator's VM sizes, merged over any earlier ones, with
    the pending pool target, or none once the resize finished."""
    path = Path(path)
    path.parent.mkdir(mode=0o700, parents=True, exist_ok=True)
    data = {"schema": 1, **read_choice(path), **choice}
    if pending:
        data["pending"] = pending
    write_json(path, data)


def read_profile(marker):
    """The VM's settings from its owner marker; a marker this version didn't
    write is refused."""
    marker = Path(marker)
    if marker.is_symlink():
        raise ValueError(f"refusing symlinked {marker}")
    data = json.loads(marker.read_text())
    if not owner_marker(data):
        raise ValueError(f"{marker} isn't a schema 1 pairtask owner marker; refusing to resize that profile")
    return data["config"]


def owner_marker(data):
    return schema_one(data) and isinstance(data.get("config"), dict)


def check_profile(directory, current, choice, installed):
    """Before anything is recorded or stopped: the profile is the managed one,
    its settings other than sizes and mounts are the installed ones (the
    start would refuse anything else, leaving the VM down), nothing in it is
    a symlink, and colima.yaml still says what the owner marker does, so a
    resize never overwrites settings changed by hand. The one other state
    taken is a rewrite cut short between the two files: colima.yaml holding
    exactly the recorded sizes over the marker's."""
    if unsized(current) != unsized(installed):
        raise ValueError("the VM's settings other than its sizes differ from the installed ones; "
                         "reconcile the profile before resizing")
    directory = Path(directory)
    settings = directory / "colima.yaml"
    refuse_symlinks(directory, directory / "pairtask-owner.json", settings)
    try:
        check_settings(settings, current)
    except ValueError:
        if not interrupted_rewrite(settings, current, choice):
            raise


def refuse_symlinks(*paths):
    for path in paths:
        if path.is_symlink():
            raise ValueError(f"refusing symlinked {path}")


def unsized(config):
    return {key: value for key, value in config.items() if key not in KEYS and key != "mounts"}


def interrupted_rewrite(settings, current, choice):
    desired = overlay(current, choice)
    if desired == current:
        return False
    try:
        check_settings(settings, desired)
    except ValueError:
        return False
    return True


def overlay(config, choice):
    """colima.json's settings with the operator's sizes over them."""
    return {**config, **{key: choice[key] for key in KEYS if key in choice}}


def validate(request, current, needs):
    """Why the VM can't take these sizes; empty when it can. needs is the
    CPUs and GiB the workspace and the other containers take, or None when
    Incus couldn't say, which only matters for a decrease."""
    errors = [bound_error(key, value) for key, value in request.items()]
    if request.get("disk", current.get("disk", 0)) < current.get("disk", 0):
        errors.append(f"disk {request['disk']} would shrink it from {current['disk']}; a VM disk only grows")
    errors += [fit_error(key, request[key], current.get(key), needs) for key in ("cpu", "memory") if key in request]
    return [error for error in errors if error]


def bound_error(key, value):
    low, high = BOUNDS[key]
    if not isinstance(value, int) or not low <= value <= high:
        return f"{key} must be a whole number from {low} to {high}"
    return None


def fit_error(key, value, current, needs):
    """A CPU or RAM decrease must still hold what the containers need."""
    if current is not None and value >= current:
        return None
    if needs is None:
        return f"{key} {value} is a decrease, and what the containers need can't be read; check pairtask size show"
    if value < needs[key]:
        return f"{key} {value} is less than the {needs[key]} the workspace and the other containers need"
    return None


def pool_growth(pool, old_disk, new_disk):
    """The pool's new size in GiB when the disk grew and the pool is an
    Incus-managed loop pool on ZFS, else None."""
    if new_disk <= old_disk:
        return None
    current = managed_pool_gib(pool)
    return None if current is None else current + (new_disk - old_disk)


def managed_pool_gib(pool):
    """An Incus-managed loop pool on ZFS's size in whole GiB, else None:
    only such a pool has a config.size to raise."""
    if (pool or {}).get("driver") != "zfs":
        return None
    import sandbox_size
    size = sandbox_size.parse_size((pool.get("config") or {}).get("size"))
    return None if size is None else size // GIB


class Step:
    """One apply step: a name, and a command to run or a file to write."""

    def __init__(self, name, argv=None, write=None):
        self.name, self.argv, self.write = name, argv, write

    def __repr__(self):
        return self.name


def pending_target(current, request, pool, earlier):
    """The pool's absolute target, fixed once: an earlier unfinished resize's
    target stands, so a retry never grows the pool twice."""
    if earlier:
        return earlier
    grown = pool_growth(pool, current.get("disk", 0), overlay(current, request).get("disk", 0))
    return {"pool": pool["name"], "size_gib": grown} if grown is not None else None


def steps(current, request, target, host):
    """What applying does, in order; each step can run again after a failure.
    target is the pool's absolute size to reach, or None. host names the
    profile directory, the size file, the profile name, the Incus remote and
    the command that starts the VM through the usual path; query reads a
    pool's size and run runs a command."""
    desired = overlay(current, request)
    plan = [Step("record the VM sizes", write=lambda: write_choice(request, host["choice"], pending=target)),
            Step("stop the VM", argv=["colima", "stop", host["profile"]]),
            Step("rewrite the Colima profile", write=lambda: rewrite_profile(host["directory"], desired)),
            Step("start the VM", argv=host["start"])]
    if target:
        plan.append(Step("grow the Incus pool", write=lambda: grow_pool(host, target)))
    plan.append(Step("finish", write=lambda: write_choice({}, host["choice"])))
    return plan


def grow_pool(host, target):
    """Set the pool to its target unless it is there already."""
    pool = host["query"](f"/1.0/storage-pools/{target['pool']}") or {}
    import sandbox_size
    size = sandbox_size.parse_size((pool.get("config") or {}).get("size"))
    if size is not None and size // GIB >= target["size_gib"]:
        return
    argv = ["incus", "storage", "set", f"{host['remote']}:{target['pool']}", f"size={target['size_gib']}GiB"]
    if host["run"](argv) != 0:
        raise ValueError(f"{' '.join(argv)} failed")


def rewrite_profile(directory, config):
    """colima.yaml and the owner marker, together, as the mount change does."""
    write_json(Path(directory) / "colima.yaml", config)
    write_json(Path(directory) / "pairtask-owner.json", {"schema": 1, "config": config})
