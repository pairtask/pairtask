"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import copy
import json
import unittest
from unittest.mock import patch
from incus_upgrade_fixtures import pins_for, transaction_for, upgrade
from incus_upgrade_runner import RecordingRunner, installs
from incus_upgrade_case import UpgradeApplyCase


class UpgradeApplyTests(UpgradeApplyCase):
    def test_apply_requires_yes_before_any_command(self):
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=False)
            self.assertEqual(runner.calls, [])

    def test_foreign_manifest_cannot_run_any_command(self):
        self.manifest["target"]["profile"] = "foreign"
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            self.assertEqual(runner.calls, [])

    def test_manifest_cannot_expand_the_trusted_package_scope(self):
        self.manifest["pins"]["arbitrary-package"] = "1.0"
        self.manifest["transaction"].append(("Inst", "arbitrary-package", "1.0"))
        with self.harness() as (runner, trusted):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            trusted.assert_called()
            self.assert_no_stop_or_install(runner)

    def test_candidate_or_transaction_drift_stops_before_daemon_mutation(self):
        for field in ("pins", "transaction"):
            with self.subTest(field=field):
                self.assert_rejects_drift(field)

    def assert_rejects_drift(self, field):
        data = copy.deepcopy(self.manifest)
        if field == "pins":
            data[field]["incus"] = "7.6-1"
        else:
            data[field].append(("Conf", "incus", "7.5-1"))
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, data, yes=True)
            self.assert_no_stop_or_install(runner)

    def test_all_preconditions_are_rechecked_before_stopping(self):
        for change in ("running", "pool", "holds", "units", "policy", "installed"):
            with self.subTest(change=change):
                self.assert_rejects_observation_change(change)

    def test_instance_started_during_apt_simulation_is_rechecked_before_stop(self):
        current = copy.deepcopy(self.obs)
        pins = pins_for(current)
        runner = RecordingRunner(self.obs)

        def simulate(*_):
            current.instances[0]["status"] = "Running"
            return pins, transaction_for(pins)

        with patch.object(upgrade, "observe", side_effect=lambda _: copy.deepcopy(current)), \
                patch.object(upgrade, "trusted_pins", side_effect=simulate):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
        self.assert_no_stop_or_install(runner)

    def test_manifest_extra_commands_and_paths_are_never_executed(self):
        marker = "manifest-command-must-never-run"
        self.manifest.update(command=["touch", marker], backup_path=f"/tmp/{marker}",
                             profile=f"foreign-{marker}")
        with self.harness() as (runner, _):
            try:
                steps = upgrade.apply(runner, self.manifest, yes=True)
            except upgrade.UpgradeError:
                steps = []
        self.assertFalse(any(marker in " ".join(argv) for _, argv, _ in runner.calls))
        self.assertTrue(all(step.ok for step in steps))

    def assert_rejects_observation_change(self, change):
        fresh = copy.deepcopy(self.obs)
        mutations = {
            "running": lambda: fresh.instances[0].update(status="Running"),
            "pool": lambda: fresh.pools[0].update(driver="zfs"),
            "holds": lambda: fresh.holds.append("new-held-package"),
            "units": lambda: setattr(fresh.units["incus.socket"], "enabled", "disabled"),
            "policy": lambda: setattr(fresh, "policy_rc_d_exists", True),
            "installed": lambda: setattr(fresh.packages["incus"], "installed", "7.2-1"),
        }
        mutations[change]()
        with self.harness(fresh) as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            self.assert_no_stop_or_install(runner)

    def test_success_installs_exact_pins_and_restores_original_state(self):
        with self.harness() as (runner, _):
            steps = upgrade.apply(runner, json.loads(json.dumps(self.manifest)), yes=True)
        self.assertTrue(steps)
        self.assertTrue(all(step.ok for step in steps))
        commands = [argv for _, argv, _ in runner.calls]
        self.assert_exact_install(commands)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assert_backup_and_bounds(runner, commands)
        self.assertIsNone(runner.policy)

    def assert_exact_install(self, commands):
        install = [argv for argv in commands if installs(argv)]
        self.assertEqual(len(install), 1)
        expected = {f"{name}={version}" for name, version in self.manifest["pins"].items()}
        self.assertEqual({arg for arg in install[0] if "=" in arg}, expected)

    def assert_backup_and_bounds(self, runner, commands):
        self.assertTrue(all(0 < timeout <= upgrade.TAR for _, _, timeout in runner.calls))
        self.assertTrue(any(argv[:2] == ["tar", "-tzf"] for argv in commands))
        self.assertTrue(any(upgrade.BACKUP_DIR in str(arg) for argv in commands for arg in argv))

    def test_tampered_expected_server_cannot_make_old_server_a_success(self):
        self.manifest["expected_server"] = "7.1"
        with self.harness() as (runner, _):
            runner.server = "7.1"
            self.apply_with_failure(runner)


if __name__ == "__main__":
    unittest.main()
