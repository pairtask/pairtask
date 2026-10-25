"""An unreadable or malformed inspection must not prove an image clean."""

import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_operator_prepared as prepared


class PreparedInspectionTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def instance(self, run):
        return prepared.Instance(run, "local", "disposable-test", "operator")

    def test_failed_listing_is_never_absence(self):
        for path in ("/etc/machine-id", "/var/lib/dbus/machine-id"):
            run = Mock(return_value=subprocess.CompletedProcess([], 1, "", "permission denied"))
            with self.subTest(path=path), self.assertRaises(prepared.Contaminated):
                self.instance(run).text(path, self.directory)
        with self.assertRaises(prepared.Contaminated):
            self.instance(run).host_keys(self.directory)

    def test_malformed_directory_entries_are_not_absence(self):
        for listing in ({}, [None], [{"name": "ssh"}], ["../ssh"], ["ssh", None]):
            run = Mock(return_value=subprocess.CompletedProcess([], 0, json.dumps(listing), ""))
            with self.subTest(listing=listing), self.assertRaises(prepared.Contaminated):
                self.instance(run).exists("/etc/ssh")

    def test_successful_empty_listing_confirms_optional_absence(self):
        run = Mock(return_value=subprocess.CompletedProcess([], 0, "[]", ""))
        instance = self.instance(run)
        self.assertEqual(instance.text("/etc/machine-id", self.directory), "")
        self.assertEqual(instance.host_keys(self.directory), {})
        self.assertFalse(any("pull" in call.args[0] for call in run.call_args_list))

    def test_present_identity_or_key_that_cannot_be_read_fails(self):
        def run(command, **kwargs):
            if "query" in command:
                return subprocess.CompletedProcess(command, 0,
                    json.dumps(["ssh", "machine-id", "ssh_host_ed25519_key"]), "")
            return subprocess.CompletedProcess(command, 1, "", "transport failed")

        with self.assertRaises(prepared.Contaminated):
            self.instance(run).text("/etc/machine-id", self.directory)
        with self.assertRaises(prepared.Contaminated):
            self.instance(run).host_keys(self.directory)

    def test_required_directory_links_are_rejected_without_traversal(self):
        for name in ("operator", "operator/bin", "operator/config"):
            with self.subTest(path=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                operator = root / "operator"
                (operator / "bin").mkdir(parents=True)
                (operator / "config").mkdir()
                target = root / name
                if name == "operator":
                    (operator / "bin").rmdir()
                    (operator / "config").rmdir()
                target.rmdir()
                target.symlink_to(self.directory, target_is_directory=True)
                with self.assertRaises(prepared.Contaminated):
                    prepared.tree_entries(operator)

    def test_executable_link_is_never_hashed_as_a_regular_binary(self):
        target = self.directory / "outside"
        target.write_bytes(b"not an operator executable")
        link = self.directory / "tofu"
        link.symlink_to(target)
        with patch.object(prepared, "sha256_file", side_effect=AssertionError("followed a link")):
            self.assertEqual(prepared.regular_sha256(link), "")
