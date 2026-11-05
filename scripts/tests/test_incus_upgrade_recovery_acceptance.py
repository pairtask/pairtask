"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import subprocess
import unittest
from incus_upgrade_fixtures import manifest_for, upgrade
from incus_upgrade_runner import installs
from incus_upgrade_case import UpgradeApplyCase


class UpgradeRecoveryTests(UpgradeApplyCase):
    def test_hold_cleanup_failure_does_not_skip_unit_restoration(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.units, self.obs.units)

    def test_one_failed_unit_cleanup_does_not_skip_the_other_unit(self):
        fail = lambda argv: argv == ["systemctl", "enable", "incus.socket"]
        with self.harness(fail=fail) as (runner, _):
            self.apply_with_failure(runner)
        commands = [argv for _, argv, _ in runner.calls]
        self.assertIn(["systemctl", "start", "incus.service"], commands)

    def test_originally_unheld_packages_remain_unheld(self):
        self.obs.holds = ["incus", "zfsutils-linux"]
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            upgrade.apply(runner, self.manifest, yes=True)
        self.assertEqual(runner.holds, set(self.obs.holds))

    def test_successful_start_command_still_requires_confirmed_restored_state(self):
        with self.harness() as (runner, _):
            runner.after_start_probe = "failed"
            self.apply_with_failure(runner)

    def test_cleanup_failure_is_a_structured_error_before_version_query(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))
        self.assertFalse(any(scope == "incus" for scope, _, _ in runner.calls))

    def test_primary_error_and_cleanup_failure_are_both_retained(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            runner.exception = (installs, upgrade.UpgradeError("primary-install-failure"))
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertIn("primary-install-failure", str(caught.exception))
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))

    def test_socket_activated_service_returns_to_its_original_inactive_state(self):
        self.obs.units["incus.service"] = upgrade.Unit("inactive", "disabled")
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            runner.query_effect = lambda: setattr(runner.units["incus.service"], "active", "active")
            upgrade.apply(runner, self.manifest, yes=True)
        self.assertEqual(runner.units, self.obs.units)

    def test_hold_restoration_is_verified_after_a_successful_command(self):
        with self.harness() as (runner, _):
            runner.hold_noop = True
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))

    def test_an_extra_incus_hold_is_removed_to_restore_the_original_set(self):
        self.obs.holds = ["incus", "zfsutils-linux"]
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            def add_hold(argv):
                if installs(argv):
                    runner.holds.add("incus-base")
                return False
            runner.fail = add_hold
            try:
                upgrade.apply(runner, self.manifest, yes=True)
            except upgrade.UpgradeError as error:
                self.fail(f"Available cleanup did not restore the original holds: {error}")
        self.assertEqual(runner.holds, set(self.obs.holds))

    def test_failed_timed_out_or_interrupted_verification_still_settles_units(self):
        errors = (None, subprocess.TimeoutExpired("incus-query", upgrade.QUERY), KeyboardInterrupt())
        for error in errors:
            with self.subTest(error=type(error).__name__):
                self.assert_failed_query_settles(error)

    def assert_failed_query_settles(self, error):
        self.obs.units["incus.service"] = upgrade.Unit("inactive", "disabled")
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            runner.server = "7.4"

            def query():
                runner.units["incus.service"].active = "active"
                if error is not None:
                    raise error

            runner.query_effect = query
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertEqual(runner.units, self.obs.units)
        self.assertTrue(any(step.name == "units as found after the version check" for step in caught.exception.steps))
        if error is None:
            self.assertIn("7.4", str(caught.exception))

    def test_primary_verify_failure_survives_failed_final_unit_cleanup(self):
        self.obs.units["incus.service"] = upgrade.Unit("inactive", "disabled")
        self.manifest = manifest_for(self.obs)
        queried = {"started": False}
        with self.harness() as (runner, _):
            runner.server = "7.4"

            def query():
                queried["started"] = True
                runner.units["incus.service"].active = "active"

            runner.query_effect = query
            runner.fail = lambda argv: queried["started"] and argv == ["systemctl", "stop", "incus.service"]
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertIn("7.4", str(caught.exception))
        self.assertTrue(any(not step.ok and "unit" in step.name for step in caught.exception.steps))

    def test_hold_changes_and_readback_continue_after_each_individual_failure(self):
        for action in ("unhold", "hold"):
            with self.subTest(action=action):
                self.assert_hold_cleanup_continues(action)

    def assert_hold_cleanup_continues(self, action):
        self.obs.holds = ["incus", "zfsutils-linux"]
        self.manifest = manifest_for(self.obs)
        unheld = set(upgrade.PACKAGES) - {"incus"}
        with self.harness() as (runner, _):
            def reject_change(argv):
                if installs(argv):
                    runner.holds.add("incus-base")
                if action == "unhold":
                    return argv[:2] == ["apt-mark", "unhold"] and set(argv[2:]) == unheld
                return argv[:2] == ["apt-mark", "hold"]

            runner.fail = reject_change
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        commands = [argv for _, argv, _ in runner.calls]
        self.assertIn(["apt-mark", "hold", "incus"], commands)
        self.assertIn(["apt-mark", "showhold"], commands)
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))
        if action == "unhold":
            self.assertIn("incus", runner.holds)


if __name__ == "__main__":
    unittest.main()
