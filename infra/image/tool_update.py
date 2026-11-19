"""In-place updates of the agent clients on a released root (pairtask update).

A fresh root is activated by tool_store.Store.activate, which refuses a
released one. An update is narrower: it only ever adds a generation and
moves `current` to it, never edits one, and only for the agent clients. The
host drives it as a journaled transaction over both containers; this module
is one container's half, each step idempotent and bound to the host's
update ID:

    stage   verify and extract artifacts, build the generation; no state change
    smoke   run the updated clients as an unprivileged user
    close   state maintenance: no new tool starts, running ones continue
    flip    current -> the staged generation; still maintenance
    verify  report what is current, for the host to reconcile
    release state ready, for whatever is current
    restore current -> a generation that validates (the way back)
    abort   remove this update's evidence, and a generation it created

Evidence of preparation lives under updates/<id>/<generation>/, never inside
a generation, so a reused generation is never written to. Nothing is pruned
here: a session started on an older generation keeps its files until the
next upgrade replaces the root.
"""

import json
import os
from pathlib import Path
import hashlib
import shutil
import subprocess
import tempfile

import tool_lock
import tool_smoke
import tool_store
from tool_store import StoreError, file_sha256, generation_id, read_json, write_durable, write_state

CHECK_USER = "nobody"  # unprivileged in every image; owns nothing but its run's home
CHECK_VERSION = "v1"
OVERRIDE_FILE = "override-lock.json"
SMOKE_TIMEOUT = 60


class UpdateError(StoreError):
    """A refused update step; code is stable for the host to act on."""

    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


class Updates:
    """One container's update steps over its tool store."""

    def __init__(self, store=None):
        self.store = store or tool_store.Store()
        # The release's own lock and build source, from the store's share
        # directory, so an isolated store never reads the live root's.
        self.lock_file = self.store.share / "tools.lock.json"
        self.source_file = self.store.share / "build-source.json"
        self.updates = self.store.root / "updates"
        self.overrides = self.store.root / "overrides"
        # Generations that have been current, recorded outside them: a
        # generation is never written to once built.
        self.released = self.store.root / "released"

    # Locking: every mutation is exclusive; launchers and verify share.

    def _locked(self, exclusive=True):
        return tool_store.StoreLock(self.store.root, exclusive)

    def evidence(self, update_id, generation):
        return self.updates / safe_id(update_id) / safe_id(generation)

    # Steps

    def stage(self, manifest, incoming, update_id):
        """Build the manifest's generation if it isn't there, and record that
        this update prepared it. Changes no state and flips nothing."""
        tool_store.validate_manifest(manifest)
        generation = generation_id(manifest)
        with self._locked():
            self.require_released()
            self.accept_override(manifest, Path(incoming))
            self.require_clients_only(manifest)
            built = not (self.store.generations / generation / "manifest.json").exists()
            if built:
                self.build(generation, manifest, Path(incoming))
            # A retry keeps what this update recorded the first time.
            earlier = read_json(self.evidence(update_id, generation) / "staged.json") or {}
            fresh = built or bool(earlier.get("fresh"))
            self.check_generation(generation, manifest)
            write_durable(self.evidence(update_id, generation) / "staged.json", dump(
                {"update": update_id, "generation": generation, "manifest_sha256": digest_of(manifest), "fresh": fresh}))
        return generation

    def restage(self, generation, update_id):
        """Stage a generation this store already holds, from its own
        manifest: the way back needs no delivery."""
        return self.stage(self.manifest_of(generation), self.store.root / "incoming", update_id)

    def build(self, generation, manifest, incoming):
        for name, tool in manifest["tools"].items():
            self.store.stage(name, tool, incoming)
        self.store.build_generation(generation, manifest)

    def smoke(self, generation, update_id, check=None):
        """Run each updated client unprivileged; record the exact binaries."""
        with self._locked():
            staged = self.staged(update_id, generation)
            manifest = self.manifest_of(generation)
            if digest_of(manifest) != staged["manifest_sha256"]:
                raise UpdateError("evidence-mismatch", "The staged generation changed since it was staged")
            binaries = {}
            for name in clients_in(manifest):
                entry = self.entry(generation, manifest, name)
                self.check(check, name, manifest["tools"][name], entry)
                binaries[name] = file_sha256(entry)
            record = {"update": update_id, "generation": generation, "manifest_sha256": staged["manifest_sha256"],
                      "binaries": binaries, "check": CHECK_VERSION}
            write_durable(self.evidence(update_id, generation) / "smoked.json", dump(record))
        return record

    def check(self, injected, name, tool, entry):
        """An injected check takes the client alone; the default one also
        reads the Codex config this store derived, not the live root's."""
        if injected:
            injected(name, tool, entry)
        else:
            smoke_check(name, tool, entry, etc=self.store.etc)

    def close(self, update_id):
        """Shut admission: maintenance for this update, current untouched."""
        with self._locked():
            state = self.state()
            if state.get("state") == "maintenance" and state.get("update") == update_id:
                return state
            self.require_released(state)
            closed = {"state": "maintenance", "update": update_id, "current": self.store.current_generation()}
            write_state(closed, self.store.state_path)
            return closed

    def flip(self, generation, update_id):
        """Move current to a generation this update staged and smoked."""
        with self._locked():
            self.prepared(update_id, generation)
            return self.move(generation, update_id)

    def restore(self, generation, update_id):
        """Move current back to a generation whose manifest validates."""
        with self._locked():
            self.check_generation(generation, self.manifest_of(generation))
            return self.move(generation, update_id)

    def move(self, generation, update_id):
        state = self.closed_for(update_id)
        predecessor = state.get("predecessor") or state.get("current") or self.store.current_generation()
        # A generation that has been current is never this update's to remove.
        write_durable(self.released / generation, dump({"update": update_id}))
        if self.store.current_generation() != generation:
            self.store.flip(generation)
        moved = {"state": "maintenance", "update": update_id, "current": generation, "flipped": generation,
                 "predecessor": predecessor}
        write_state(moved, self.store.state_path)
        return moved

    def verify(self, update_id):
        """What is current here, for the host to reconcile; changes nothing."""
        with self._locked(exclusive=False):
            state = self.state()
            current = self.store.current_generation()
            return {"state": state.get("state"), "update": state.get("update"), "current": current,
                    "predecessor": state.get("predecessor"), "entries_ok": self.entries_ok(current)}

    def release(self, update_id):
        """Reopen admission on whatever is current, once its entries resolve."""
        with self._locked():
            state = self.state()
            current = self.store.current_generation()
            if (state.get("state"), state.get("update"), state.get("generation")) == ("ready", update_id, current):
                return state
            self.closed_for(update_id)
            if not self.entries_ok(current):
                raise UpdateError("entries", "The current generation's entries don't resolve")
            released = {"state": "ready", "generation": current, "update": update_id}
            write_state(released, self.store.state_path)
            return released

    def abort(self, update_id):
        """Remove this update's evidence, and any generation it created that
        isn't current; never another generation."""
        with self._locked():
            directory = self.updates / safe_id(update_id)
            for staged in sorted(directory.glob("*/staged.json")) if directory.is_dir() else []:
                record = read_json(staged, {})
                generation = record.get("generation", "")
                if record.get("fresh") and not self.in_use(generation):
                    shutil.rmtree(self.store.generations / safe_id(generation), ignore_errors=True)
            shutil.rmtree(directory, ignore_errors=True)

    def in_use(self, generation):
        """A generation that is or has been current, or that the recorded
        state still names as flipped or as the one to go back to."""
        if (self.released / safe_id(generation)).exists():
            return True
        state = self.state()
        return generation in (self.store.current_generation(), state.get("current"), state.get("flipped"),
                              state.get("predecessor"), state.get("generation"))

    # Checks

    def state(self):
        return tool_store.read_state(self.store.state_path) or {}

    def require_released(self, state=None):
        """An update only applies to a root already released to agents."""
        state = self.state() if state is None else state
        if state.get("state") == "maintenance" and state.get("update"):
            raise UpdateError("foreign-state", "Another update holds this root in maintenance")
        if state.get("state") != "ready":
            raise UpdateError("not-released", "This root was never released; activate it with a rollout")

    def closed_for(self, update_id):
        state = self.state()
        if state.get("state") != "maintenance" or state.get("update") != update_id:
            raise UpdateError("not-closed", "This root isn't closed for this update")
        return state

    def staged(self, update_id, generation):
        return self.evidence_file(update_id, generation, "staged.json")

    def smoked(self, update_id, generation):
        return self.evidence_file(update_id, generation, "smoked.json")

    def evidence_file(self, update_id, generation, name):
        record = read_json(self.evidence(update_id, generation) / name)
        if not record or (record.get("update"), record.get("generation")) != (update_id, generation):
            raise UpdateError("no-evidence", f"This update has no {name} for generation {generation}")
        return record

    def prepared(self, update_id, generation):
        """This update's evidence still describes what is on disk: the
        manifest it staged and the exact binaries it smoked."""
        staged, smoked = self.staged(update_id, generation), self.smoked(update_id, generation)
        manifest = self.manifest_of(generation)
        if not digest_of(manifest) == staged["manifest_sha256"] == smoked.get("manifest_sha256"):
            raise UpdateError("evidence-mismatch", "The generation's manifest changed since it was prepared")
        if not smoke_covers(smoked, manifest):
            raise UpdateError("evidence-mismatch", "The smoke check doesn't cover this generation's clients")
        self.same_binaries(generation, manifest, smoked["binaries"])

    def same_binaries(self, generation, manifest, binaries):
        """Each client on disk is still the file that was smoke-checked."""
        for name, sha in binaries.items():
            if file_sha256(self.entry(generation, manifest, name)) != sha:
                raise UpdateError("evidence-mismatch", f"{name} changed since it was smoke-checked")

    def require_clients_only(self, manifest):
        """The manifest is the current one with nothing changed but the
        agent clients, and those are the override lock's for this machine."""
        current = self.store.current_manifest()
        if current is None:
            raise UpdateError("not-released", "This root has no current generation")
        require_same_root(manifest, current)
        lock = self.manifest_lock(manifest)
        for name, tool in manifest["tools"].items():
            if tool != expected_tool(name, current, lock):
                raise UpdateError("override", f"An update can't change {name} beyond its pinned client version")

    def manifest_lock(self, manifest):
        """The lock the manifest was made from: the override's, or the image's."""
        if manifest.get("override") is None:
            return json.loads(self.lock_file.read_text())
        return json.loads((self.overrides / (manifest["lock_sha256"] + ".json")).read_text())

    def manifest_of(self, generation):
        manifest = read_json(self.store.generations / safe_id(generation) / "manifest.json")
        if manifest is None:
            raise UpdateError("no-generation", f"Generation {generation} isn't in this store")
        return manifest

    def check_generation(self, generation, manifest):
        """The directory is this manifest's, and every tool's entry is there."""
        if generation_id(manifest) != generation or self.manifest_of(generation) != manifest:
            raise UpdateError("evidence-mismatch", "The generation doesn't match its manifest")
        if not self.entries_ok(generation):
            raise UpdateError("entries", "A tool's entry is missing from the generation")

    def entries_ok(self, generation):
        if not generation:
            return False
        manifest = read_json(self.store.generations / generation / "manifest.json")
        if manifest is None or generation_id(manifest) != generation:
            return False
        return all(tool["kind"] == "debs" or self.entry(generation, manifest, name).is_file()
                   for name, tool in manifest["tools"].items())

    def entry(self, generation, manifest, name):
        tool = manifest["tools"][name]
        return (self.store.generations / generation / name).resolve() / tool_store.normalized(tool["entry"])

    # Overrides

    def accept_override(self, manifest, incoming):
        """Keep the host's override lock under the store, once it is the
        image's lock with nothing changed but the clients."""
        if manifest.get("override") is None:
            return self.require_image_lock(manifest)
        self.keep_override(manifest["lock_sha256"], incoming / OVERRIDE_FILE)
        reason = tool_store.override_reason(manifest, self.lock_file, self.overrides)
        if reason:
            raise UpdateError("override", reason)
        return None

    def require_image_lock(self, manifest):
        if manifest["lock_sha256"] != file_sha256(self.lock_file):
            raise UpdateError("override", "The manifest names another lock and carries no override")

    def keep_override(self, digest, source):
        """Store a delivered override lock under its digest, once."""
        target = self.overrides / (digest + ".json")
        if target.exists():
            return
        if not delivered(source, digest):
            raise UpdateError("override", "The override lock wasn't delivered or doesn't match its digest")
        write_durable(target, source.read_text())


def delivered(source, digest):
    return source.is_file() and file_sha256(source) == digest


def smoke_covers(smoked, manifest):
    """The recorded check is this version's and names exactly the clients."""
    if smoked.get("check") != CHECK_VERSION:
        return False
    return set(smoked.get("binaries", {})) == set(clients_in(manifest))


def require_same_root(manifest, current):
    """An update keeps the release, the architecture, the selection and
    the set of tools."""
    for field in ("release", "architecture", "selection"):
        if manifest.get(field) != current.get(field):
            raise UpdateError("override", f"An update can't change the {field}")
    if set(manifest["tools"]) != set(current["tools"]):
        raise UpdateError("override", "An update can't add or remove a tool")


def expected_tool(name, current, lock):
    """What a tool's entry must be: unchanged, or for an agent client the
    lock's artifact for this machine."""
    if name not in tool_lock.LANE_TOOLS:
        return current["tools"][name]
    return tool_store.tool_entry(tool_lock.artifact(lock, name, current["architecture"]))


def clients_in(manifest):
    return [name for name in tool_lock.LANE_TOOLS if name in manifest["tools"]]


def smoke_check(name, tool, entry, etc=tool_store.ETC):
    """Start one updated client as the unprivileged check user, in a home
    made for this run and removed after it: it must name its version,
    Claude must also print its help, and Codex must also answer initialize
    with the config this root derives."""
    home = tempfile.mkdtemp(prefix="pairtask-tools-check-")
    try:
        shutil.chown(home, CHECK_USER)
        check_version(name, tool, entry, home)
        if name == "claude":
            run_dropped(entry, "--help", home)
        if name == "codex":
            check_codex(tool, entry, home, Path(etc) / "codex/config.toml")
    except (OSError, LookupError, subprocess.SubprocessError, tool_smoke.SmokeError, ValueError) as error:
        raise UpdateError("smoke", f"{name} failed its check: {error}") from error
    finally:
        shutil.rmtree(home, ignore_errors=True)


def run_dropped(entry, argument, home):
    """One short run of entry as the check user with an empty environment
    but its private home; its output, or a finding when it exits non-zero."""
    command = ["runuser", "-u", CHECK_USER, "--", "env", "-i", f"HOME={home}", "PATH=/usr/bin:/bin", str(entry), argument]
    result = subprocess.run(command, capture_output=True, text=True, timeout=SMOKE_TIMEOUT, check=False)
    if result.returncode != 0:
        raise tool_smoke.SmokeError(f"{argument} exited {result.returncode}")
    return result.stdout


def check_version(name, tool, entry, home):
    if not tool_smoke.reports(tool.get("version", ""), run_dropped(entry, "--version", home)):
        raise tool_smoke.SmokeError(f"{name} didn't report version {tool.get('version')}")


# runuser options end at "--", so the candidate is what it executes; the
# handshake's own environment (private HOME and CODEX_HOME) is kept.
CODEX_DROP = ("runuser", "-u", CHECK_USER, "--preserve-environment", "--")


def check_codex(tool, entry, home, config):
    """The App Server's initialize answer, started as pairtask-codex starts
    it, in a CODEX_HOME the check user owns."""
    how = tool_smoke.Handshake(SMOKE_TIMEOUT, client="pairtask-update", prefix=CODEX_DROP, owner=CHECK_USER)
    result = tool_smoke.codex_handshake(entry, home, config, how)
    if not tool_smoke.reports(tool.get("version", ""), result.get("userAgent", "")):
        raise tool_smoke.SmokeError("codex's App Server reports another version")


def digest_of(manifest):
    return hashlib.sha256(json.dumps(manifest, sort_keys=True).encode()).hexdigest()


def dump(value):
    return json.dumps(value, indent=2, sort_keys=True) + "\n"


def plain(character):
    return character.isalnum() or character in "-_"


def safe_id(value):
    """An ID used as one path component."""
    if not value or not all(map(plain, value)):
        raise UpdateError("bad-id", "An update or generation ID must be letters, digits, - or _")
    return value


def resolve_launch(name, store=None, environment=None):
    """What the launcher execs: the entry and the child's environment, both
    captured under the shared store lock together with the readiness check,
    so a flip can't land between them. Returns (entry, env); raises
    UpdateError("maintenance") when tools may not start."""
    store = store or tool_store.Store()
    with tool_store.launch_lock(store):
        reason = tool_store.ready(store, lock_file=store.share / "tools.lock.json",
                                  source_file=store.share / "build-source.json", mode_file=store.share / "tools-mode")
        if reason:
            raise UpdateError("maintenance", reason)
        entry = store.resolve_name(name)
        if entry is None or not entry.is_file():
            raise UpdateError("missing", f"{name} is not installed in this sandbox")
        env = dict(os.environ if environment is None else environment)
        env["PATH"] = f"{entry.parent}:{env.get('PATH', '/usr/bin:/bin')}"
        if name in ("go", "gofmt"):
            env["GOROOT"] = str(entry.parent.parent)
        return entry, env
