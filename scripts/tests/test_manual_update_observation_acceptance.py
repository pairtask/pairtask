"""A slow identity lookup must not publish an app state replaced meanwhile."""

import unittest
from unittest import mock

from update_host_fixture import HostBoundary, NEW


class ManualUpdateObservationAcceptance(unittest.TestCase):
    def test_rollout_during_installed_manifest_lookup_is_observed_before_report(self):
        host = HostBoundary(self)
        original = host.http
        changed = []

        def rollout_during_lookup(request, **kwargs):
            url = getattr(request, "full_url", request)
            if url.endswith("/releases/assets/1"):
                host.deploy(NEW)
                changed.append(True)
            return original(request, **kwargs)

        with mock.patch.object(host, "http", side_effect=rollout_during_lookup):
            report = host.check()
        self.assertTrue(changed, "the scenario must reach the old installed image's manifest")
        self.assertEqual(report["app"]["release"], NEW)
        self.assertEqual(report["update"], {"state": "current", "action": "none"})


if __name__ == "__main__":
    unittest.main()
