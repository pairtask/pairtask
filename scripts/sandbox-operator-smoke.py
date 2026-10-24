#!/usr/bin/env python3
"""Exercise containerized plan/apply/state/recovery using a disposable Incus project."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

from sandbox_releases import repository


ROOT = Path(__file__).resolve().parents[1]


def setup(directory, project):
    state, image = directory / "state", directory / "image"
    state.mkdir()
    image.mkdir()
    archive = image / "workspace.tar.gz"
    archive.write_bytes(b"Operator fixture: no agent container is launched\n")
    (image / "image.tfvars.json").write_text(json.dumps({"image_file": str(archive),
        "image_fingerprint": hashlib.sha256(archive.read_bytes()).hexdigest()}))
    for name in ("versions.tf", ".terraform.lock.hcl", "secured_preflight.py"):
        shutil.copy(ROOT / "infra/incus" / name, state / name)
    variables = ["incus_socket", "project_name", "image_file", "image_fingerprint", "storage_pool", "dev_network"]
    config = '\n'.join(f'variable "{name}" {{ type = string }}' for name in variables)
    config += '''
variable "secured_runtime" { type = bool }
provider "incus" {
  default_remote = "sandbox"
  remote {
    name = "sandbox"
    address = "unix://${var.incus_socket}"
  }
}
resource "incus_project" "proof" {
  name = var.project_name
  description = "Disposable operator proof; no agents"
}
output "operator_paths" {
  value = { image = var.image_file, socket = var.incus_socket }
}
'''
    (state / "main.tf").write_text(config)
    # Existing deployments retain host paths in auto-loaded variable files.
    (state / "sandbox.auto.tfvars.json").write_text(json.dumps({
        "image_file": str(archive), "incus_socket": "/var/lib/incus/unix.socket"}))
    return state, image


def invoke(directory, args, action, extra=()):
    command = [sys.executable, str(ROOT / "scripts/pairtask.py"), action,
               "--remote", args.remote, "--state-dir", str(directory / "state"), "--project", args.project,
               "--repo", args.repo]
    env = dict(os.environ, PAIRTASK_OPERATOR_HOME=str(directory / "registry"))
    return subprocess.run(command + list(extra), env=env, check=False)


def prove(directory, args):
    state, image = setup(directory, args.project)
    invoke(directory, args, "plan", ["--image-dir", str(image)]).check_returncode()
    invoke(directory, args, "apply").check_returncode()
    saved = state / "terraform.tfstate"
    assert saved.exists() and saved.stat().st_mode & 0o777 == 0o600
    before = json.loads(saved.read_text())
    assert before["outputs"]["operator_paths"]["value"] == {
        "image": f"/operator/workspace-{hashlib.sha256((image / 'workspace.tar.gz').read_bytes()).hexdigest()}.tar.gz",
        "socket": "/run/operator-incus.sock"}
    assert not (state / "operator-pending.json").exists()

    # The native process fails after a stateful resource is known to exist.
    (state / "failure.tf").write_text('''resource "terraform_data" "failure" {
  provisioner "local-exec" {
    command = "exit 42"
  }
}
''')
    invoke(directory, args, "plan").check_returncode()
    assert invoke(directory, args, "apply").returncode != 0
    pending = json.loads((state / "operator-pending.json").read_text())
    status = subprocess.check_output(["incus", "--project", pending["project"], "list", args.remote + ":", "--format=json"])
    assert all(instance["status"] == "Stopped" for instance in json.loads(status))
    assert json.loads(saved.read_text())["lineage"] == before["lineage"]
    assert (state / "terraform.tfstate.backup").exists()
    invoke(directory, args, "recover").check_returncode()
    assert not (state / "operator-pending.json").exists()
    print("PASS: operator plan/apply, private host state/backup, failed-apply retention and recovery; no host tofu used.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--repo", type=repository, default="pairtask/pairtask",
                        help="Repository holding the base-<fingerprint> mirror releases")
    args = parser.parse_args()
    args.project = "pairtask-operator-proof-" + uuid.uuid4().hex[:8]
    directory = Path(tempfile.mkdtemp(prefix=args.project + "-"))
    try:
        prove(directory, args)
    except BaseException:
        print(f"Proof failed; retained state and recovery metadata at {directory}", flush=True)
        raise
    subprocess.run(["incus", "project", "delete", args.remote + ":" + args.project], check=True)
    shutil.rmtree(directory)


if __name__ == "__main__":
    main()
