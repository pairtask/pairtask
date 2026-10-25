"""Independent lifecycle checks at the Incus and image-store boundaries."""

from contextlib import ExitStack
from dataclasses import replace
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_operator_prepared as prepared
import sandbox_operator_runtime as runtime

DIGEST = "a" * 64
FINGERPRINT = "b" * 64


def snapshot(identity=""):
    return prepared.Snapshot(
        devices={"root": {"type": "disk", "path": "/", "pool": "pool"},
                 "eth0": {"type": "nic", "network": "network"}},
        operator_files=frozenset({"bin/tofu"}), binary_sha256=DIGEST,
        machine_id=identity, dbus_machine_id=identity, ssh_host_keys={},
    )


class PreparedLifecycleTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        self.calls = []
        self.store = Mock(spec=prepared.PreparedStore)
        self.store.project = "pairtask-operator-prepared-test"
        self.store.find.return_value = FINGERPRINT
        self.store.publish.return_value = FINGERPRINT
        self.stack.enter_context(patch.object(runtime, "ensure_base", return_value=Mock(ref="local:base")))
        self.stack.enter_context(patch.object(prepared, "expected_binary_sha256", return_value=DIGEST))
        self.inspection = self.stack.enter_context(patch.object(prepared, "inspect", side_effect=self.inspect))
        self.install = self.stack.enter_context(patch.object(runtime.Operator, "install"))

    def run_incus(self, command, **kwargs):
        self.calls.append(tuple(command))
        return subprocess.CompletedProcess(command, 0, "[]", "")

    def inspect(self, incus, remote, project, name):
        self.calls.append(("inspect", project, name))
        return snapshot() if project == self.store.project else snapshot("1" * 32)

    def operator(self):
        return runtime.Operator("local", {"architectures": ["x86_64"]}, "pool", "network",
                                incus=self.run_incus, prepared_store=self.store)

    def init_calls(self):
        return [command for command in self.calls if "init" in command]

    def test_first_operation_prepares_once_and_second_reuses_in_fresh_projects(self):
        self.store.find.side_effect = [None, FINGERPRINT]
        first, second = self.operator(), self.operator()
        first.create()
        second.create()
        self.assertNotEqual(first.project, second.project)
        self.store.ensure_project.assert_called_once()
        self.store.publish.assert_called_once()
        self.assertEqual(self.store.copy_into.call_args_list[0].args, (FINGERPRINT, first.project))
        self.assertEqual(self.store.copy_into.call_args_list[1].args, (FINGERPRINT, second.project))
        self.assertEqual(len(self.init_calls()), 3)  # scratch plus two fresh operators
        self.install.assert_called_once()

    def test_cached_launch_retains_isolation_limits_and_explicit_network_and_pool(self):
        self.operator().create()
        command, = self.init_calls()
        for option in ("--no-profiles", "security.privileged=false", "security.idmap.isolated=true",
                       "security.nesting=false", "security.guestapi=false", "boot.autostart=false",
                       "limits.cpu=2", "limits.memory=2GiB", "limits.processes=256", "root,size=8GiB"):
            self.assertIn(option, command)
        self.assertEqual(command[command.index("--network") + 1], "network")
        self.assertEqual(command[command.index("--storage") + 1], "pool")

    def test_candidate_is_inspected_after_stop_before_publication(self):
        self.store.find.return_value = None
        self.store.publish.side_effect = lambda *args: self.calls.append(("publish",)) or FINGERPRINT
        self.operator().create()
        stopped = next(i for i, command in enumerate(self.calls) if "stop" in command)
        inspected = next(i for i, command in enumerate(self.calls)
                         if command[:2] == ("inspect", self.store.project))
        published = self.calls.index(("publish",))
        self.assertLess(stopped, inspected)
        self.assertLess(inspected, published)

    def test_publish_failure_discards_candidate_and_bootstraps_fresh_operation(self):
        self.store.find.return_value = None
        self.store.publish.side_effect = subprocess.CalledProcessError(1, ["incus", "publish"])
        operator = self.operator()
        operator.create()
        self.store.discard.assert_called_once()
        self.store.copy_into.assert_not_called()
        self.assertFalse(operator.verified)
        self.assertIn("local:base", self.init_calls()[-1])
        self.assertTrue(any("delete" in command and any("scratch-" in arg for arg in command)
                            for command in self.calls))

    def test_bad_cached_executable_is_deleted_and_never_falls_back(self):
        self.inspection.return_value = replace(snapshot("1" * 32), binary_sha256="0" * 64)
        self.inspection.side_effect = None
        operator = self.operator()
        with self.assertRaises(prepared.Contaminated):
            operator.create()
        self.assertFalse(operator.verified)
        self.assertEqual(len(self.init_calls()), 1)
        self.assertTrue(any("delete" in command and operator.target in command for command in self.calls))
        self.assertFalse(any("apt-get" in command for command in self.calls))
        self.store.discard.assert_called_once()

    def test_disabled_cache_uses_existing_full_bootstrap(self):
        operator = runtime.Operator("local", {"architectures": ["x86_64"]}, "pool", "network",
                                    incus=self.run_incus)
        operator.create()
        self.store.find.assert_not_called()
        self.inspection.assert_not_called()
        self.assertIn("local:base", self.init_calls()[0])
        self.assertTrue(any("apt-get" in command and "update" in command for command in self.calls))


class PreparedStoreBoundaryTests(unittest.TestCase):
    def test_invalid_or_absent_tokens_do_not_touch_incus(self):
        for token in ("", "UPPER", "../foreign", "a" * 41, "valid\n", "valid token"):
            run = Mock()
            with self.subTest(token=token):
                store = prepared.store_from_env("local", incus=run, environ={prepared.ENV: token})
                self.assertIsNone(store)
                run.assert_not_called()

    def test_cleanup_only_removes_the_run_owned_project(self):
        calls = []
        project = "pairtask-operator-prepared-run-test"

        def run(command, **kwargs):
            calls.append(command)
            if command[1:3] == ["project", "list"]:
                records = [{"name": project}, {"name": "unrelated"}]
            elif command[3:5] == ["image", "list"]:
                records = [{"fingerprint": FINGERPRINT}]
            elif command[3] == "list":
                records = [{"name": "scratch"}]
            else:
                records = []
            return subprocess.CompletedProcess(command, 0, json.dumps(records), "")

        prepared.PreparedStore("local", "run-test", incus=run).cleanup()
        self.assertIn(["incus", "project", "delete", "local:" + project], calls)
        for command in calls:
            if command[1] == "--project":
                self.assertEqual(command[2], project)
            self.assertNotIn("local:unrelated", command)

    def test_missing_prepared_project_cleanup_is_read_only(self):
        run = Mock(return_value=subprocess.CompletedProcess([], 0, "[]", ""))
        prepared.PreparedStore("local", "run-test", incus=run).cleanup()
        run.assert_called_once()
        self.assertIn("list", run.call_args.args[0])
