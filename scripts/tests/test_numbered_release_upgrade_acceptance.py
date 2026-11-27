"""Exercise the public host version command across a release numbering transition."""

import unittest
from update_host_fixture import HostBoundary, manifest


INSTALLED = "workspace-v0.0.0-build.1160.1"
BRIDGE = "workspace-v0.0.0-build.1164.1"
NUMBERED = "v1165"
HOTFIX = "v1165.1"


class NumberedReleaseUpgradeTests(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)
        self.host.manifests = {
            tag: manifest(digit * 40, digit * 64)
            for tag, digit in zip((INSTALLED, BRIDGE, NUMBERED, HOTFIX), "1234")
        }

    def observe(self, installed, latest, action, refresh=True):
        host_tag, app_tag = installed
        self.host.host_tag = host_tag
        self.host.host_commit = self.host.manifests[host_tag]["collab"]["commit"]
        self.host.deploy(app_tag)
        self.host.latest = latest
        report = self.host.check(refresh=refresh)
        self.assertEqual(report["host"]["release"], host_tag)
        self.assertEqual(report["app"]["release"], app_tag)
        self.assertEqual(report["available"]["release"], latest)
        self.assertEqual(report["update"]["action"], action)

    def test_old_reader_can_reach_the_bridge(self):
        self.observe((INSTALLED, INSTALLED), BRIDGE, "update_host")
        self.observe((BRIDGE, INSTALLED), BRIDGE, "upgrade_app")
        self.observe((BRIDGE, BRIDGE), BRIDGE, "none")

    def test_bridge_to_numbered_to_hotfix_preserves_upgrade_order(self):
        self.observe((BRIDGE, BRIDGE), NUMBERED, "update_host")
        self.observe((NUMBERED, BRIDGE), NUMBERED, "upgrade_app")
        self.observe((NUMBERED, NUMBERED), NUMBERED, "none")
        self.observe((NUMBERED, NUMBERED), HOTFIX, "update_host")
        self.observe((HOTFIX, NUMBERED), HOTFIX, "upgrade_app")
        self.observe((HOTFIX, HOTFIX), HOTFIX, "none")
        self.observe((HOTFIX, HOTFIX), NUMBERED, "none")

    def test_cached_numbered_tag_survives_an_offline_check(self):
        self.observe((NUMBERED, NUMBERED), HOTFIX, "update_host")
        self.host.http_error = 503
        self.observe((NUMBERED, NUMBERED), HOTFIX, "update_host", refresh=False)
