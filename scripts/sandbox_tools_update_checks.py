"""Prove real client updates in an installed, disposable Incus deployment."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

from sandbox_tools_install import Container
from sandbox_tools_plan import Instance, Resolver, STORE, clients_of
import sandbox_tools_update as update

ROOT = Path(__file__).resolve().parents[1]
# Both versions are published immutable artifacts. Choose the other when the
# image itself already carries the older one; never resolve a moving latest.
CLAUDE_VERSIONS = ("2.1.283", "2.1.327")


def require(condition, detail):
    if not condition:
        raise ValueError("Installed client update proof: " + detail)


def emit(phase, **evidence):
    print(json.dumps({"proof": "installed-client-update", "phase": phase, **evidence}, sort_keys=True), flush=True)


def versions(manifest):
    return {name: manifest["tools"][name]["version"] for name in clients_of(manifest)}


def requested_versions(before):
    wanted = {}
    for record in before.values():
        for name, version in versions(record["manifest"]).items():
            require(wanted.get(name, version) == version, "installed client versions disagree between roles")
            wanted[name] = version
    require("claude" in wanted, "the installed stores have no Claude client")
    wanted["claude"] = CLAUDE_VERSIONS[1] if wanted["claude"] == CLAUDE_VERSIONS[0] else CLAUDE_VERSIONS[0]
    return wanted


class InstalledUpdate:
    def __init__(self, args, directory, project):
        self.args, self.directory, self.project = args, Path(directory), project
        self.instances = {role: Instance(Container(args.remote, project, role)) for role in update.ROLES}
        self.journal = update.journal_in(self.directory)

    def snapshots(self):
        result = {}
        for role, instance in self.instances.items():
            state = instance.activate_plain("status")
            require(state.get("mode") == "store", role + " is not a store-mode installation")
            require(state.get("current"), role + " has no current generation")
            result[role] = {"uuid": instance.uuid, "current": state["current"], "manifest": state["manifest"],
                            "provenance": instance.provenance(), "architecture": instance.container.output("uname", "-m").strip()}
        return result

    def offline_containers(self):
        # prepare_state sets dev_network_enabled=false; the optional network
        # check ends disabled, and rollout preserves that saved configuration.
        for role, instance in self.instances.items():
            links = json.loads(instance.container.output("ip", "-json", "link"))
            require([link["ifname"] for link in links] == ["lo"], role + " has a network interface")

    def forward(self, wanted):
        work = update.Update(self.journal, self.instances, Resolver(self.instances))
        record = work.of({"project": self.project, "remote": self.args.remote}).run(wanted)
        require((record["phase"], record.get("outcome")) == ("done", "candidate"), "forward update failed: " + str(record))
        return record

    def smoke_evidence(self, record):
        result = {}
        for role, instance in self.instances.items():
            generation = record["containers"][role]["candidate"]
            path = "{}/updates/{}/{}/smoked.json".format(STORE, record["id"], generation)
            evidence = json.loads(instance.read(path))
            require(evidence.get("update") == record["id"], role + " smoke record belongs to another transaction")
            require(evidence.get("generation") == generation, role + " smoke record names another generation")
            require(evidence.get("check"), role + " smoke record lacks its check identity")
            require(evidence.get("binaries"), role + " smoke record lacks binary digests")
            result[role] = evidence
        return result

    def ready(self, record, expected):
        for role, instance in self.instances.items():
            seen = instance.activate("verify", "--update", record["id"])
            require(seen.get("current") == expected[role], role + " runs another generation")
            require(seen.get("state") == "ready", role + " was not released")
            require(seen.get("entries_ok") is True, role + " entries do not verify")
            instance.container.output(instance.container.layout.tools_ready)

    def observe_clients(self, snapshots):
        instance = self.instances["workspace"]
        output = instance.container.output("timeout", "60", "runuser", "-u", "agent", "--", "claude", "--version")
        version = versions(snapshots["workspace"]["manifest"])["claude"]
        require(re.search(r"(?<![\w.])" + re.escape(version) + r"(?![\w.])", output),
                "the user-facing Claude launcher did not report " + version)
        emit("launcher", user="agent", command="claude --version", output=output.strip())

    def overrides(self, snapshots):
        for role, snapshot in snapshots.items():
            manifest = snapshot["manifest"]
            require(manifest.get("override"), role + " candidate lacks an override")
            lock = self.instances[role].read(STORE + "/overrides/" + manifest["lock_sha256"] + ".json")
            require(hashlib.sha256(lock.encode()).hexdigest() == manifest["lock_sha256"],
                    role + " override lock digest differs")

    def rollback_offline(self):
        command = ["unshare", "-n", "--", sys.executable, str(ROOT / "scripts/sandbox-tools-update.py"),
                   "--rollback", "--remote", self.args.remote, "--project", self.project,
                   "--state-dir", str(self.directory)]
        try:
            result = subprocess.run(command, capture_output=True, text=True, timeout=300,
                                    env=dict(os.environ, PAIRTASK_ALLOW_ROOT="1"))
        except subprocess.TimeoutExpired as error:
            raise ValueError("Offline rollback timed out: {} {}".format(error.stdout, error.stderr)) from error
        require(result.returncode == 0, "offline rollback failed: " + result.stdout + result.stderr)
        require("Rolled back" in result.stdout, "rollback CLI did not report completion: " + result.stdout)
        with self.journal:
            record = self.journal.load()
        require(record is not None, "offline rollback wrote no journal")
        require((record["kind"], record["phase"], record.get("outcome")) == ("rollback", "done", "candidate"),
                "offline rollback did not complete")
        emit("rollback-command", host_network="disabled by unshare -n", output=result.stdout.strip())
        return record

    def retained(self, candidates):
        for role, generation in candidates.items():
            instance = self.instances[role]
            instance.container.output("test", "-d", STORE + "/generations/" + generation)
            kept = instance.activate_plain("status").get("retained")
            require(kept and kept["generations"] >= 2 and kept["stored_bytes"] > 0,
                    role + " does not report its retained generations")
            emit("retained", role=role, **kept)


def pointers(snapshots):
    return {role: record["current"] for role, record in snapshots.items()}


def unchanged_instances(before, after):
    for role in update.ROLES:
        require(after[role]["uuid"] == before[role]["uuid"], role + " container was replaced")
        require(after[role]["provenance"] == before[role]["provenance"], role + " image provenance changed")


def verify(args, directory, project):
    require(project.startswith(("pairtask-smoke-", "collab-smoke-")), "requires a disposable smoke project")
    require(shutil.which("unshare"), "unshare is required for the offline rollback proof")
    subprocess.run(["unshare", "-n", "--", "true"], check=True)
    proof = InstalledUpdate(args, directory, project)
    proof.offline_containers()
    before = proof.snapshots()
    wanted = requested_versions(before)
    emit("before", stores=before, requested=wanted)
    forward = proof.forward(wanted)
    after = proof.snapshots()
    unchanged_instances(before, after)
    for role, snapshot in after.items():
        selected = {name: wanted[name] for name in clients_of(snapshot["manifest"])}
        require(versions(snapshot["manifest"]) == selected, role + " did not install the requested versions")
    proof.overrides(after)
    candidates = pointers(after)
    require(all(candidates[role] != before[role]["current"] for role in update.ROLES), "both stores must change generation")
    proof.ready(forward, candidates)
    proof.observe_clients(after)
    emit("updated", stores=after, smoke=proof.smoke_evidence(forward))
    backward = proof.rollback_offline()
    restored = proof.snapshots()
    unchanged_instances(before, restored)
    require(pointers(restored) == pointers(before), "offline rollback did not restore both original generations")
    for role in update.ROLES:
        require(versions(restored[role]["manifest"]) == versions(before[role]["manifest"]),
                role + " rollback did not restore the original client versions")
    proof.ready(backward, pointers(before))
    proof.observe_clients(restored)
    proof.retained(candidates)
    emit("rolled-back", stores=restored, smoke=proof.smoke_evidence(backward), candidate_generations_retained=True)
    print("PASS: installed Incus client update, unprivileged client smoke, unchanged instances, offline rollback and retention.", flush=True)
