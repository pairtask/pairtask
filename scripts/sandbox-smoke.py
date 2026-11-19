#!/usr/bin/env python3
"""Provision an exported image, verify offline SSH and restart, then destroy the test deployment."""

import argparse
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

import sandbox_mount_checks
import sandbox_rollout
import sandbox_network_checks
import sandbox_secured_checks
import sandbox_storage_checks
import sandbox_workspace_checks
import sandbox_rollout_checks
import sandbox_tools_update_checks


ROOT = Path(__file__).resolve().parents[1]


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def local_socket(remote):
    result = run(["incus", "remote", "list", "--format", "json"], capture_output=True, text=True)
    connection = json.loads(result.stdout)[remote]
    addresses = connection.get("Addrs", [connection.get("Addr")])
    if len(addresses) != 1:
        raise ValueError("Smoke tests require one explicit local Incus socket")
    address = addresses[0]
    if not address.startswith("unix://"):
        raise ValueError("Smoke tests require a local Incus Unix socket")
    return address.removeprefix("unix://") or "/var/lib/incus/unix.socket"


def prepare_state(directory, args, project):
    for source in (ROOT / "infra/incus").glob("*.tf"):
        shutil.copyfile(source, directory / source.name)
    shutil.copyfile(ROOT / "infra/incus/.terraform.lock.hcl", directory / ".terraform.lock.hcl")
    shutil.copyfile(ROOT / "infra/incus/secured_preflight.py", directory / "secured_preflight.py")
    manifest = json.loads((args.image_dir / "manifest.json").read_text())
    variables = {
        "incus_socket": local_socket(args.remote), "project_name": project,
        "image_file": str((args.image_dir / "workspace.tar.gz").resolve()),
        "image_fingerprint": manifest["image_fingerprint"],
        "dev_network_enabled": False,
    }
    if getattr(args, "network_check", False):
        variables["dev_network"] = sandbox_network_checks.prepare(directory, project)
    (directory / "sandbox.auto.tfvars.json").write_text(json.dumps(variables))
    return manifest


def destroy_deployment(tofu, directory, project):
    try:
        sandbox_storage_checks.allow_test_teardown(directory, project)
        run(tofu + ["destroy", "-auto-approve", "-input=false"])
    except BaseException:
        print(f"Cleanup failed; retained deployment state for {project} at {directory}", file=sys.stderr)
        raise
    shutil.rmtree(directory)


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", required=True)
    parser.add_argument("--image-dir", required=True, type=Path)
    parser.add_argument("--tofu", default="tofu", help="OpenTofu or Terraform executable")
    parser.add_argument("--mount-check", action="store_true", help="also test disposable local Linux host mounts")
    parser.add_argument("--network-check", action="store_true", help="also test dev network enable/disable using only the bridge DNS service")
    parser.add_argument("--secured-check", action="store_true", help="also test persistent secured state and read-only status IPC")
    parser.add_argument("--tools-update-check", action="store_true", help="also prove real installed client update and offline rollback")
    return parser.parse_args()


def check_deployment(args, directory, project, manifest):
    sandbox_workspace_checks.check_workspace(args, directory, project, manifest)
    sandbox_storage_checks.prepare(args, directory, project)
    if args.secured_check:
        sandbox_secured_checks.verify(args, directory, project)
    if args.mount_check:
        sandbox_mount_checks.verify(args, directory, project)
    sandbox_storage_checks.verify(args, directory, project)
    if args.network_check:
        sandbox_network_checks.verify(args, directory, project)
    sandbox_rollout_checks.verify(args, directory, project)
    if args.tools_update_check:
        sandbox_tools_update_checks.verify(args, directory, project)


def main():
    args = parse_args()
    project = "pairtask-smoke-" + uuid.uuid4().hex[:12]
    directory = Path(tempfile.mkdtemp(prefix="pairtask-smoke-"))
    manifest = prepare_state(directory, args, project)
    if args.mount_check:
        sandbox_mount_checks.prepare(directory, args.remote)
    tofu = [args.tofu, f"-chdir={directory}"]
    run(tofu + ["init", "-input=false", "-lockfile=readonly"])
    try:
        run(tofu + ["apply", "-auto-approve", "-input=false"])
        run(tofu + ["plan", "-detailed-exitcode", "-input=false"])
        # A store-mode root admits agents only after the host activates its tools.
        sandbox_rollout.activate_running(args.remote, project, args.image_dir)
        check_deployment(args, directory, project, manifest)
        print("PASS: verified image, unchanged plan, offline SSH, tool versions and restart persistence.")
    except Exception:
        subprocess.run(["incus", "--project", project, "info", f"{args.remote}:workspace", "--show-log"],
                       check=False, timeout=30)
        raise
    finally:
        destroy_deployment(tofu, directory, project)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-smoke: {error}", file=sys.stderr)
        sys.exit(1)
