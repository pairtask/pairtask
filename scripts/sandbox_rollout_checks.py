"""Exercise automatic replacement against an existing disposable deployment."""

import os
import json
from pathlib import Path
import shutil
import subprocess
import sys

from sandbox_secured_checks import Deployment
from sandbox_operator_config import file_sha
from sandbox_storage_checks import check_data
from sandbox_workspace_checks import wait_for_broker


ROOT = Path(__file__).resolve().parents[1]


def changed_image(source, destination):
    """Give the same tested rootfs a new archive identity without rebuilding tools."""
    destination.mkdir()
    archive = destination / "workspace.tar.gz"
    shutil.copyfile(source / "workspace.tar.gz", archive)
    with archive.open("r+b") as stream:
        header = stream.read(10)
        if header[:3] != b"\x1f\x8b\x08" or header[3] & 2:
            raise ValueError("Upgrade proof expects gzip without a header checksum")
        stream.seek(4)
        stream.write(((int.from_bytes(header[4:8], "little") + 1) % 2**32).to_bytes(4, "little"))
    fingerprint = file_sha(archive)
    (destination / "image.tfvars.json").write_text(json.dumps({
        "image_file": str(archive.resolve()), "image_fingerprint": fingerprint}))
    # Same rootfs, same tools: the upgrade keeps the release's bundle source.
    for metadata in [*source.glob("collab-ai-bundles-linux-*.json"), source / "bundle-source.json"]:
        if metadata.exists():
            shutil.copyfile(metadata, destination / metadata.name)
    return fingerprint


def rollout(args, directory, project, image):
    subprocess.run([sys.executable, str(ROOT / "scripts/pairtask.py"), "upgrade",
        "--remote", args.remote, "--project", project, "--state-dir", str(directory),
        "--image-dir", str(image.resolve()), "--ssh-state-dir", str(directory / "ssh")],
        env=dict(os.environ, PAIRTASK_OPERATOR_HOME=str(directory / "registry")), check=True)


def require_imported_image(directory, fingerprint):
    state = json.loads((directory / "terraform.tfstate").read_text())
    imported = [instance["attributes"]["fingerprint"] for resource in state["resources"]
                if resource["type"] == "incus_image" and resource["name"] == "workspace"
                for instance in resource["instances"]]
    if imported != [fingerprint]:
        raise ValueError("Rollout did not import the changed image")


def verify(args, directory, project):
    if not project.startswith(("pairtask-smoke-", "collab-smoke-")):
        raise ValueError("Rollout proof requires a disposable smoke project")
    ssh = directory / "ssh"
    identity = (ssh / "id_ed25519").read_bytes()
    previous_host = (ssh / "known_hosts").read_bytes()
    rollout(args, directory, project, args.image_dir)
    # A second operator must see changed bytes as a different image resource.
    upgrade = directory / "upgrade-image"
    fingerprint = changed_image(args.image_dir, upgrade)
    rollout(args, directory, project, upgrade)
    require_imported_image(directory, fingerprint)
    if (ssh / "id_ed25519").read_bytes() != identity:
        raise ValueError("Rollout changed the private login key")
    if (ssh / "known_hosts").read_bytes() == previous_host:
        raise ValueError("Rollout did not renew the replaced workspace host key")
    check_data(Deployment(args, directory, project))
    wait_for_broker(["ssh", "-F", str(ssh / "config"), "workspace"])
    print("PASS: different-image upgrade retains projects/home and login key; renewed SSH reaches the broker.", flush=True)
