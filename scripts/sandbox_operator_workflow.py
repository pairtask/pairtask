"""Plan/apply orchestration; state is persisted on the host throughout the run."""

from contextlib import ExitStack
from dataclasses import dataclass
import json
import shlex
from pathlib import Path
import subprocess
import tempfile

from sandbox_operator_config import (ROOT, OPERATOR_HOME, file_sha, image_selection, plan_identity,
                                     provisioning_files, state_project)
from sandbox_operator_plan import validate_plan
from sandbox_base_image import DEFAULT_REPO
from sandbox_operator_runtime import Operator
from sandbox_operator_prepared import store_from_env
from sandbox_operator_state import deployment_lock, private_write, state_server
from sandbox_operator_tunnel import state_connection


PLAN = "operator.tfplan"
IDENTITY = "operator-plan.json"
PENDING = "operator-pending.json"


def write_json(path, value):
    private_write(path, (json.dumps(value, indent=2) + "\n").encode())


@dataclass
class PreparedInputs:
    args: object
    directory: Path
    image: Path
    fingerprint: str
    files: dict
    identity: dict


def require_recovered(directory):
    if (directory / PENDING).exists():
        pending = json.loads((directory / PENDING).read_text())
        raise ValueError(f"Unfinished operator {pending['project']} retained on {pending['remote']}. "
                         "Run sandbox-provision.py recover before another plan/apply.")


def initialize_variables(args, directory):
    if state_project(directory) is not None:
        return
    projects = subprocess.check_output(["incus", "project", "list", args.remote + ":", "--format=json"])
    if args.project in {project["name"] for project in json.loads(projects)}:
        raise ValueError("Incus project already exists but no matching state was found; refusing empty-state provisioning")
    defaults = directory / "operator.auto.tfvars.json"
    if not defaults.exists():
        write_json(defaults, {"project_name": args.project, "storage_pool": args.storage_pool,
                              "dev_network": args.network, "secured_runtime": True})


def require_saved_plan(directory, identity):
    previous = json.loads((directory / IDENTITY).read_text())
    for flag in ("replace", "destroy"):
        identity["options"][flag] = previous["options"].get(flag, False)
    expected = {key: value for key, value in previous.items() if key != "plan_sha256"}
    if identity != expected:
        raise ValueError("Provisioning inputs or server changed; run plan again")
    if file_sha(directory / PLAN) != previous["plan_sha256"]:
        raise ValueError("Saved plan changed; run plan again")


def prepare(args, directory, server):
    require_recovered(directory)
    initialize_variables(args, directory)
    image, fingerprint, variables = image_selection(directory, args.image_dir)
    files = provisioning_files(directory)
    options = {"remote": args.remote, "project": args.project,
               "pool": args.storage_pool, "network": args.network, "replace": args.replace, "destroy": args.destroy,
               "tools": sorted(getattr(args, "tools", None) or [])}
    identity = plan_identity(files, fingerprint, server["certificate_fingerprint"], options)
    identity["operator_lock"] = file_sha(ROOT / "infra/operator/tools.lock.json")
    identity["operator_runner"] = file_sha(ROOT / "infra/operator/run.py")
    # A plan can't change what the tool store installs without showing it.
    identity["tools_lock"] = file_sha(image.parent / "manifest.json") if (image.parent / "manifest.json").exists() else None
    identity["tools_activator"] = file_sha(ROOT / "infra/image/tool_store.py")
    if args.action == "apply":
        require_saved_plan(directory, identity)
    write_json(directory / "operator-image.json", {"variables": str(variables)})
    return PreparedInputs(args, directory, image, fingerprint, files, identity)


def stage(operator, temporary, prepared, token):
    args = prepared.args
    image_path = f"/operator/workspace-{prepared.fingerprint}.tar.gz"
    for name, content in prepared.files.items():
        source = temporary / name
        private_write(source, content)
        operator.push(source, "/operator/config/" + name)
    backend = temporary / "operator-backend.tf.json"
    write_json(backend, {"terraform": {"backend": {"http": {}}}})
    operator.push(backend, "/operator/config/operator-backend.tf.json")
    variables = temporary / "variables.json"
    write_json(variables, {"incus_socket": "/run/operator-incus.sock", "project_name": args.project,
                          "image_file": image_path, "image_fingerprint": prepared.fingerprint})
    operator.push(variables, "/operator/variables.json")
    settings = temporary / "settings.json"
    write_json(settings, {"token": token, "action": args.action, "replace": args.replace, "destroy": args.destroy})
    operator.push(settings, "/operator/settings.json")
    operator.push(ROOT / "infra/operator/run.py", "/operator/run.py")
    operator.push(prepared.image, image_path)
    if args.action == "apply":
        operator.push(prepared.directory / PLAN, "/operator/approved.tfplan")


def run_operation(args, directory, server):
    with deployment_lock(directory):
        run_locked_operation(args, directory, server)


def run_locked_operation(args, directory, server):
    prepared = prepare(args, directory, server)
    operator = Operator(args.remote, server, args.storage_pool, args.network,
                        prepared_store=store_from_env(args.remote))
    write_json(directory / PENDING, {"project": operator.project, "remote": args.remote,
                                    "action": args.action, "server": server["certificate_fingerprint"],
                                    "deployment_project": args.project})
    print(f"Host state: {directory / 'terraform.tfstate'}", flush=True)
    apply_started = False
    completed = False
    try:
        operator.create(getattr(args, "repo", DEFAULT_REPO))
        with ExitStack() as stack:
            temporary = Path(stack.enter_context(tempfile.TemporaryDirectory(prefix="pairtask-operator-")))
            operator.install(temporary)
            backend = stack.enter_context(state_server(directory))
            address = stack.enter_context(state_connection(backend.server_port, args.remote))
            stage(operator, temporary, prepared, backend.store.token)
            operator.connect(address)
            apply_started = args.action == "apply"
            try:
                operator.execute("python3", "/operator/run.py")
            except BaseException:
                if apply_started:
                    operator.run("stop", operator.target, "--force")
                raise
            complete_operation(operator, temporary, prepared)
            completed = True
    finally:
        if apply_started and not completed:
            print(f"Apply interrupted/failed. Host state is retained at {directory}. "
                  f"Operator {operator.project} is retained. Run sandbox-provision.py recover "
                  "to reconcile emergency state before retrying.", flush=True)
        else:
            operator.cleanup()
            (directory / PENDING).unlink()


def complete_operation(operator, temporary, prepared):
    directory, args = prepared.directory, prepared.args
    if args.action == "apply":
        (directory / PLAN).unlink()
        (directory / IDENTITY).unlink()
        return
    rendered = operator.execute("/operator/bin/tofu", "-chdir=/operator/config", "show", "-json",
                                "/operator/approved.tfplan", capture_output=True, text=True)
    validate_plan(json.loads(rendered.stdout), directory, OPERATOR_HOME, args.destroy)
    plan = temporary / PLAN
    operator.run("file", "pull", operator.target + "/operator/approved.tfplan", str(plan))
    private_write(directory / PLAN, plan.read_bytes())
    prepared.identity["plan_sha256"] = file_sha(directory / PLAN)
    write_json(directory / IDENTITY, prepared.identity)
    if getattr(args, "automatic", False):
        print("Plan validated; applying rollout.", flush=True)
        return
    command = ["python3", "scripts/sandbox-provision.py", "apply", "--project", args.project,
               "--remote", args.remote, "--storage-pool", args.storage_pool, "--network", args.network]
    print("Plan saved. Review it above, then run: " + shlex.join(command), flush=True)
