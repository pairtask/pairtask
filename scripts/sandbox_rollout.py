"""Download, stop managed runtimes, and apply a data-preserving replacement."""

from copy import copy
from pathlib import Path
import importlib.util
import json
import subprocess
from types import SimpleNamespace

from sandbox_operator_config import ROOT, image_selection, state_project
from sandbox_operator_state import deployment_lock
from sandbox_operator_recipe import refresh_recipe
from sandbox_operator_workflow import initialize_variables, require_recovered, run_locked_operation
from sandbox_pairing import pairing_change, pairing_connector, report, write_change
from sandbox_releases import download_release
from sandbox_tools_install import (Container, Release, shipped_tools, RETRY_COMMAND, has_tool_store, install,
                                   load_bundles,
                                   release_lock_from, release_provenance, selection, supports_tool_store)


def managed_instances(directory, project):
    if state_project(directory) is None:
        return []
    state = json.loads((directory / "terraform.tfstate").read_text())
    owned = []
    for resource in state.get("resources", []):
        if resource.get("mode", "managed") != "managed" or resource["type"] != "incus_instance":
            continue
        owned.extend((resource, instance["attributes"]) for instance in resource.get("instances", []))
    return [managed_name(resource, attrs, project) for resource, attrs in owned]


def managed_name(resource, attrs, project):
    if attrs["project"] != project or not admitted(resource, attrs, project):
        raise ValueError("Unexpected managed instance; use explicit plan/apply")
    return attrs["name"]


def admitted(resource, attrs, project):
    """workspace and secured, and the pairing route's own connector."""
    return attrs["name"] in ("workspace", "secured") or pairing_connector(resource, attrs, project)


def instances(args):
    result = subprocess.check_output(["incus", "--project", args.project, "list", args.remote + ":", "--format=json"])
    return {item["name"]: item["status"] for item in json.loads(result)}


def stop_managed(args, names):
    if not names:
        return
    statuses = instances(args)
    for name in sorted(names, key=STOP_ORDER.index):
        status = statuses.get(name)
        if status is None or status == "Stopped":
            continue
        if status != "Running":
            raise ValueError(f"{name} is {status}; stop it before retrying rollout")
        print(f"Stopping {name} (active sessions will end)…", flush=True)
        subprocess.run(["incus", "--project", args.project, "stop", f"{args.remote}:{name}",
                        "--timeout", "60"], check=True)


# secured serves the others, so it stops last.
STOP_ORDER = ("workspace", "connector", "secured")


def configure_ssh(args):
    if instances(args).get("workspace") != "Running":
        print("Rollout complete; workspace is stopped by deployment settings. Start it before SSH setup.")
        return
    spec = importlib.util.spec_from_file_location("sandbox_ssh", ROOT / "scripts/sandbox-ssh.py")
    ssh = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(ssh)
    ssh.setup(SimpleNamespace(remote=args.remote, project=args.project, instance="workspace",
                              state_dir=args.ssh_state_dir, refresh_host_key=True))


def install_tools(args, server, image_dir):
    """Activate this release's tool generation in every running instance,
    before any agent is admitted. A stopped instance installs at its rollout."""
    release = tool_release(image_dir, server["architectures"][0])
    if release is None:
        print("This release predates the tool store; its tools stay baked into the image.", flush=True)
        return
    selected = selection(getattr(args, "tools", None) or [])
    for name in running_targets(args):
        install_in(Container(args.remote, args.project, name), release, selected)


def activate_running(remote, project, image_dir):
    """For disposable test deployments: activate the release's tools, with every
    optional tool, in each running managed instance. Idempotent: a root already
    released for this generation only has its gated units started."""
    machine = json.loads((Path(image_dir) / "manifest.json").read_text())["architecture"]
    # Every optional tool the release ships: an older release lacks newer bundles.
    args = SimpleNamespace(remote=remote, project=project, tools=shipped_tools(tool_release(image_dir, machine)))
    install_tools(args, {"architectures": [machine]}, image_dir)


def activate_instances(args, image_dir, machine, instances):
    """For disposable test deployments: activate the release's generation in
    each running instance, `instances` mapping names to roles."""
    release = tool_release(image_dir, machine)
    if release is None:
        return
    selected = selection(getattr(args, "tools", None) or [])
    for name, role in instances.items():
        install_in(Container(args.remote, args.project, name).as_role(role), release, selected)


def tool_release(image_dir, machine):
    """The Release for image_dir, or None when its lock predates the artifact
    table. An image directory without a manifest (a locally modified image)
    leaves the root's own lock in charge."""
    bundles = load_bundles(image_dir, machine)
    if not (Path(image_dir) / "manifest.json").exists():
        return Release(None, machine, bundles=bundles)
    release_lock = release_lock_from(image_dir)
    if not supports_tool_store(release_lock):
        return None
    provenance, architecture = release_provenance(image_dir)
    if architecture not in (None, machine):
        raise ValueError(f"Release image is for {architecture}, the server is {machine}")
    return Release(release_lock, machine, provenance, bundles)


def running_targets(args):
    statuses = instances(args)
    for name in ("workspace", "secured"):
        if statuses.get(name) == "Running":
            yield name
        elif name in statuses:
            print(f"{name} is stopped; its tools install at the next rollout.", flush=True)


def install_in(container, release, selected):
    if not has_tool_store(container):
        print(f"{container.name}: this release's image has no tool store; its tools stay baked in.", flush=True)
        return
    generation = install(container, release, selected)
    print(f"{container.name}: tool generation {generation or 'already'} active", flush=True)


def tools_retry(args, directory, server):
    """Rerun a crashed activation. install() verifies a ready peer without
    touching it and refuses any mutation of a root that admitted agents."""
    with deployment_lock(directory):
        require_recovered(directory)
        _, _, variables = image_selection(directory, args.image_dir)
        install_tools(args, server, variables.parent)
        configure_ssh(args)


def rollout(args, directory, server):
    # One lock covers download through SSH: no competing plan/apply can change the selection.
    with deployment_lock(directory):
        require_recovered(directory)
        pairing = checked_pairing(args, directory)
        initialize_variables(args, directory)
        names = managed_instances(directory, args.project)
        selected = copy(args)
        selected.image_dir = args.image_dir or download_release(server["architectures"][0], args.release, args.repo)
        selected.image_dir = selected.image_dir.resolve()
        image_selection(directory, selected.image_dir)
        refresh_recipe(directory)
        record_pairing(args, directory, pairing)
        print("Rolling out workspace; persistent volumes are protected by plan validation.", flush=True)
        stop_managed(args, names)
        selected.action, selected.replace, selected.destroy = "plan", True, False
        selected.automatic = True
        try:
            run_locked_operation(selected, directory, server)
            selected.action, selected.replace = "apply", False
            run_locked_operation(selected, directory, server)
        except BaseException:
            print("Rollout incomplete. Inspect the error before restarting containers; host state and data are retained.", flush=True)
            raise
        try:
            install_tools(args, server, selected.image_dir)
        except BaseException:
            print(f"Tools not activated; agents are not admitted. Fix the cause, then run: {RETRY_COMMAND}", flush=True)
            raise
        # Re-trust the key through authenticated Incus only after a successful apply.
        configure_ssh(args)
        if pairing is not None:
            report(pairing, directory, args.project)


def checked_pairing(args, directory):
    """The requested pairing route (None keeps the setting), refused before
    anything is written or stopped."""
    pairing = getattr(args, "pairing", None)
    if pairing is not None:
        pairing_change(directory, pairing, getattr(args, "host_port", None))
    return pairing


def record_pairing(args, directory, pairing):
    # Computed again from the files as they are now: initialization may have written them.
    if pairing is not None:
        write_change(pairing_change(directory, pairing, getattr(args, "host_port", None)))
