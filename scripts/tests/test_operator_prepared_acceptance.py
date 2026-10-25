"""Acceptance checks for the disposable CI operator image cache."""

from pathlib import Path
from dataclasses import replace
import sys
import unittest

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_operator_prepared as prepared


class PreparedRecipeAcceptanceTests(unittest.TestCase):
    def test_same_recipe_has_a_stable_full_digest(self):
        inputs = ("x86_64", "a" * 64, b'{"tofu_version":"1.12.6"}', b"bootstrap")
        key = prepared.recipe_key(*inputs)
        self.assertRegex(key, r"^[0-9a-f]{64}$")
        self.assertEqual(prepared.recipe_key(*inputs), key)

    def test_each_recipe_input_invalidates_the_image(self):
        inputs = ("x86_64", "a" * 64, b"operator lock", b"bootstrap recipe")
        original = prepared.recipe_key(*inputs)
        replacements = ("aarch64", "b" * 64, b"changed lock", b"changed recipe")
        for index, replacement in enumerate(replacements):
            with self.subTest(input=index):
                changed = list(inputs)
                changed[index] = replacement
                self.assertNotEqual(prepared.recipe_key(*changed), original)

    def test_input_boundaries_cannot_alias_another_recipe(self):
        first = prepared.recipe_key("x86_64", "a" * 64, b"a", b"bc")
        second = prepared.recipe_key("x86_64", "a" * 64, b"ab", b"c")
        self.assertNotEqual(first, second)


class PreparedValidationAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.digest = "a" * 64
        self.clean = prepared.Snapshot(
            devices={
                "root": {"type": "disk", "path": "/", "pool": "test-pool"},
                "eth0": {"type": "nic", "network": "test-network", "name": "eth0"},
            },
            operator_files=frozenset({"bin/tofu"}),
            binary_sha256=self.digest,
            machine_id="",
            dbus_machine_id="",
            ssh_host_keys={},
        )

    def check_rejected(self, candidate):
        launched = replace(candidate, machine_id="1" * 32, dbus_machine_id="1" * 32)
        for verify, snapshot in ((prepared.verify_clean, candidate),
                                 (prepared.verify_launch, launched)):
            with self.subTest(verify=verify.__name__), self.assertRaises(prepared.Contaminated):
                verify(snapshot, self.digest)

    def test_clean_stopped_candidate_can_be_published(self):
        prepared.verify_clean(self.clean, self.digest)

    def test_published_candidate_cannot_carry_old_identity(self):
        cases = (
            {"machine_id": "1" * 32},
            {"dbus_machine_id": "1" * 32},
            {"ssh_host_keys": {"ssh_host_ed25519_key": "b" * 64}},
        )
        for changes in cases:
            with self.subTest(changes=changes), self.assertRaises(prepared.Contaminated):
                prepared.verify_clean(replace(self.clean, **changes), self.digest)

    def test_launched_instance_requires_a_fresh_machine_identity(self):
        with self.assertRaises(prepared.Contaminated):
            prepared.verify_launch(self.clean, self.digest)
        launched = replace(self.clean, machine_id="1" * 32, dbus_machine_id="1" * 32)
        prepared.verify_launch(launched, self.digest)

    def test_both_checks_reject_missing_or_wrong_executable(self):
        for digest in ("", "0" * 64):
            with self.subTest(digest=digest):
                self.check_rejected(replace(self.clean, binary_sha256=digest))

    def test_both_checks_reject_state_tokens_plans_and_extra_files(self):
        for path in ("settings.json", "approved.tfplan", "config/backend.tf.json",
                     "terraform.tfstate", "bin/unexpected", "config/symlink"):
            candidate = replace(self.clean, operator_files=frozenset({"bin/tofu", path}))
            with self.subTest(path=path):
                self.check_rejected(candidate)

    def test_both_checks_reject_proxy_devices_regardless_of_name(self):
        for name in ("admin", "state", "root", "eth0"):
            devices = dict(self.clean.devices)
            devices[name] = {"type": "proxy", "listen": "unix:/run/operator-incus.sock"}
            candidate = replace(self.clean, devices=devices)
            with self.subTest(name=name):
                self.check_rejected(candidate)

    def test_both_checks_reject_extra_mounts_and_missing_required_devices(self):
        devices = dict(self.clean.devices)
        devices["host"] = {"type": "disk", "path": "/host", "source": "/"}
        cases = (devices, {}, {"root": self.clean.devices["root"]},
                 {"eth0": self.clean.devices["eth0"]})
        for devices in cases:
            with self.subTest(devices=devices):
                self.check_rejected(replace(self.clean, devices=devices))

    def test_both_checks_require_root_disk_mounted_at_root(self):
        devices = dict(self.clean.devices)
        devices["root"] = dict(devices["root"], path="/somewhere-else")
        self.check_rejected(replace(self.clean, devices=devices))
