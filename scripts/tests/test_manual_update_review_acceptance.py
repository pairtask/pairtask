"""Independent review regressions; additive to frozen revision 1."""

import subprocess
import unittest
from unittest import mock

from update_host_fixture import HostBoundary, NEW, OLD, TAG, manifest


class ManualUpdateReviewAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)

    def test_missing_workspace_cannot_be_reported_as_known_deployment(self):
        del self.host.instances["workspace"]
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["action"], "none")

    def test_latest_prefetch_cannot_hide_two_releases_for_same_image(self):
        duplicate = TAG.format(100, 2)
        self.host.manifests[duplicate] = manifest("b" * 40, "2" * 64)
        self.host.deploy(NEW)
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["action"], "none")

    def test_candidate_overflow_never_ignores_a_fourth_ambiguous_release(self):
        for attempt, fingerprint in ((2, "3" * 64), (3, "4" * 64), (4, "1" * 64)):
            self.host.manifests[TAG.format(99, attempt)] = manifest("a" * 40, fingerprint)
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["action"], "none")

    def test_failed_git_status_cannot_attest_a_clean_host(self):
        original = self.host.git

        def unavailable_status(argv):
            if "status" in argv:
                return subprocess.CompletedProcess(argv, 128, "", "cannot read worktree")
            return original(argv)

        with mock.patch.object(self.host, "git", side_effect=unavailable_status):
            report = self.host.check()
        self.assertEqual(report["host"]["state"], "unknown")
        self.assertIsNone(report["host"]["release"])
        self.assertEqual(report["update"]["action"], "none")


if __name__ == "__main__":
    unittest.main()
