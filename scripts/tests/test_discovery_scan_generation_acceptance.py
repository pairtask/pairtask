"""A live scan report acknowledges the exact Find lease the helper applied."""

import contextlib
import io
import unittest
from unittest import mock

from test_discovery_report_acceptance import helper


class DiscoveryScanGenerationAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = helper()
        self.now = 100.0
        self.wall = 1700000000.0
        monotonic = mock.patch.object(self.host.time, "monotonic", lambda: self.now)
        wall = mock.patch.object(self.host.time, "time", lambda: self.wall)
        monotonic.start()
        wall.start()
        self.addCleanup(monotonic.stop)
        self.addCleanup(wall.stop)
        self.status = mock.Mock(data={})
        self.discovery = self.host.Discovery(self.host.Options(mock.Mock(), 0, self.status, None,
                                                              "224.0.0.251", 5353))

    def lease(self, generation, find=True):
        answer = {"contact_discovery": {"find": find, "find_generation": generation,
                                        "advertise": False, "browse": False, "ports": [],
                                        "expires_at": self.host.stamp(self.wall + 20)}}
        return self.host.engine.lease_from(answer, self.now, self.now, self.wall)

    def report_generation(self):
        return self.host.report_fields(self.discovery.report()).get("contact_find_generation", 0)

    def test_active_browser_echoes_generation_and_replacement_only_after_apply(self):
        session = mock.Mock(browser=self.host.engine.Browser())
        with mock.patch.object(self.host.engine, "Session", return_value=session):
            self.assertEqual(self.report_generation(), 0)
            self.discovery.apply(self.lease(7))
            self.assertEqual(self.report_generation(), 7)
            self.discovery.apply(self.lease(8))
            self.assertEqual(self.report_generation(), 8)
            self.discovery.apply(self.lease(0, find=False))
            self.assertEqual(self.report_generation(), 0, "revoked Find still claimed to be scanning")

    def test_failed_browser_does_not_claim_scan_started(self):
        with mock.patch.object(self.host.engine, "Session", side_effect=OSError("cannot join multicast")):
            with contextlib.redirect_stderr(io.StringIO()):
                self.discovery.apply(self.lease(7))
        self.assertEqual(self.report_generation(), 0)

    def test_expired_browser_stops_acknowledging_find_generation(self):
        session = mock.Mock(browser=self.host.engine.Browser())
        with mock.patch.object(self.host.engine, "Session", return_value=session):
            self.discovery.apply(self.lease(7))
            self.assertEqual(self.report_generation(), 7)
            self.now += 21
            self.discovery.expire(self.now)
            self.assertEqual(self.report_generation(), 0)

    def test_default_idle_lease_poll_is_fifteen_seconds(self):
        args = self.host.arguments(["run", "--state-dir", "/tmp/discovery-test-state"])
        self.assertEqual(args.interval, 15)


if __name__ == "__main__":
    unittest.main()
