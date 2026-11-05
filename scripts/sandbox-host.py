#!/usr/bin/env python3
"""Bootstrap the dedicated macOS Colima host; Incus resources belong to Terraform."""

import argparse
import json
import os
from pathlib import Path
import platform
import shlex
import shutil
import subprocess
import sys

from sandbox_mounts import colima_mounts, mount_variables, read_mounts, write_json
from sandbox_colima_config import check_settings
from sandbox_incus_version import check as check_server_version, query_server
from sandbox_remote import PROFILE, REMOTE


CONFIG = Path(__file__).resolve().parents[1] / "infra/incus/colima.json"


def desired_config():
    config = json.loads(CONFIG.read_text())
    architecture = {"arm64": "aarch64", "x86_64": "x86_64"}.get(platform.machine())
    if architecture is None:
        raise ValueError("Unsupported Mac architecture")
    config["arch"] = architecture
    return config


def profile_config(directory):
    base = desired_config()
    if not directory.exists():
        return base
    # Check ownership and drift before adopting only the managed mount list.
    check_profile_files(directory)
    config = json.loads((directory / "pairtask-owner.json").read_text())["config"]
    if {**config, "mounts": None} != base:
        raise ValueError("Host settings changed; reconcile the existing profile explicitly before proceeding")
    check_profile(directory, config)
    return config


def check_profile(directory, config):
    check_profile_files(directory)
    if not directory.exists():
        return
    marker = directory / "pairtask-owner.json"
    settings = directory / "colima.yaml"
    if json.loads(marker.read_text()) != {"schema": 1, "config": config}:
        raise ValueError("Host settings changed; reconcile the existing profile explicitly before proceeding")
    check_settings(settings, config)


def check_profile_files(directory):
    if directory.is_symlink():
        raise ValueError("Refusing a symlinked Colima profile")
    if not directory.exists():
        return
    marker = directory / "pairtask-owner.json"
    settings = directory / "colima.yaml"
    if not marker.is_file() or not settings.is_file():
        raise ValueError("Existing pairtask profile is not managed by this script; refusing to adopt it")
    if marker.is_symlink() or settings.is_symlink():
        raise ValueError("Refusing symlinked profile configuration")


def apply_profile(directory, config):
    check_profile(directory, config)
    if directory.exists():
        return
    directory.mkdir(mode=0o700, parents=True)
    # JSON is valid YAML; ordinary Colima commands may later save expanded YAML.
    with (directory / "colima.yaml").open("x") as stream:
        json.dump(config, stream, indent=2)
        stream.write("\n")
    with (directory / "pairtask-owner.json").open("x") as stream:
        json.dump({"schema": 1, "config": config}, stream, indent=2)
        stream.write("\n")


def check_remote(socket, required=False):
    result = subprocess.run(["incus", "remote", "list", "--format", "json"],
                            capture_output=True, text=True, check=True, timeout=30)
    remote = json.loads(result.stdout).get(REMOTE)
    if remote is None and not required:
        return
    # Older Incus clients expose Addr; newer clients expose an Addrs list.
    addresses = remote_addresses(remote)
    if addresses != [f"unix://{socket}"] or remote.get("Protocol") != "incus":
        raise ValueError(f"Incus remote {REMOTE} does not point exclusively to {socket}; refusing to use it")


def remote_addresses(remote):
    if remote is None:
        return []
    return remote.get("Addrs", [remote.get("Addr")])


def start_host(directory, config, command):
    require_host_tools()
    socket = directory / "incus.sock"
    check_remote(socket)
    apply_profile(directory, config)
    subprocess.run(command, check=True, timeout=900)
    if not socket.is_socket():
        raise ValueError(f"Colima did not expose its Incus socket: {socket}")
    check_remote(socket, required=True)
    version = check_server_version(query_server(REMOTE), REMOTE)
    print(f"Incus {version} ready. Set incus_socket = {json.dumps(str(socket))}")


def require_host_tools():
    for executable in ("colima", "incus"):
        if shutil.which(executable) is None:
            raise ValueError(f"Install {executable} first; see docs/sandbox.md")


def host_directory():
    if os.environ.get("COLIMA_HOME"):
        raise ValueError("Unset COLIMA_HOME; this bootstrap uses the standard ~/.colima location")
    return Path.home() / ".colima" / PROFILE


def bootstrap(args):
    if platform.system() != "Darwin":
        raise ValueError("This bootstrap is for macOS; on Linux use your initialized Incus server (docs/sandbox.md)")
    directory = host_directory()
    config = profile_config(directory)
    check_profile(directory, config)
    command = start_command(config)
    print(json.dumps(config, indent=2))
    print(shlex.join(command), flush=True)
    if args.action == "plan":
        print("Preview only: no files written or VM started. Apply checks the Incus server version once the VM is up.")
        return
    start_host(directory, config, command)


def start_command(config):
    command = ["colima", "start", PROFILE, "--save-config=false", "--template=false",
               "--activate=false", "--ssh-config=false", "--ssh-agent=false"]
    if not config["mounts"]:
        command += ["--mount", "none"]
    return command


def require_stopped():
    result = subprocess.run(["colima", "list", "--json"], capture_output=True,
                            text=True, check=True, timeout=30)
    profiles = [json.loads(line) for line in result.stdout.splitlines() if line.strip()]
    for profile in profiles:
        if profile["name"] == PROFILE and profile["status"] != "Stopped":
            raise ValueError("Stop the dedicated VM first: colima stop pairtask")


def configure_profile_mounts(mounts):
    directory = host_directory()
    current = profile_config(directory)
    desired = {**current, "mounts": colima_mounts(mounts)}
    if desired == current:
        return
    require_stopped()
    apply_profile(directory, current)
    write_json(directory / "colima.yaml", desired)
    write_json(directory / "pairtask-owner.json", {"schema": 1, "config": desired})


def configure_mounts(args):
    if args.mounts_file is None:
        raise ValueError("--mounts-file is required for mount configuration")
    mounts = read_mounts(args.mounts_file)
    variables = mount_variables(mounts, args.share_user, platform.system())
    print(json.dumps(variables, indent=2))
    if args.action == "mounts-plan":
        print("Preview only. macOS also shares exactly these paths through Colima.")
        return
    if platform.system() == "Darwin":
        configure_profile_mounts(mounts)
    output = mount_output(args)
    write_json(output, variables)
    print(f"Wrote {output}. Run sandbox-provision.py plan, then apply; macOS: start the dedicated host first.")


def mount_output(args):
    if args.output is not None:
        return args.output
    from sandbox_operator_config import deployment_directory, remember
    directory = deployment_directory(None, args.project)
    remember(directory, args.project)
    return directory / "mounts.auto.tfvars.json"


def parse_args():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["plan", "apply", "mounts-plan", "mounts-apply", "incus-plan", "incus-apply"])
    parser.add_argument("--mounts-file", type=Path, help="JSON list of project_name/host_path/container_mount_path/container_readonly mount objects")
    parser.add_argument("--output", type=Path, help="Override the discovered deployment mount-variable file")
    parser.add_argument("--project", default="pairtask", help="Deployment project for automatic state discovery")
    parser.add_argument("--share-user", help="Dedicated local Linux sharing account; required for writable mounts")
    parser.add_argument("--manifest", type=Path, help="incus-plan writes, incus-apply reads, this upgrade manifest")
    parser.add_argument("--yes", action="store_true", help="incus-apply: change the VM's Incus as the manifest plans")
    return parser.parse_args()


def main():
    args = parse_args()
    if args.action.startswith("mounts-"):
        configure_mounts(args)
    elif args.action.startswith("incus-"):
        incus_upgrade(args)
    else:
        bootstrap(args)


def incus_upgrade(args):
    """Plan or apply an upgrade of the VM's held Incus packages (#312)."""
    import sandbox_incus_upgrade as upgrade
    if platform.system() != "Darwin":
        raise ValueError("Incus upgrades here are for the macOS Colima VM; on Linux, upgrade the host's Incus "
                         "from the repository you installed it from (docs/sandbox.md)")
    if args.manifest is None:
        raise ValueError(f"{args.action} needs --manifest FILE")
    print(UPGRADE_SCOPE, flush=True)
    if args.action == "incus-plan":
        manifest = upgrade.plan(upgrade.Runner())
        upgrade.write_manifest(args.manifest, manifest)
        print(json.dumps(manifest, indent=2))
        print(f"Preview only: nothing in the VM changed. Wrote {args.manifest}." if not manifest["blockers"]
              else "Blocked: " + "; ".join(manifest["blockers"]))
        return
    try:
        steps = upgrade.apply(upgrade.Runner(), json.loads(args.manifest.read_text()), args.yes)
    except upgrade.UpgradeError as err:
        print_steps(err.steps)
        backups = [step.detail for step in err.steps if step.ok and step.name == "backup readable"]
        if backups:
            print(f"Backup: {backups[-1]} (kept; nothing was restored, downgraded or deleted). "
                  "To restore it, follow docs/sandbox.md, Upgrade the VM's Incus.")
        raise
    print_steps(steps)


def print_steps(steps):
    for step in steps:
        print(f"{'ok  ' if step.ok else 'FAIL'} {step.name} {step.detail}".rstrip(), flush=True)


UPGRADE_SCOPE = ("Upgrades the held Incus packages of the pairtask Colima VM only, with Incus stopped and "
                 "/var/lib/incus backed up first. Only self-contained dir storage under /var/lib/incus is "
                 "supported; any other pool stops the plan for a manual upgrade.")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-host: {error}", file=sys.stderr)
        sys.exit(1)
