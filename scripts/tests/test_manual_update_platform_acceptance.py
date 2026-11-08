"""Manual commands must preserve the deployment on either supported host OS."""

import shlex
import unittest

from update_host_fixture import HostBoundary, OLD


class ManualUpdatePlatformAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)
        self.host.system = "Darwin"

    def test_macos_default_colima_deployment_needs_no_remote_override(self):
        self.host.remote = "colima-pairtask"
        report = self.host.check()
        self.assertEqual(report["update"]["action"], "upgrade_app")
        self.assertEqual(report["guidance"], "pk upgrade")
        self.assertEqual(report["scope"], "default")

    def test_macos_local_deployment_keeps_override_and_never_runs_default_installer(self):
        report = self.host.check()
        self.assertEqual(shlex.split(report["guidance"]), ["pk", "upgrade", "--remote", "local"])
        self.assertEqual(report["scope"], "custom")
        self.host.host_tag, self.host.host_commit = OLD, "a" * 40
        report = self.host.check()
        installer, rollout = report["guidance"].split(" && ", 1)
        self.assertIn("--no-provision", installer)
        self.assertEqual(shlex.split(rollout), ["pk", "upgrade", "--remote", "local"])


if __name__ == "__main__":
    unittest.main()
