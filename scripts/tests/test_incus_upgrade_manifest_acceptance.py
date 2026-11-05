"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import copy
import json
import stat
import tempfile
import unittest
from incus_upgrade_fixtures import manifest_for, observation, upgrade


class UpgradeManifestTests(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.manifest = manifest_for(self.obs)

    def test_json_round_trip_keeps_a_valid_manifest(self):
        loaded = json.loads(json.dumps(self.manifest))
        self.assertIsInstance(upgrade.validate_manifest(loaded), dict)

    def test_malformed_schema_or_foreign_target_is_rejected(self):
        invalid = (None, [], {}, {**self.manifest, "schema": True},
                   {**self.manifest, "schema": 999},
                   {**self.manifest, "target": {"profile": "another-vm", "remote": "colima-pairtask"}},
                   {**self.manifest, "target": {"profile": "pairtask", "remote": "local"}})
        for data in invalid:
            with self.subTest(data=data):
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)

    def test_package_names_versions_and_missing_fixed_packages_are_rejected(self):
        invalid = ({"../incus": "7.5-1"}, {"incus;touch": "7.5-1"},
                   {"incus": "7.5\ncommand"}, {"incus": "$(command)"})
        for pins in invalid:
            with self.subTest(pins=pins):
                data = copy.deepcopy(self.manifest)
                data["pins"].update(pins)
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)
        self.manifest["pins"].pop("incus-base")
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.validate_manifest(self.manifest)

    def test_removal_unpinned_transaction_or_blocked_manifest_is_rejected(self):
        for tx in ([("Remv", "incus", "7.1-1")], [("Inst", "unapproved", "1.0")]):
            with self.subTest(tx=tx):
                data = {**self.manifest, "transaction": tx}
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)
        self.manifest["blockers"] = ["running instance"]
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.validate_manifest(self.manifest)

    def test_private_manifest_never_overwrites_existing_file_or_symlink(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "plan.json"
            upgrade.write_manifest(path, self.manifest)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode) & 0o077, 0)
            self.assertEqual(json.loads(path.read_text())["target"], upgrade.TARGET)
            original = path.read_bytes()
            with self.assertRaises((upgrade.UpgradeError, OSError)):
                upgrade.write_manifest(path, {})
            self.assertEqual(path.read_bytes(), original)
            link = Path(temp) / "link.json"
            link.symlink_to(path)
            with self.assertRaises((upgrade.UpgradeError, OSError)):
                upgrade.write_manifest(link, {})
            self.assertEqual(path.read_bytes(), original)


if __name__ == "__main__":
    unittest.main()
