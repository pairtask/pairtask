"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import contextlib
import subprocess
import unittest
from incus_upgrade_fixtures import manifest_for, observation, upgrade
from incus_upgrade_runner import installs, recorded_apply, stops


class UpgradeApplyCase(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.manifest = manifest_for(self.obs)

    @contextlib.contextmanager
    def harness(self, fresh=None, fail=None, interrupt=None):
        with recorded_apply(self.obs, fresh, fail, interrupt) as context:
            yield context

    def assert_no_stop_or_install(self, runner):
        self.assertFalse(any(stops(argv) or installs(argv) for _, argv, _ in runner.calls))

    def apply_with_failure(self, runner):
        try:
            steps = upgrade.apply(runner, self.manifest, yes=True)
        except (upgrade.UpgradeError, KeyboardInterrupt, subprocess.TimeoutExpired):
            return
        self.assertTrue(any(not step.ok for step in steps))
