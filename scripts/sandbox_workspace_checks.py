"""Verify installed tools, interactive shells and restart over sandbox SSH."""

import json
from pathlib import Path
import subprocess
import sys
import time

import sandbox_skill_checks
import sandbox_rtk_checks
import sandbox_docker_checks
import sandbox_mcp_checks
import sandbox_pi_checks


ROOT = Path(__file__).resolve().parents[1]


def run(command, **kwargs):
    return subprocess.run(command, check=True, **kwargs)


def wait_for_broker(ssh, cli="pairtask"):
    """cli is the status command of the image under test (collab before layout 2)."""
    for _ in range(30):
        result = subprocess.run(ssh + [cli + " status --json"], capture_output=True, text=True)
        if result.returncode == 0:
            if json.loads(result.stdout)["health"] == "ready":
                return
        time.sleep(1)
    raise ValueError(f"Broker did not become ready: {result.stderr} {result.stdout}")


def pinned(lock, name):
    return lock["artifacts"][name]["version"]


def check_versions(ssh, lock):
    commands = {
        "codex --version": f"codex-cli {pinned(lock, 'codex')}\n",
        "claude --version": f"{pinned(lock, 'claude')} (Claude Code)",
        "go version": f"go version go{pinned(lock, 'go')} ",
        "starship --version": f"starship {pinned(lock, 'starship')}\n",
        "rtk --version": f"rtk {pinned(lock, 'rtk')}\n",
        "gh --version": "gh version ",
        "node --version": f"v{pinned(lock, 'node')}\n",
    }
    for command, expected in commands.items():
        output = run(ssh + [command], capture_output=True, text=True).stdout
        if not output.startswith(expected):
            raise ValueError(f"Unexpected installed tool version: {command}")


def check_offline_image(execute):
    links = json.loads(run(execute + ["ip", "-json", "link"], capture_output=True, text=True).stdout)
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError("Workspace unexpectedly has a network interface")
    key_check = run(execute + ["find", "/etc/ssh", "-name", "ssh_host_*"], capture_output=True, text=True)
    if key_check.stdout.strip():
        raise ValueError("Image shipped SSH host keys")


def check_agent_aliases(ssh, lock):
    # Exercise installed login and non-login shell startup, without model requests.
    for flags in ("-ic", "-lic"):
        command = ('test "$(type -t codex)" = alias && test "$(type -t claude)" = alias && '
                   'test "$(type -t dashboard)" = alias && '
                   'test "${STARSHIP_SHELL-}" = bash && codex --version && claude --version')
        result = run(ssh + ["bash", flags, "'" + command + "'"], capture_output=True, text=True)
        if f"codex-cli {pinned(lock, 'codex')}" not in result.stdout or \
                f"{pinned(lock, 'claude')} (Claude Code)" not in result.stdout:
            raise ValueError("Interactive agent aliases did not reach the installed native tools")


def check_workspace(args, directory, project, manifest):
    target = f"{args.remote}:workspace"
    execute = ["incus", "--project", project, "exec", target, "-T", "--"]
    check_offline_image(execute)
    ssh_dir = directory / "ssh"
    run([sys.executable, str(ROOT / "scripts/sandbox-ssh.py"), "--remote", args.remote,
         "--project", project, "--state-dir", str(ssh_dir)])
    ssh = ["ssh", "-F", str(ssh_dir / "config"), "workspace"]
    uid = run(ssh + ["id -u"], capture_output=True, text=True).stdout.strip()
    if uid == "0":
        raise ValueError("SSH must log in as an unprivileged user")
    check_versions(ssh, manifest["tools"])
    sandbox_skill_checks.verify(ssh, manifest)
    run(["ssh", "-tt", "-F", str(ssh_dir / "config"), "workspace", "test -t 0 && test -t 1"],
        stdin=subprocess.DEVNULL)
    wait_for_broker(ssh)
    check_agent_aliases(ssh, manifest["tools"])
    startup = (ROOT / "scripts/codex/startup.py").read_text()
    run(ssh + ["python3 -"], input=startup, text=True, timeout=40)
    sandbox_rtk_checks.verify(ssh)
    sandbox_docker_checks.verify(ssh, create=True)
    sandbox_mcp_checks.verify(ssh)
    sandbox_pi_checks.verify(ssh)
    run(ssh + ["touch /workspace/restart-check"])
    run(["incus", "--project", project, "stop", target])
    run(["incus", "--project", project, "start", target])
    wait_for_broker(ssh)
    run(ssh + ["test -f /workspace/restart-check"])
    sandbox_rtk_checks.verify(ssh)
    sandbox_docker_checks.verify(ssh)
