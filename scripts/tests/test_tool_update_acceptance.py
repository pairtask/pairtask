"""Container transaction acceptance: real stores, local harmless artifacts."""

import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
from test_tool_store import StoreHarness, digest
import tool_store
import tool_update


def encoded(value):
    return (json.dumps(value, sort_keys=True) + "\n").encode()


def tree_contents(root):
    return {str(p.relative_to(root)): ("link", os.readlink(p)) if p.is_symlink()
            else ("file", p.read_bytes(), p.stat().st_mode & 0o777)
            for p in root.rglob("*") if p.is_file() or p.is_symlink()}


class UpdateAcceptance(StoreHarness):
    def setUp(self):
        super().setUp()
        self.store.share.mkdir()
        self.lock = self.store.share / "tools.lock.json"
        self.source = self.store.share / "build-source.json"
        self.mode = self.store.share / "tools-mode"
        self.source.write_text("{}")
        self.mode.write_text("store\n")
        self.base_lock = {"schema": 2, "architectures": {"aarch64": {"base_image": "pinned"}}, "artifacts": {}}
        tools = {}
        for name in ("claude", "codex", "cs-mcp", "go"):
            data = b"#!/bin/sh\nprintf '1.0.0\\n'\n"
            # Distinct bytes make accidental cross-tool substitution observable.
            data += ("# " + name + "\n").encode()
            tools.update(self.deliver(name, data, kind="binary", entry=name))
            self.base_lock["artifacts"][name] = {
                "version": "1.0.0", "kind": "binary", "entry": name,
                "url": "https://example.invalid/{version}/" + name + ".bin",
                "architectures": {"aarch64": {"arch": "arm64", "sha256": digest(data)}}}
        self.lock.write_bytes(encoded(self.base_lock))
        self.base_manifest = self.manifest(tools)
        self.base_manifest["lock_sha256"] = digest(self.lock.read_bytes())
        self.old = self.store.activate(self.base_manifest, self.incoming)
        self.updates = tool_update.Updates(self.store)

    def candidate(self, version="2.0.0"):
        manifest, lock = copy.deepcopy(self.base_manifest), copy.deepcopy(self.base_lock)
        data = ("#!/bin/sh\nprintf '" + version + "\\n'\n").encode()
        manifest["tools"].update(self.deliver("claude", data, kind="binary", entry="claude", version=version))
        lock["artifacts"]["claude"]["version"] = version
        lock["artifacts"]["claude"]["architectures"]["aarch64"]["sha256"] = digest(data)
        payload = encoded(lock)
        (self.incoming / "override-lock.json").write_bytes(payload)
        manifest["lock_sha256"] = digest(payload)
        manifest["override"] = {"base_lock_sha256": digest(self.lock.read_bytes())}
        return manifest

    def staged(self, update="update-a", version="2.0.0"):
        return self.updates.stage(self.candidate(version), self.incoming, update)

    def smoked(self, update="update-a", version="2.0.0"):
        generation = self.staged(update, version)
        self.updates.smoke(generation, update, check=lambda *_: None)
        return generation

    def commit(self, update="update-a", version="2.0.0"):
        generation = self.smoked(update, version)
        self.updates.close(update)
        self.updates.flip(generation, update)
        self.updates.release(update)
        return generation

    def evidence(self, generation, name, update="update-a"):
        return self.store.root / "updates" / update / generation / name

    def test_staging_preserves_ready_old_bytes_and_directory_access(self):
        state = self.store.state_path.read_bytes()
        old_files = tree_contents(self.store.generations / self.old)
        old_entry = self.store.resolve("claude")
        old_bytes = old_entry.read_bytes()
        generation = self.staged()
        self.assertNotEqual(generation, self.old)
        self.assertEqual(self.store.current_generation(), self.old)
        self.assertEqual(self.store.state_path.read_bytes(), state)
        self.assertEqual(tree_contents(self.store.generations / self.old), old_files)
        self.assertEqual(old_entry.read_bytes(), old_bytes)
        for directory in (self.store.artifacts, self.store.generations):
            self.assertEqual(directory.stat().st_mode & 0o777, 0o755)
        self.assertEqual(subprocess.check_output([old_entry], text=True).strip(), "1.0.0")
        self.assertIsNone(self.ready())

    def test_stage_checksum_failure_preserves_live_state(self):
        manifest = self.candidate()
        state = self.store.state_path.read_bytes()
        old_files = tree_contents(self.store.artifacts)
        (self.incoming / "claude.bin").write_bytes(b"wrong delivery")
        with self.assertRaises(tool_store.StoreError):
            self.updates.stage(manifest, self.incoming, "bad-delivery")
        self.assertEqual(self.store.state_path.read_bytes(), state)
        self.assertEqual(tree_contents(self.store.artifacts), old_files)
        self.assertEqual(self.store.current_generation(), self.old)

    def test_repeated_stage_keeps_creation_ownership_for_abort(self):
        manifest = self.candidate()
        generation = self.updates.stage(manifest, self.incoming, "update-a")
        self.updates.stage(manifest, self.incoming, "update-a")
        evidence = json.loads(self.evidence(generation, "staged.json").read_text())
        self.assertTrue(evidence["fresh"])
        self.updates.abort("update-a")
        self.assertFalse((self.store.generations / generation).exists())
        self.assertTrue((self.store.generations / self.old).exists())
        self.assertIsNone(self.ready())

    def test_reused_generation_is_never_rewritten_or_removed_by_abort(self):
        manifest_path = self.store.generations / self.old / "manifest.json"
        before, inode = tree_contents(self.store.generations / self.old), manifest_path.stat().st_ino
        generation = self.updates.stage(self.base_manifest, self.incoming, "reuse")
        self.assertEqual(generation, self.old)
        self.updates.smoke(generation, "reuse", check=lambda *_: None)
        self.updates.abort("reuse")
        self.assertEqual(tree_contents(self.store.generations / self.old), before)
        self.assertEqual(manifest_path.stat().st_ino, inode)

    def test_foreign_close_and_missing_smoke_never_move_current(self):
        generation = self.staged()
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError) as error:
            self.updates.close("update-b")
        self.assertEqual(error.exception.code, "foreign-state")
        for update in ("update-a", "update-b"):
            with self.subTest(update=update), self.assertRaises(tool_store.StoreError):
                self.updates.flip(generation, update)
        self.assertEqual(self.store.current_generation(), self.old)
        self.assertEqual(tool_store.read_state(self.store.state_path)["update"], "update-a")

    def test_smoke_failure_cannot_authorize_flip(self):
        generation = self.staged()
        def fail(*_):
            raise tool_store.StoreError("fixture smoke failure")
        with self.assertRaises(tool_store.StoreError):
            self.updates.smoke(generation, "update-a", check=fail)
        self.assertFalse(self.evidence(generation, "smoked.json").exists())
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError):
            self.updates.flip(generation, "update-a")
        self.assertEqual(self.store.current_generation(), self.old)

    def test_changed_binary_after_smoke_refuses_flip(self):
        generation = self.smoked()
        entry = (self.store.generations / generation / "claude").resolve() / "claude"
        entry.write_bytes(b"#!/bin/sh\nexit 42\n")
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError):
            self.updates.flip(generation, "update-a")
        self.assertEqual(self.store.current_generation(), self.old)

    def test_changed_manifest_after_smoke_refuses_flip(self):
        generation = self.smoked()
        path = self.store.generations / generation / "manifest.json"
        manifest = json.loads(path.read_text())
        manifest["selection"] = {"docker": True}
        path.write_bytes(encoded(manifest))
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError):
            self.updates.flip(generation, "update-a")
        self.assertEqual(self.store.current_generation(), self.old)

    def test_smoke_record_binds_manifest_and_exact_binary(self):
        generation = self.smoked()
        record = json.loads(self.evidence(generation, "smoked.json").read_text())
        staged = json.loads(self.evidence(generation, "staged.json").read_text())
        self.assertEqual(record.get("manifest_sha256"), staged["manifest_sha256"])
        entry = (self.store.generations / generation / "claude").resolve() / "claude"
        self.assertEqual(record["binaries"]["claude"], hashlib.sha256(entry.read_bytes()).hexdigest())

    def test_protected_manifest_changes_rejected_with_valid_override_lock(self):
        for field, value in (("release", "0" * 64), ("selection", {"docker": True}),
                             ("architecture", "x86_64")):
            with self.subTest(field=field):
                manifest = self.candidate()
                manifest[field] = value
                with self.assertRaises(tool_store.StoreError):
                    self.updates.stage(manifest, self.incoming, "bad-" + field)
        manifest = self.candidate()
        manifest["tools"]["go"] = copy.deepcopy(manifest["tools"]["claude"])
        with self.assertRaises(tool_store.StoreError):
            self.updates.stage(manifest, self.incoming, "bad-tool")
        self.assertEqual(self.store.current_generation(), self.old)
        self.assertIsNone(self.ready())

    def test_repeated_updates_preserve_captured_old_executable(self):
        old_entry = self.store.resolve("claude")
        first = self.commit()
        second = self.commit("update-b", "3.0.0")
        self.assertEqual({p.name for p in self.store.generations.iterdir()}, {self.old, first, second})
        self.assertEqual(subprocess.check_output([old_entry], text=True).strip(), "1.0.0")
        self.assertEqual(subprocess.check_output([self.store.resolve("claude")], text=True).strip(), "3.0.0")
        self.assertIsNone(self.ready())

    def test_restore_requires_own_close_and_leaves_admission_shut(self):
        generation = self.commit()
        with self.assertRaises(tool_store.StoreError):
            self.updates.restore(self.old, "restore-a")
        self.assertEqual(self.store.current_generation(), generation)
        self.updates.close("restore-a")
        self.updates.restore(self.old, "restore-a")
        state = self.updates.verify("restore-a")
        self.assertEqual(state["state"], "maintenance")
        self.assertEqual(state["current"], self.old)
        self.assertTrue(state["entries_ok"])
        self.assertIsNotNone(self.ready())
        self.updates.release("restore-a")
        self.assertIsNone(self.ready())

    def test_incomplete_smoke_evidence_cannot_authorize_flip(self):
        generation = self.smoked()
        path = self.evidence(generation, "smoked.json")
        record = json.loads(path.read_text())
        record["binaries"] = {}
        path.write_bytes(encoded(record))
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError):
            self.updates.flip(generation, "update-a")
        self.assertEqual(self.store.current_generation(), self.old)

    def test_unknown_smoke_check_cannot_authorize_flip(self):
        generation = self.smoked()
        path = self.evidence(generation, "smoked.json")
        record = json.loads(path.read_text())
        record["check"] = "obsolete-check"
        path.write_bytes(encoded(record))
        self.updates.close("update-a")
        with self.assertRaises(tool_store.StoreError):
            self.updates.flip(generation, "update-a")
        self.assertEqual(self.store.current_generation(), self.old)

    def test_late_abort_never_deletes_a_previously_released_generation(self):
        first = self.commit()
        self.commit("update-b", "3.0.0")
        before = tree_contents(self.store.generations / first)
        self.updates.abort("update-a")
        self.assertTrue((self.store.generations / first).exists())
        self.assertEqual(tree_contents(self.store.generations / first), before)

    def test_ready_rejects_tampered_override_bytes(self):
        generation = self.commit()
        manifest = json.loads((self.store.generations / generation / "manifest.json").read_text())
        path = self.store.root / "overrides" / (manifest["lock_sha256"] + ".json")
        path.write_bytes(path.read_bytes() + b" ")
        self.assertIsNotNone(self.ready())

    def test_verify_rejects_a_generation_whose_manifest_changed(self):
        generation = self.smoked()
        self.updates.close("update-a")
        self.updates.flip(generation, "update-a")
        path = self.store.generations / generation / "manifest.json"
        manifest = json.loads(path.read_text())
        manifest["release"] = "0" * 64
        path.write_bytes(encoded(manifest))
        self.assertFalse(self.updates.verify("update-a")["entries_ok"])
