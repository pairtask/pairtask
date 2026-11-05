"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import subprocess
import unittest
from incus_upgrade_fixtures import upgrade
from incus_upgrade_runner import creates_policy, installs, written_policy
from incus_upgrade_case import UpgradeApplyCase


class UpgradePolicyTests(UpgradeApplyCase):
    def test_install_failure_still_restores_holds_and_units(self):
        with self.harness(fail=installs) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assertIsNone(runner.policy)

    def test_interruption_during_install_still_restores_holds_and_units(self):
        with self.harness(interrupt=installs) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assertIsNone(runner.policy)

    def test_policy_changed_by_another_actor_is_retained_and_reported(self):
        with self.harness() as (runner, _):
            def change_policy(argv):
                if installs(argv):
                    runner.policy = "administrator policy\n"
                return False
            runner.fail = change_policy
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, "administrator policy\n")
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_failed_policy_removal_still_restores_holds_and_units(self):
        with self.harness(fail=lambda argv: argv == ["rm", "-f", upgrade.POLICY]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, runner.created_policy)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_install_timeout_still_cleans_up_policy_holds_and_units(self):
        with self.harness() as (runner, _):
            runner.exception = (installs, subprocess.TimeoutExpired("apt-get", upgrade.APT))
            self.apply_with_failure(runner)
        self.assertIsNone(runner.policy)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_partial_policy_creation_failure_cleans_up_our_file(self):
        with self.harness() as (runner, _):
            def fail_after_creation(argv):
                if creates_policy(argv):
                    runner.policy = runner.created_policy = written_policy(argv)
                    return True
                return False
            runner.fail = fail_after_creation
            self.apply_with_failure(runner)
        self.assertIsNone(runner.policy)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)

    def test_admin_standard_policy_created_after_observation_is_never_deleted(self):
        admin_body = "#!/bin/sh\nexit 101\n"
        with self.harness() as (runner, _):
            def admin_wins_race(argv):
                if creates_policy(argv):
                    runner.policy = admin_body
                return False
            runner.fail = admin_wins_race
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, admin_body)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)


if __name__ == "__main__":
    unittest.main()
