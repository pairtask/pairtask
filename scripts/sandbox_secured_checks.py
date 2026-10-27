"""Exercise the persistent two-runtime deployment in a disposable smoke project."""

import json
from pathlib import Path
import subprocess
import time

import sandbox_idmap_checks
import sandbox_installed_checks
import sandbox_broker_checks
from sandbox_boundary_checks import report, stop_relay


STATE = "/var/lib/collab-ai-secured"
STATUS = "/mnt/collab-status/status.sock"
ENV = ["env", f"PAIRTASK_BUDGET_DIR={STATE}/budgets"]


def run(command, **kwargs):
    result = subprocess.run(command, capture_output=True, text=True, timeout=120, **kwargs)
    if result.returncode:
        raise ValueError(f"Secured smoke command failed: {command}\n{result.stdout}\n{result.stderr}")
    return result.stdout


def denied(command):
    result = subprocess.run(command, capture_output=True, timeout=10)
    if result.returncode == 0:
        raise ValueError(f"Forbidden operation succeeded: {command}")


class Deployment:
    def __init__(self, args, directory, project):
        self.remote = args.remote + ":"
        self.base = ["incus", "--project", project]
        self.tofu = [args.tofu, f"-chdir={directory}"]
        self.args, self.project = args, project

    def activate(self):
        """After an apply that may create or replace a root: a store-mode root
        admits agents only once the host has activated its tools."""
        import sandbox_rollout
        sandbox_rollout.activate_running(self.args.remote, self.project, self.args.image_dir)

    def execute(self, role, *command, uid=0):
        return self.base + ["exec", self.remote + role, "-T", "--user", str(uid),
                            "--group", str(uid), "--", *command]

    def status(self):
        return self.execute("workspace", "env", "PAIRTASK_BUDGET_DIR=/tmp/decoy", "pairtask", "budget", "status",
                            "smoke", "--socket", STATUS, "--json", uid=1001)

    def snapshot(self):
        return json.loads(run(self.execute("secured", *ENV, "pairtask", "budget", "status", "smoke", "--json")))


def check_configuration(deployment):
    check_volume_permissions(deployment)
    instances = {item["name"]: item for item in json.loads(run(
        deployment.base + ["list", deployment.remote, "--format", "json"]))}
    for role in ("workspace", "secured"):
        check_offline(deployment, role)
    sandbox_idmap_checks.check_disjoint({role: instances[role] for role in ("workspace", "secured")})
    devices = instances["secured"]["expanded_devices"]
    if set(devices) != {"root", "secured-state", "budget-status", "dev-executor", "broker-ipc", "secured-tools"}:
        raise ValueError("Secured inherited unexpected devices")
    denied(deployment.execute("secured", "systemctl", "is-active", "--quiet", "collab-broker"))


def await_permission_setup(deployment):
    for _ in range(40):
        result = subprocess.run(deployment.execute("secured", "systemctl", "is-active", "--quiet", "collab-secured-setup"),
                                capture_output=True, timeout=5)
        if result.returncode == 0:
            return
        time.sleep(0.25)
    diagnostic = run(deployment.execute("secured", "journalctl", "-b", "--no-pager", "-n", "80"))
    raise ValueError(f"Secured volume permission setup did not complete: {diagnostic}")


def check_volume_permissions(deployment):
    await_permission_setup(deployment)
    for path, expected in ((STATE, "710 0 1002"), ("/mnt/collab-status", "755 0 0")):
        actual = run(deployment.execute("secured", "stat", "-c", "%a %u %g", path)).strip()
        if actual != expected:
            raise ValueError(f"Unsafe secured volume permissions: {path}: {actual}")


def check_offline(deployment, role):
    for path in ("/var/lib/incus/unix.socket", "/dev/incus/sock", "/var/run/docker.sock"):
        run(deployment.execute(role, "test", "!", "-S", path))
    links = json.loads(run(deployment.execute(role, "ip", "-json", "link")))
    if [link["ifname"] for link in links] != ["lo"]:
        raise ValueError(f"{role} unexpectedly has networking")


def prepare_budget(deployment):
    run(deployment.execute("secured", *ENV, "pairtask", "budget", "create", "smoke", "--tokens", "100"))
    source = Path(__file__).parent / "boundary/relay.py"
    run(deployment.base + ["file", "push", str(source), deployment.remote + "secured/opt/smoke-relay.py"])
    run(deployment.execute("secured", "sh", "-c",
        "printf '#!/bin/sh\nexec python3 /opt/smoke-relay.py fake-codex\n' >/opt/smoke-codex; chmod 755 /opt/smoke-codex"))
    return deployment.execute("secured", *ENV, "/usr/local/lib/pairtask/pairtask-codex", "--agent-id", "smoke", "--budget", "smoke",
                              "--budget-status-socket", STATUS, "--codex", "/opt/smoke-codex")


def await_status(deployment):
    for _ in range(30):
        result = subprocess.run(deployment.status(), capture_output=True, text=True, timeout=5)
        if result.returncode == 0:
            snapshot = json.loads(result.stdout)
            if snapshot["reported_tokens"] == 60:
                return
        time.sleep(0.2)
    raise ValueError("Read-only status socket did not become ready")


def check_denials(deployment):
    for uid in (0, 1001):
        for command in (["touch", "/mnt/collab-status/forbidden"], ["rm", STATUS],
                        ["cat", f"{STATE}/budgets/smoke.json"],
                        ["cat", f"/proc/1/root{STATE}/budgets/smoke.json"]):
            denied(deployment.execute("workspace", *command, uid=uid))
    for options in ("remount,rw", "remount,bind,rw"):
        denied(deployment.execute("workspace", "mount", "-o", options, "/mnt/collab-status"))
    denied(deployment.execute("secured", "cat", f"{STATE}/budgets/smoke.json", uid=1001))
    denied(deployment.execute("secured", "touch", "/mnt/collab-status/forbidden", uid=1001))
    # Creating a same-name budget in dev must not change the mounted authority.
    run(deployment.execute("workspace", "env", "PAIRTASK_BUDGET_DIR=/tmp/decoy", "pairtask", "budget", "create",
                           "smoke", "--tokens", "9999", uid=1001))
    snapshot = json.loads(run(deployment.status()))
    if (snapshot["cap"], snapshot["reported_tokens"]) != (100, 60):
        raise ValueError("Dev changed the authoritative snapshot")


def check_persistence(deployment):
    sandbox_broker_checks.await_broker(deployment)
    pending = sandbox_broker_checks.snapshot(deployment, "workspace")["durable_pending"]
    for running in (False, True):
        run(deployment.tofu + ["apply", "-auto-approve", "-input=false", f"-var=running={str(running).lower()}"])
        deployment.activate()
        instances = json.loads(run(deployment.base + ["list", deployment.remote, "--format", "json"]))
        expected = "Running" if running else "Stopped"
        if {item["status"] for item in instances} != {expected}:
            raise ValueError("Desired power state did not apply to both runtimes")
    check_saved_budget(deployment)
    # Replacement destroys the secured root disk; the private custom volume survives.
    run(deployment.tofu + ["apply", "-auto-approve", "-input=false", "-replace=incus_instance.secured[0]"])
    deployment.activate()
    check_saved_budget(deployment)
    sandbox_broker_checks.await_broker(deployment)
    if sandbox_broker_checks.snapshot(deployment, "workspace")["durable_pending"] != pending:
        raise ValueError("Control replacement lost broker inbox state")
    run(deployment.tofu + ["plan", "-input=false", "-detailed-exitcode"])


def check_saved_budget(deployment):
    snapshot = deployment.snapshot()
    if (snapshot["cap"], snapshot["reported_tokens"]) != (100, 60):
        raise ValueError("Lifecycle operation changed persisted accounting")
    denied(deployment.execute("secured", *ENV, "pairtask", "budget", "create", "smoke", "--tokens", "9999"))
    denied(deployment.status())


def enable_secured(deployment, directory):
    path = directory / "sandbox.auto.tfvars.json"
    variables = json.loads(path.read_text())
    variables["secured_runtime"] = True
    path.write_text(json.dumps(variables))
    check_hot_upgrade_rejected(deployment)
    run(deployment.base + ["stop", deployment.remote + "workspace"])
    run(deployment.tofu + ["apply", "-auto-approve", "-input=false"])
    deployment.activate()
    run(deployment.tofu + ["plan", "-input=false", "-detailed-exitcode"])
    run(deployment.execute("workspace", "test", "-f", "/workspace/restart-check"))
    print("PASS: enabling secured runtime preserves dev data and produces an unchanged second plan.", flush=True)


def check_hot_upgrade_rejected(deployment):
    result = subprocess.run(deployment.tofu + ["apply", "-auto-approve", "-input=false"],
                            capture_output=True, text=True, timeout=120)
    if result.returncode == 0:
        raise ValueError("Provisioning accepted a hot upgrade")
    if "ValueError: Stop workspace before enabling" not in result.stderr:
        raise ValueError(f"Upgrade failed for an unexpected reason: {result.stderr}")
    run(deployment.execute("workspace", "test", "!", "-e", "/mnt/collab-status"))


def verify(args, directory, project):
    deployment = Deployment(args, directory, project)
    enable_secured(deployment, directory)
    check_configuration(deployment)
    sandbox_broker_checks.verify(deployment)
    command = prepare_budget(deployment)
    # Inherit the test log; undrained output pipes could stall the launcher.
    process = subprocess.Popen(command, stdin=subprocess.PIPE, text=True)
    try:
        report(process, 60)
        await_status(deployment)
        check_denials(deployment)
        print("PASS: read-only UDS status and private-state denials for dev root and agent.", flush=True)
    finally:
        stop_relay(process)
        run(deployment.base + ["restart", deployment.remote + "secured"])
    check_persistence(deployment)
    sandbox_installed_checks.verify(deployment)
    sandbox_broker_checks.verify(deployment)
    print("PASS: isolated runtimes, private persistent state, shared broker, read-only status, stop/start and secured replacement.")
