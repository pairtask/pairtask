"""Manual guidance must target the deployment that was actually observed."""

import shlex
import unittest

from update_host_fixture import HostBoundary, NEW, OLD


class ManualUpdateScopeAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)
        self.host.project, self.host.remote, self.host.repo = "team-two", "other-host", "example/another"

    def check(self):
        return self.host.command(["version", "--json", "--refresh", "--project", self.host.project,
                                  "--remote", self.host.remote, "--repo", self.host.repo])

    def assert_rollout_scope(self, command):
        args = shlex.split(command)
        self.assertEqual(args[:2], ["pk", "upgrade"])
        for flag, value in (("--project", self.host.project), ("--remote", self.host.remote), ("--repo", self.host.repo)):
            self.assertIn(flag, args)
            self.assertEqual(args[args.index(flag) + 1], value)

    def test_custom_upgrade_guidance_preserves_repository_project_remote(self):
        report = self.check()
        self.assertEqual(report["update"]["action"], "upgrade_app")
        self.assert_rollout_scope(report["guidance"])

    def test_custom_old_host_refresh_cannot_run_default_installer_rollout(self):
        self.host.host_tag, self.host.host_commit = OLD, "a" * 40
        report = self.check()
        self.assertEqual(report["update"]["action"], "update_host")
        installer, rollout = report["guidance"].split(" && ", 1)
        self.assertIn("--no-provision", installer)
        self.assert_rollout_scope(rollout)

    def test_custom_current_app_only_refreshes_host_without_provisioning(self):
        self.host.deploy(NEW)
        self.host.host_tag, self.host.host_commit = OLD, "a" * 40
        report = self.check()
        self.assertEqual(report["app"]["release"], NEW)
        self.assertIn("--no-provision", report["guidance"])
        self.assertNotIn("pk upgrade", report["guidance"])

    def test_custom_notice_cannot_be_displayed_as_default_scope(self):
        self.check()
        notices = [argv for argv in self.host.processes if "update-notice" in argv]
        self.assertTrue(notices, "the host must publish its scoped update view")
        for argv in notices:
            self.assertIn("--scope", argv)
            self.assertEqual(argv[argv.index("--scope") + 1], "custom")


if __name__ == "__main__":
    unittest.main()
