"""Prove Pi in the offline workspace (#155): the pinned version, the start
plan's stable paths, then the real Pi in RPC mode with the pairtask
extension, against an isolated broker and a local model fixture. No
credentials, no network, no model calls."""

import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]
TESTS = ROOT / "integrations/pi/tests"
PLAN = ("/usr/local/bin/pi", "-e", "/usr/local/lib/pairtask/pi-extension/index.ts")


def pinned_pi():
    lock = json.loads((ROOT / "infra/image/pi/package-lock.json").read_text())
    return lock["packages"]["node_modules/@earendil-works/pi-coding-agent"]["version"]


def output(ssh, command, **kwargs):
    return subprocess.run(ssh + [command], capture_output=True, text=True, check=True, **kwargs).stdout


def verify(ssh):
    version = output(ssh, "pi --version", timeout=60).strip()
    if version != pinned_pi():
        raise ValueError(f"Unexpected Pi version: {version!r}, want {pinned_pi()}")
    check_start_plan(ssh)
    directory = output(ssh, "mktemp -d").strip()
    try:
        for name in ("host-smoke.mjs", "helpers.mjs"):
            subprocess.run(ssh + [f"cat > {directory}/{name}"], input=(TESTS / name).read_bytes(), check=True)
        subprocess.run(ssh + [f"cd {directory} && node --test host-smoke.mjs"], check=True, timeout=240)
    finally:
        subprocess.run(ssh + [f"rm -rf {directory}"], check=False, timeout=60)


def check_start_plan(ssh):
    """`pairtask start pi` runs the wrapper with the stable extension shim."""
    plan = output(ssh, "pairtask start pi --id pi-smoke --dry-run", timeout=60).splitlines()
    if tuple(plan[:3]) != PLAN:
        raise ValueError(f"Unexpected Pi start plan: {plan}")
