#!/usr/bin/env python3
"""Set up key-authenticated SSH through Incus; no sandbox NIC or TCP listener."""

import argparse
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import sys

from sandbox_layout import discover
from sandbox_remote import default_remote


DEFAULT_STATE = Path(__file__).resolve().parents[1] / "infra/incus/ssh"


def identifier(value):
    if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]*", value):
        raise argparse.ArgumentTypeError("Use a name without a colon, whitespace or shell syntax")
    return value


def run(args, **kwargs):
    return subprocess.run(args, check=True, **kwargs)


def private_state(directory, endpoint):
    if directory.is_symlink():
        raise ValueError("Refusing a symlinked SSH state directory")
    directory.mkdir(parents=True, exist_ok=True, mode=0o700)
    check_state_files(directory)
    marker = directory / "endpoint.json"
    previous = json.loads(marker.read_text()) if marker.exists() else endpoint
    if previous != endpoint:
        raise ValueError("SSH state belongs to another endpoint; choose a different --state-dir")
    directory.chmod(0o700)
    marker.write_text(json.dumps(endpoint) + "\n")


def check_state_files(directory):
    for name in ("endpoint.json", "id_ed25519", "id_ed25519.pub", "known_hosts", "config"):
        if (directory / name).is_symlink():
            raise ValueError(f"Refusing symlinked SSH state: {name}")


def pin_host_key(destination, public_key, refresh=False):
    fields = public_key.split()
    if len(fields) < 2 or fields[0] != "ssh-ed25519":
        raise ValueError("Incus did not return an Ed25519 SSH host key")
    expected = f"collab-workspace {fields[0]} {fields[1]}\n"
    changed = destination.exists() and destination.read_text() != expected
    if changed and not refresh:
        raise ValueError(f"SSH host key changed. Verify the workspace replacement, then remove {destination} and rerun setup.")
    destination.write_text(expected)


def ssh_config(directory, transport):
    # SSH expands percent tokens even inside quotes; state paths are literal.
    def quoted(path):
        return '"' + str(path).replace("\\", "\\\\").replace('"', '\\"').replace("%", "%%") + '"'
    proxy = shlex.join(transport).replace("%", "%%")
    return f"""Host workspace
  HostName workspace
  HostKeyAlias collab-workspace
  User agent
  IdentityFile {quoted(directory / 'id_ed25519')}
  UserKnownHostsFile {quoted(directory / 'known_hosts')}
  GlobalKnownHostsFile /dev/null
  StrictHostKeyChecking yes
  IdentitiesOnly yes
  IdentityAgent none
  BatchMode yes
  ForwardAgent no
  ForwardX11 no
  ClearAllForwardings yes
  ProxyCommand {proxy}
"""


class Instance:
    """The exec seam layout discovery probes through."""

    def __init__(self, name, execute):
        self.name, self.execute = name, execute

    def output(self, *args):
        return run(self.execute + list(args), capture_output=True, text=True).stdout


def setup(args):
    for tool in ("incus", "ssh", "ssh-keygen"):
        if not shutil.which(tool):
            raise ValueError(f"Install {tool} first")
    directory = args.state_dir.absolute()
    if "\n" in str(directory) or "\r" in str(directory):
        raise ValueError("SSH state path must not contain newlines")
    endpoint = {"remote": args.remote, "project": args.project, "instance": args.instance}
    private_state(directory, endpoint)
    execute = [shutil.which("incus"), "--project", args.project, "exec",
               f"{args.remote}:{args.instance}", "-T", "--"]
    # Before anything changes: which names this image uses. Any probe failure stops here.
    layout = discover(Instance(args.instance, execute))
    run(execute + ["test", "-f", layout.sshd_config])
    # Incus start may return before first-boot volume initialization finishes.
    run(execute + ["systemctl", "start", layout.units["workspace_setup"]])
    # Admission: no agent key before this root's tools are released (docs/sandbox-tools.md).
    if layout.version >= 2:
        run(execute + [layout.tools_ready])  # this layout always has the helper; missing fails closed
    else:
        # Only legacy images from before the tool store lack the readiness helper.
        run(execute + ["sh", "-c", 'test ! -x "$1" || exec "$1"', "ready", layout.tools_ready])
    run(execute + ["ssh-keygen", "-A"], stdout=subprocess.DEVNULL)
    host_key = run(execute + ["cat", "/etc/ssh/ssh_host_ed25519_key.pub"],
                   capture_output=True, text=True).stdout
    # Obtain trust from the already-authenticated Incus endpoint, never ssh-keyscan.
    pin_host_key(directory / "known_hosts", host_key, getattr(args, "refresh_host_key", False))
    identity = directory / "id_ed25519"
    if not identity.exists():
        run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "collab-workspace", "-f", str(identity)])
    identity.chmod(0o600)
    public_key = run(["ssh-keygen", "-y", "-f", str(identity)], capture_output=True, text=True).stdout
    # Root owns the authorization file. Only the PUBLIC key crosses into Incus.
    run(execute + ["install", "-d", "-m", "755", "/run/sshd", "/etc/ssh/authorized_keys"])
    run(execute + ["tee", "/etc/ssh/authorized_keys/agent"], input=public_key,
        text=True, stdout=subprocess.DEVNULL)
    run(execute + ["chmod", "644", "/etc/ssh/authorized_keys/agent"])
    run(execute + ["/usr/sbin/sshd", "-t", "-f", layout.sshd_config])
    # Incus start can return before systemd-tmpfiles has recreated /run/sshd.
    transport = execute + ["sh", "-c", "install -d -m 755 /run/sshd && "
                           f"exec /usr/sbin/sshd -i -e -o LogLevel=ERROR -f {layout.sshd_config}"]
    config = directory / "config"
    config.write_text(ssh_config(directory, transport))
    config.chmod(0o600)
    print(shlex.join(["ssh", "-F", str(config), "workspace"]))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--remote", default=default_remote(), type=identifier, help="Incus remote; defaults to colima-pairtask on macOS, local on Linux")
    parser.add_argument("--project", default="pairtask", type=identifier)
    parser.add_argument("--instance", default="workspace", type=identifier)
    parser.add_argument("--state-dir", type=Path, default=DEFAULT_STATE)
    args = parser.parse_args()
    os.umask(0o077)
    setup(args)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        print(f"sandbox-ssh: {error}", file=sys.stderr)
        sys.exit(1)
