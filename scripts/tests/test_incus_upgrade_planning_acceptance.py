"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import unittest
from dataclasses import asdict
from unittest.mock import Mock
from unittest.mock import patch
from incus_upgrade_fixtures import observation, pins_for, transaction_for, upgrade


class UpgradePlanningTests(unittest.TestCase):
    def test_trusted_scope_starts_from_only_the_fixed_package_candidates(self):
        obs = observation()
        base = pins_for(obs)
        pins = {**base, "libdependency": "1:2.0-3"}
        tx = transaction_for(pins)
        with patch.object(upgrade, "transaction", return_value=tx) as simulate:
            actual_pins, actual_tx = upgrade.trusted_pins(Mock(), obs)
        self.assertEqual(simulate.call_args_list[0].args[1], base)
        self.assertEqual(simulate.call_args_list[-1].args[1], pins)
        self.assertEqual(actual_pins, pins)
        self.assertEqual(actual_tx, tx)

    def test_already_current_fixed_packages_remain_pinned(self):
        obs = observation()
        pins = pins_for(obs)
        tx = [("Inst", "incus", pins["incus"])]
        with patch.object(upgrade, "transaction", return_value=tx):
            actual_pins, _ = upgrade.trusted_pins(Mock(), obs)
        self.assertEqual(actual_pins, pins)

    def test_plan_records_per_package_and_daemon_wide_snapshot(self):
        obs = observation()
        pins = pins_for(obs)
        runner = Mock()
        with patch.object(upgrade, "observe", return_value=obs), \
                patch.object(upgrade, "trusted_pins", return_value=(pins, transaction_for(pins))):
            manifest = upgrade.plan(runner)
        self.assertEqual(manifest["packages"], {name: asdict(value) for name, value in obs.packages.items()})
        self.assertEqual(manifest["instances"], obs.instances)
        self.assertEqual(manifest["holds"], obs.holds)
        self.assertEqual(manifest["units"], {name: asdict(value) for name, value in obs.units.items()})
        self.assertEqual(manifest["target"], upgrade.TARGET)
        runner.assert_not_called()
        self.assertEqual(runner.mock_calls, [])

    def test_runner_targets_only_the_dedicated_profile_and_preserves_deadline(self):
        runner = upgrade.Runner()
        with patch.object(runner, "run") as run:
            runner.vm(["apt-cache", "policy", "incus"], upgrade.QUERY)
        run.assert_called_once_with(["colima", "ssh", "-p", "pairtask", "--", "sudo",
                                     "apt-cache", "policy", "incus"], upgrade.QUERY)


if __name__ == "__main__":
    unittest.main()
