"""Tester-owned public CLI acceptance for manual app update awareness."""

import unittest

from update_host_fixture import HostBoundary, NEW, OLD, TAG, manifest


class ManualAppUpdatesAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)

    def test_observed_older_app_has_numeric_update_and_manual_action(self):
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "known")
        self.assertEqual(report["app"]["release"], OLD)
        self.assertEqual(report["host"]["release"], NEW)
        self.assertEqual(report["available"]["release"], NEW)
        self.assertEqual(report["update"], {"state": "update_available", "action": "upgrade_app"})
        self.assertEqual(report["guidance"], "pk upgrade")

    def test_actual_rollout_changes_installed_even_with_fresh_latest_cache(self):
        self.host.check()
        self.host.deploy(NEW)
        requests = len(self.host.requests)
        report = self.host.check(refresh=False)
        self.assertEqual(report["app"]["release"], NEW)
        self.assertEqual(report["update"], {"state": "current", "action": "none"})
        self.assertEqual(len(self.host.requests), requests, "fresh metadata should be reused")

    def test_current_app_and_old_host_are_distinguished(self):
        self.host.deploy(NEW)
        self.host.host_tag, self.host.host_commit = OLD, "a" * 40
        report = self.host.check()
        self.assertEqual(report["app"]["release"], NEW)
        self.assertEqual(report["host"]["release"], OLD)
        self.assertEqual(report["update"]["action"], "update_host")
        self.assertIn("--no-provision", report["guidance"])

    def test_newer_app_with_old_host_never_offers_a_downgrade(self):
        newer = TAG.format(101, 1)
        self.host.manifests[newer] = manifest("d" * 40, "3" * 64)
        self.host.deploy(newer)
        self.host.host_tag = OLD
        report = self.host.check()
        self.assertEqual(report["app"]["release"], newer)
        self.assertEqual(report["update"]["action"], "none")
        self.assertEqual(report["update"]["state"], "current")

    def test_same_commit_requires_exact_image_and_full_provenance(self):
        self.host.manifests[NEW]["collab"] = self.host.manifests[OLD]["collab"].copy()
        self.host.manifests[NEW]["recipe_commit"] = "a" * 40
        report = self.host.check()
        self.assertEqual(report["app"]["release"], OLD)
        self.assertEqual(report["app"]["state"], "known")

    def test_unpublished_image_with_matching_commit_is_unknown(self):
        for data in self.host.instances.values():
            data["config"]["volatile.base_image"] = "f" * 64
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["action"], "none")

    def test_recipe_and_file_digest_mismatch_never_claim_known(self):
        for field, value in (("recipe_commit", "e" * 40), ("files_sha256", {"different": "e" * 64})):
            with self.subTest(field=field):
                self.host.deploy(OLD)
                for data in self.host.instances.values():
                    data["source"][field] = value
                report = self.host.check()
                self.assertEqual(report["app"]["state"], "unknown")
                self.assertIsNone(report["app"]["release"])

    def test_mixed_deployment_cannot_be_cleared_by_cached_desired_version(self):
        self.host.check()
        self.host.instances["workspace"] = self.host.instance(NEW)
        report = self.host.check(refresh=False)
        self.assertEqual(report["app"]["state"], "mixed")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["state"], "mixed")
        self.assertEqual(report["update"]["action"], "upgrade_app")

    def test_stopped_instance_is_never_started_for_observation(self):
        self.host.instances["workspace"]["status"] = "Stopped"
        report = self.host.check()
        instance = report["app"]["instances"]["workspace"]
        self.assertEqual(instance["status"], "Stopped")
        self.assertIsNone(instance["commit"])
        self.assertNotEqual(report["app"]["state"], "mixed")

    def test_custom_project_remote_and_cache_never_replace_live_state(self):
        self.host.check()
        self.host.project, self.host.remote = "team-two", "other-host"
        self.host.deploy(NEW)
        report = self.host.check(refresh=False)
        self.assertEqual(report["app"]["release"], NEW)
        self.assertEqual(report["update"]["action"], "none")

    def test_server_failure_is_unknown_and_recovers_without_upgrade(self):
        self.host.http_error = 504
        report = self.host.check()
        self.assertEqual(report["available"]["state"], "check_failed")
        self.assertIsNone(report["available"]["release"])
        self.assertEqual(report["update"]["action"], "none")
        self.host.http_error = None
        self.assertEqual(self.host.check()["update"]["action"], "upgrade_app")

    def test_dirty_host_is_unknown_without_hiding_known_app(self):
        self.host.dirty = True
        report = self.host.check()
        self.assertEqual(report["host"]["state"], "unknown")
        self.assertTrue(report["host"]["dirty"])
        self.assertEqual(report["app"]["release"], OLD)

    def test_old_broker_does_not_break_version_command(self):
        self.host.notice_supported = False
        report = self.host.check()
        self.assertEqual(report["app"]["release"], OLD)
        self.assertEqual(report["update"]["action"], "upgrade_app")

    def test_old_host_and_old_app_receive_explicit_installer_guidance(self):
        self.host.host_tag, self.host.host_commit = OLD, "a" * 40
        report = self.host.check()
        self.assertEqual(report["update"]["action"], "update_host")
        self.assertIn("https://raw.githubusercontent.com/pairtask/pairtask/main/install.sh", report["guidance"])
        self.assertNotIn("--no-provision", report["guidance"])

    def test_failed_refresh_keeps_old_metadata_explicitly_stale(self):
        self.host.check()
        self.host.http_error = 504
        report = self.host.check()
        self.assertEqual(report["available"]["state"], "stale")
        self.assertEqual(report["available"]["release"], NEW)
        self.assertTrue(report["available"]["error"])
        self.assertIsNotNone(report["available"]["checked_at"])

    def test_dirty_or_mismatched_published_provenance_is_unknown(self):
        for data in self.host.instances.values():
            data["source"]["collab"]["working_tree"] = True
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertEqual(report["update"]["action"], "none")

    def test_draft_or_executable_latest_metadata_never_recommends_action(self):
        for fields in ({"draft": True}, {"tag_name": "$(touch /tmp/update-must-never-run)"}):
            with self.subTest(fields=fields):
                self.host.latest_fields = fields
                report = self.host.check()
                self.assertEqual(report["available"]["state"], "check_failed")
                self.assertEqual(report["update"]["action"], "none")

    def test_two_releases_matching_same_image_are_ambiguous(self):
        duplicate = TAG.format(99, 2)
        self.host.manifests[duplicate] = manifest("a" * 40, "1" * 64)
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "unknown")
        self.assertIsNone(report["app"]["release"])
        self.assertEqual(report["update"]["action"], "none")

    def test_realistic_full_build_source_is_not_truncated_to_four_kilobytes(self):
        self.host.manifests[OLD]["files_sha256"] = {f"source/file-{i}": "c" * 64 for i in range(1500)}
        self.host.deploy(OLD)
        report = self.host.check()
        self.assertEqual(report["app"]["state"], "known")
        self.assertEqual(report["app"]["release"], OLD)


if __name__ == "__main__":
    unittest.main()
