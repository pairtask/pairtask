"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import unittest
from unittest.mock import patch
from incus_upgrade_fixtures import upgrade
from incus_upgrade_runner import installs, stops
from incus_upgrade_case import UpgradeApplyCase


class UpgradeBackupTests(UpgradeApplyCase):
    def test_failed_stop_never_backs_up_or_installs(self):
        with self.harness(fail=stops) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))

    def test_backup_verification_failure_never_installs(self):
        with self.harness(fail=lambda argv: argv[:2] == ["tar", "-tzf"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)

    def test_stop_requires_confirmed_inactive_not_unknown_or_transitioning(self):
        for state in ("unknown", "activating", "deactivating", "failed"):
            with self.subTest(state=state), self.harness() as (runner, _):
                runner.probe_state = state
                self.apply_with_failure(runner)
                self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))

    def test_archive_verification_requires_subuid_and_subgid_when_present(self):
        with self.harness() as (runner, _):
            runner.archive = "var/lib/incus/\nvar/lib/incus/database/\n"
            self.apply_with_failure(runner)
            self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))

    def test_backup_interruption_restores_units_without_installing(self):
        with self.harness(interrupt=lambda argv: argv[:1] == ["tar"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertIsNone(runner.policy)
        self.assertEqual(runner.units, self.obs.units)

    def test_backup_destinations_are_unique_even_at_the_same_clock_second(self):
        paths = []
        with patch.object(upgrade, "datetime", create=True) as clock:
            clock.now.return_value.strftime.return_value = "20261105T090000Z"
            for _ in range(2):
                with self.harness() as (runner, _):
                    upgrade.apply(runner, self.manifest, yes=True)
                paths.extend(argv[argv.index("-czpf") + 1] for _, argv, _ in runner.calls if "-czpf" in argv)
        self.assertEqual(len(paths), 2)
        self.assertNotEqual(paths[0], paths[1])
        self.assertTrue(all(path.startswith(upgrade.BACKUP_DIR + "/") for path in paths))

    def test_untrusted_backup_reservation_path_is_rejected_before_tar(self):
        with self.harness() as (runner, _):
            runner.backup_override = "/tmp/foreign-directory"
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))


if __name__ == "__main__":
    unittest.main()
