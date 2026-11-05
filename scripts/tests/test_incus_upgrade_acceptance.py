"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import unittest
from incus_upgrade_fixtures import observation, pins_for, transaction_for, upgrade


class UpgradeBlockerTests(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.tx = transaction_for(pins_for(self.obs))

    def assert_blocked(self):
        self.assertTrue(upgrade.blockers(self.obs, self.tx))

    def test_stopped_other_project_instances_are_allowed(self):
        self.assertEqual(upgrade.blockers(self.obs, self.tx), [])

    def test_running_instance_in_another_project_blocks(self):
        self.obs.instances[0]["status"] = "Running"
        self.assert_blocked()

    def test_external_and_non_dir_pools_block(self):
        cases = (("zfs", "/var/lib/incus/disks/default.img"), ("lvm", "vg"),
                 ("btrfs", "/var/lib/incus/storage-pools/default"), ("dir", "/outside"),
                 ("dir", "/var/lib/incus-other/pool"), ("dir", "/var/lib/incus/../outside"))
        for driver, source in cases:
            with self.subTest(driver=driver, source=source):
                self.obs.pools = [{"name": "pool", "driver": driver, "source": source}]
                self.assert_blocked()

    def test_missing_versions_wrong_origin_or_downgrade_block(self):
        for field, value in (("installed", None), ("candidate", None), ("origin", "untrusted.example")):
            with self.subTest(field=field):
                self.obs = observation()
                setattr(self.obs.packages["incus"], field, value)
                self.assert_blocked()
        self.obs = observation()
        self.obs.upgrades["incus"] = False
        self.assert_blocked()

    def test_unverified_package_comparison_blocks(self):
        self.obs.upgrades["incus"] = None
        self.assert_blocked()

    def test_existing_admin_policy_low_space_and_removal_block(self):
        self.obs.policy_rc_d_exists = True
        self.assert_blocked()
        self.obs = observation()
        self.obs.backup_free_bytes = 0
        self.assert_blocked()
        self.obs = observation()
        self.tx.append(("Remv", "libdependency", "1.0"))
        self.assert_blocked()

    def test_unsupported_expected_server_blocks(self):
        self.obs.packages["incus"].candidate = "7.4-1"
        self.assert_blocked()

    def test_physical_pool_escape_or_unknown_and_symlinked_incus_root_block(self):
        for resolved in (None, "/external/pool", "/var/lib/incus-other/pool"):
            with self.subTest(resolved=resolved):
                self.obs.pools[0]["resolved"] = resolved
                self.assert_blocked()
        self.obs = observation()
        self.obs.incus_root = "/external/incus"
        self.assert_blocked()

    def test_only_explicitly_stopped_instances_pass_the_cold_backup_gate(self):
        for status in ("Frozen", "Starting", "Stopping", "Error", "", None, "missing"):
            with self.subTest(status=status):
                self.obs = observation()
                self.obs.instances[0]["status"] = status
                if status == "missing":
                    self.obs.instances[0].pop("status")
                self.assert_blocked()

    def test_unknown_or_unsupported_unit_states_block(self):
        for active, enabled in (("unknown", "enabled"), ("active", "unknown"),
                                ("activating", "enabled"), ("inactive", "masked")):
            with self.subTest(active=active, enabled=enabled):
                self.obs.units["incus.service"] = upgrade.Unit(active, enabled)
                self.assert_blocked()


if __name__ == "__main__":
    unittest.main()
