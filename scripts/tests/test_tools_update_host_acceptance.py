"""Host recovery acceptance over two real stores; no Incus or network.

The adapter only translates activator calls. The observer checks safety at
each effect, including intermediate effects that a final-state test misses.
"""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path[:0] = [str(ROOT / "scripts"), str(ROOT / "infra/image")]
import sandbox_tools_update as host
import tool_store
import test_tool_update_acceptance as fixtures

tree_contents = fixtures.tree_contents


class Container:
    def __init__(self, owner, role):
        self.owner, self.role = owner, role
        self.name, self.uuid = role + "-instance", "immutable-" + role
        self.fixture = fixtures.UpdateAcceptance()
        self.fixture.setUp()
        owner.addCleanup(self.fixture.doCleanups)
        self.store = self.fixture.store
        self.unreachable = False

    def activate(self, command, *args):
        if self.unreachable:
            raise host.ContainerError(host.UNREACHABLE, self.role)
        update = args[args.index("--update") + 1]
        self.owner.before_effect(self.role, command, update)
        try:
            if command == "verify":
                answer = self.store.verify_update(update)
            elif command == "stage":
                if args[0] == "--reuse":
                    generation = self.store.restage_update(args[1], update)
                else:
                    generation = self.store.stage_update(args[0], self.fixture.incoming, update)
                answer = {"generation": generation, "fresh": self.store.staged_fresh(generation, update)}
            elif command == "smoke":
                answer = self.store.smoke_update(args[0], update, check=lambda *_: None)
            elif command in ("flip", "restore"):
                answer = getattr(self.store, command + "_update")(args[0], update)
            elif command in ("close", "release", "abort"):
                answer = getattr(self.store, command + "_update")(update)
            else:
                raise AssertionError(command)
        except tool_store.StoreError as error:
            raise host.ContainerError(getattr(error, "code", "refused"), str(error)) from error
        self.owner.events.append((self.role, command))
        return answer


class Interrupt(host.Hooks):
    def __init__(self, at=None, failure=None, lost=False):
        self.at, self.failure, self.lost = at, failure, lost
        self.trace, self.fired = [], False

    def boundary(self, edge, phase, step, role):
        point = (edge, phase, step, role)
        self.trace.append(point)
        if self.at == point or self.at == len(self.trace):
            raise host.Crash(point)

    def before(self, phase, step, role):
        self.boundary("before", phase, step, role)

    def after(self, phase, step, role):
        self.boundary("after", phase, step, role)

    def inject(self, role, command, lost):
        if not self.fired and self.failure == (role, command) and self.lost == lost:
            self.fired = True
            raise host.ContainerError("refused", "injected failure or lost reply")

    def fail(self, role, command):
        self.inject(role, command, False)

    def replied(self, role, command):
        self.inject(role, command, True)


class HostAcceptance(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.journal = host.Journal(Path(self.temporary.name) / "tools-update.json")
        self.events = []
        self.boxes = {role: Container(self, role) for role in host.ROLES}
        # Prove selectors are per-container, not one shared generation ID.
        self.boxes["secured"].fixture.commit("initial-secured", "1.1.0")
        self.old = self.pointers()
        self.assertNotEqual(*self.old.values())
        self.deployment = {"project": "test", "release": "release-a", "base_lock_sha256": "lock-a"}

    def update(self, hooks=None, resolve=None):
        return host.Update(self.journal, self.boxes, resolve or self.resolve, hooks, self.deployment)

    def resolve(self, requested, record):
        plans = {}
        for role, box in self.boxes.items():
            manifest = box.fixture.candidate(requested.get("claude", "2.0.0"))
            plans[role] = {"candidate": tool_store.generation_id(manifest),
                           "deliver": lambda container, update, m=manifest: [m]}
        return {"requested": {"claude": requested.get("claude", "2.0.0")}, "containers": plans}

    def pointers(self):
        return {role: box.store.current_generation() for role, box in self.boxes.items()}

    def observations(self, update):
        return {role: box.store.verify_update(update) for role, box in self.boxes.items()}

    def before_effect(self, role, command, update):
        if command not in ("flip", "restore", "release"):
            return
        record = self.journal.load()
        seen = self.observations(update)
        if command in ("flip", "restore"):
            for value in seen.values():
                self.assertEqual((value["state"], value["update"]), ("maintenance", update))
            self.assertEqual(record["target"], "predecessor" if command == "restore" else "candidate")
            self.assertEqual(record["phase"], "restoring" if command == "restore" else "flipping")
            self.assertEqual(record["step"], "intent")
        else:
            target = "predecessor" if record["phase"] == "reopening" else record["target"]
            for key, value in seen.items():
                self.assertEqual(value["current"], record["containers"][key][target])
                self.assertTrue(value["entries_ok"], (role, command, key, value))

    def stop_at(self, phase, step="intent", role=None, edge="after"):
        with self.assertRaises(host.Crash):
            self.update(Interrupt((edge, phase, step, role))).run({})
        return self.journal.load()

    def assert_settled(self, record):
        if record is None:
            self.assertEqual(self.pointers(), self.old)
            return
        self.assertIn(record["phase"], ("done", "aborted"), record)
        expected = self.old if record["phase"] == "aborted" else {
            role: record["containers"][role][record["outcome"]] for role in host.ROLES}
        self.assertEqual(self.pointers(), expected)
        for value in self.observations(record["id"]).values():
            self.assertEqual(value["state"], "ready")
            self.assertTrue(value["entries_ok"])
        events = list(self.events)
        self.assertEqual(self.update().recover(), record)
        self.assertEqual(self.events, events, "terminal recovery must be a no-op")

    def test_success_offline_rollback_and_retention(self):
        before = {role: tree_contents(box.store.generations / self.old[role]) for role, box in self.boxes.items()}
        forward = self.update().run({})
        self.assertEqual((forward["phase"], forward["outcome"]), ("done", "candidate"))
        candidates = self.pointers()
        self.assert_settled(forward)
        def offline(*_):
            self.fail("rollback tried to resolve or download")
        backward = self.update(resolve=offline).rollback()
        self.assertEqual(backward["kind"], "rollback")
        self.assertEqual(self.pointers(), self.old)
        self.assert_settled(backward)
        for role, box in self.boxes.items():
            self.assertTrue((box.store.generations / candidates[role]).is_dir())
            self.assertEqual(tree_contents(box.store.generations / self.old[role]), before[role])

    def test_partial_release_bad_candidate_closes_both_before_restore(self):
        record = self.update(Interrupt(failure=("secured", "release"))).run({})
        self.assertEqual((record["phase"], record["failure"]), ("failed", "release"))
        self.assertEqual(self.observations(record["id"])["workspace"]["state"], "ready")
        # Damage only the candidate manifest, preserving the old artifacts.
        path = self.boxes["secured"].store.generations / self.pointers()["secured"] / "manifest.json"
        manifest = json.loads(path.read_text())
        manifest["release"] = "damaged"
        path.write_text(json.dumps(manifest))
        self.events.clear()
        result = self.update().recover()
        self.assertEqual((result["phase"], result["outcome"]), ("done", "predecessor"))
        first_restore = next(i for i, (_, command) in enumerate(self.events) if command == "restore")
        self.assertIn(("workspace", "close"), self.events[:first_restore])
        self.assertIn(("secured", "close"), self.events[:first_restore])
        self.assert_settled(result)

    def test_release_failure_checks_both_before_reopening_either(self):
        record = self.stop_at("releasing")
        path = self.boxes["secured"].store.generations / self.pointers()["secured"] / "manifest.json"
        data = json.loads(path.read_text())
        data["release"] = "damaged"
        path.write_text(json.dumps(data))
        result = self.update().recover()
        self.assertEqual(result["outcome"], "predecessor")
        self.assert_settled(result)

    def test_unreachable_recovery_preserves_journal_and_all_store_evidence(self):
        self.stop_at("flipping", "flip", "workspace")
        journal = self.journal.path.read_bytes()
        before = {role: tree_contents(box.store.root) for role, box in self.boxes.items()}
        self.events.clear()
        self.boxes["secured"].unreachable = True
        with self.assertRaises(host.Blocked):
            self.update().recover()
        self.assertEqual(self.journal.path.read_bytes(), journal)
        self.assertTrue(all(command == "verify" for _, command in self.events))
        for role, box in self.boxes.items():
            self.assertEqual(tree_contents(box.store.root), before[role])
        self.boxes["secured"].unreachable = False
        self.assert_settled(self.update().recover())

    def test_unreachable_preflight_never_resolves_or_stages(self):
        self.boxes["secured"].unreachable = True
        def must_not_resolve(*_):
            self.fail("unreachable preflight reached resolution")
        with self.assertRaises(host.UpdateRefused):
            self.update(resolve=must_not_resolve).run({})
        self.assertFalse(self.journal.path.exists())
        self.assertEqual(self.pointers(), self.old)
        self.assertTrue(all(command == "verify" for _, command in self.events))

    def test_failed_restore_stays_closed_until_verified_recovery(self):
        failures = [Interrupt(failure=("secured", "flip")), Interrupt(failure=("workspace", "restore"))]
        class Both(host.Hooks):
            def fail(self, role, command):
                for failure in failures:
                    failure.fail(role, command)
        record = self.update(Both()).run({})
        self.assertEqual((record["phase"], record["failure"], record["target"]),
                         ("failed", "restore", "predecessor"))
        for seen in self.observations(record["id"]).values():
            self.assertEqual((seen["state"], seen["update"]), ("maintenance", record["id"]))
        self.assertIsNotNone(host.session_refusal(record))
        result = self.update().recover()
        self.assertEqual(result["outcome"], "predecessor")
        self.assert_settled(result)

    def test_failed_abort_requires_explicit_repair_and_preserves_old_generations(self):
        failures = [Interrupt(failure=("secured", "smoke")), Interrupt(failure=("secured", "abort"))]
        class Both(host.Hooks):
            def fail(self, role, command):
                for failure in failures:
                    failure.fail(role, command)
        record = self.update(Both()).run({})
        self.assertEqual((record["phase"], record["failure"]), ("failed", "abort"))
        self.events.clear()
        self.assertEqual(self.update().recover(), record)
        self.assertEqual(self.events, [])
        result = self.update().recover(repair=True)
        self.assert_settled(result)
        for role, box in self.boxes.items():
            self.assertTrue((box.store.generations / self.old[role]).is_dir())
            self.assertFalse((box.store.root / "updates" / record["id"]).exists())

    def test_changed_instance_uuid_refuses_without_container_calls(self):
        self.stop_at("flipping")
        self.events.clear()
        self.boxes["secured"].uuid = "replacement-instance"
        record = self.update().recover()
        self.assertEqual((record["phase"], record["failure"]), ("failed", "deployment-changed"))
        self.assertEqual(self.events, [])
        self.assertEqual(self.pointers(), self.old)
        self.assertIsNotNone(host.session_refusal(record))

    def test_foreign_maintenance_refuses_without_mutations(self):
        record = self.stop_at("staging")
        self.boxes["secured"].store.close_update("someone-else")
        self.events.clear()
        result = self.update().recover()
        self.assertEqual(result["failure"], "foreign-state")
        self.assertTrue(all(command == "verify" for _, command in self.events))
        self.assertEqual(self.pointers(), self.old)
        self.assertEqual(self.observations(record["id"])["secured"]["update"], "someone-else")

    def test_unexpected_pointer_refuses_without_mutations(self):
        self.stop_at("staging")
        self.boxes["secured"].fixture.commit("someone-else", "9.0.0")
        pointers = self.pointers()
        self.events.clear()
        result = self.update().recover()
        self.assertEqual(result["failure"], "unexpected-pointer")
        self.assertTrue(all(command == "verify" for _, command in self.events))
        self.assertEqual(self.pointers(), pointers)

    def test_malformed_committing_record_fails_before_any_container_call(self):
        variants = [("target", None), ("restores", "bad"), ("deployment", None)]
        variants += [("candidate", value) for value in (None, [], "")]
        for key, value in variants:
            with self.subTest(field=key, value=value):
                rig = HostAcceptance()
                rig.setUp()
                try:
                    record = rig.stop_at("flipping")
                    target = record["containers"]["secured"] if key == "candidate" else record
                    target[key] = value
                    rig.journal.write(record)
                    before = rig.journal.path.read_bytes()
                    rig.events.clear()
                    with self.assertRaises(host.JournalError):
                        rig.update().recover()
                    self.assertEqual(rig.events, [])
                    self.assertEqual(rig.journal.path.read_bytes(), before)
                finally:
                    rig.doCleanups()

    def test_bad_predecessor_is_refused_before_resolving_or_staging(self):
        path = self.boxes["secured"].store.generations / self.old["secured"] / "manifest.json"
        data = json.loads(path.read_text())
        data["release"] = "damaged"
        path.write_text(json.dumps(data))
        def must_not_resolve(*_):
            self.fail("preflight accepted a predecessor that cannot verify")
        with self.assertRaises(host.UpdateRefused):
            self.update(resolve=must_not_resolve).run({})
        self.assertFalse(self.journal.path.exists())
        self.assertEqual(self.pointers(), self.old)

    def test_superseded_journal_never_offers_old_root_rollback(self):
        self.update().run({})
        self.update().run({"claude": "3.0.0"})
        host.supersede(self.journal)
        self.assertEqual(self.update().status()["return_to"], {})
        self.events.clear()
        with self.assertRaises(host.UpdateRefused):
            self.update().rollback()
        self.assertEqual(self.events, [])

    def test_lock_stays_exclusive_across_atomic_journal_replacement(self):
        code = """import sys
from sandbox_tools_update import Journal, UpdateRefused
try:
    with Journal(sys.argv[1]):
        raise SystemExit('unexpected lock acquisition')
except UpdateRefused:
    pass
"""
        with self.journal:
            inode = self.journal.lock_path.stat().st_ino
            self.update().run({})
            self.assertEqual(self.journal.lock_path.stat().st_ino, inode)
            result = subprocess.run([sys.executable, "-c", code, str(self.journal.path)],
                                    cwd=ROOT / "scripts", capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
        self.assertEqual(self.journal.path.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.journal.lock_path.stat().st_mode & 0o777, 0o600)


class HostCrashAcceptance(unittest.TestCase):
    def rig(self):
        rig = HostAcceptance()
        rig.setUp()
        return rig

    def test_failures_and_lost_replies(self):
        for command in ("stage", "smoke", "close", "flip", "release"):
            for role in host.ROLES:
                for lost in (False, True):
                    with self.subTest(command=command, role=role, lost=lost):
                        rig = self.rig()
                        try:
                            record = rig.update(Interrupt(failure=(role, command), lost=lost)).run({})
                            if record["phase"] == "failed":
                                self.assertEqual(record["failure"], "release")
                                record = rig.update().recover()
                            rig.assert_settled(record)
                        finally:
                            rig.doCleanups()

    def test_unreachable_each_mutation_preserves_evidence_and_can_resume(self):
        for command in ("stage", "smoke", "close", "flip", "release"):
            for role in host.ROLES:
                with self.subTest(command=command, role=role):
                    rig = self.rig()
                    saved = {}
                    class Disconnect(host.Hooks):
                        def fail(self, current_role, current_command):
                            if (current_role, current_command) == (role, command):
                                saved["journal"] = rig.journal.path.read_bytes()
                                saved["stores"] = {key: tree_contents(box.store.root) for key, box in rig.boxes.items()}
                                raise host.ContainerError(host.UNREACHABLE, "injected disconnect")
                    try:
                        with self.assertRaises(host.Blocked):
                            rig.update(Disconnect()).run({})
                        self.assertEqual(rig.journal.path.read_bytes(), saved["journal"])
                        for key, box in rig.boxes.items():
                            self.assertEqual(tree_contents(box.store.root), saved["stores"][key])
                        rig.assert_settled(rig.update().recover())
                    finally:
                        rig.doCleanups()

    def test_crash_at_every_journal_and_activator_boundary(self):
        for failure in (None, ("secured", "flip"), ("secured", "close"), ("secured", "smoke")):
            probe = self.rig()
            hooks = Interrupt(failure=failure)
            try:
                probe.update(hooks).run({})
                trace = list(hooks.trace)
            finally:
                probe.doCleanups()
            for index, point in enumerate(trace, 1):
                with self.subTest(failure=failure, boundary=point, index=index):
                    rig = self.rig()
                    try:
                        with self.assertRaises(host.Crash):
                            rig.update(Interrupt(at=index, failure=failure)).run({})
                        rig.assert_settled(rig.update().recover())
                    finally:
                        rig.doCleanups()


if __name__ == "__main__":
    unittest.main()
