"""Slow peer probes must not hold up lease renewal or discovery reports."""

import copy
import threading
import time
from types import SimpleNamespace
import unittest
from unittest import mock

from test_discovery_report_acceptance import helper


class DiscoveryFixture:
    def __init__(self, clock):
        self.clock = clock
        self.lease = SimpleNamespace(ports=[7445], deadline=1000)
        self.session = None
        self.status = mock.Mock()
        self.snapshot = {"found": []}
        self.reports = []
        self.probing = False
        self.probe_started = threading.Event()
        self.release_probe = threading.Event()

    def report(self):
        return copy.deepcopy(self.snapshot)

    def ask_lease(self, report):
        self.reports.append((self.clock(), report))
        return self.lease

    def active(self, now):
        return now < self.lease.deadline

    def apply(self, lease):
        self.lease = lease

    def browsing(self, now):
        return self.probing

    def submission_open(self, now, next_submit):
        return now >= next_submit

    def lan(self):
        return []

    def submit(self, lan, ports):
        self.probe_started.set()
        self.release_probe.wait(5)
        return []

    def submitted(self, result, error):
        pass


class DiscoveryProgressAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = helper()
        self.now = 100.0
        self.clock = lambda: self.now
        patch = mock.patch.object(self.host.time, "monotonic", self.clock)
        patch.start()
        self.addCleanup(patch.stop)
        self.discovery = DiscoveryFixture(self.clock)
        self.addCleanup(self.discovery.release_probe.set)
        self.loop = self.host.Loop(self.discovery, 15)
        self.pump(100)
        self.assertEqual(len(self.discovery.reports), 1)

    def pump(self, now):
        self.now = now
        for _ in range(10):
            self.loop.dispatch(now)
            time.sleep(0.002)

    def test_due_lease_proceeds_while_candidate_probe_is_blocked(self):
        self.discovery.probing = True
        self.pump(101)
        self.assertTrue(self.discovery.probe_started.is_set())
        self.pump(115)
        self.assertEqual(len(self.discovery.reports), 2,
                         "blocked candidate probe prevented the due discovery lease")
        self.assertFalse(self.discovery.release_probe.is_set())

    def test_new_invitation_is_reported_within_five_seconds(self):
        self.discovery.snapshot["found"] = [{"name": "Office Mac", "host": "192.168.8.20",
                                              "port": 8445, "seen_at": "2026-11-09T18:00:00Z"}]
        for now in range(101, 107):
            self.pump(now)
        self.assertTrue(any(report.get("found") for _, report in self.discovery.reports),
                        "heard invitation waited for the regular lease interval")
    def test_goodbye_is_reported_within_five_seconds(self):
        self.discovery.snapshot["found"] = [{"name": "Office Mac", "host": "192.168.8.20",
                                              "port": 8445, "seen_at": "2026-11-09T18:00:00Z"}]
        self.pump(115)
        self.assertTrue(self.discovery.reports[-1][1]["found"])
        self.discovery.snapshot["found"] = []
        for now in range(116, 122):
            self.pump(now)
        self.assertEqual(self.discovery.reports[-1][1]["found"], [],
                         "goodbye was not promptly reported to clear the picker")

    def test_announcement_success_is_reported_within_five_seconds(self):
        self.discovery.snapshot["announcing"] = {"id": "invitation", "name": "Office Mac", "sent": True}
        for now in range(101, 107):
            self.pump(now)
        self.assertTrue(any(report.get("announcing", {}).get("sent")
                            for _, report in self.discovery.reports),
                        "sent invitation still appeared pending until the regular lease")

    def test_timestamp_only_changes_do_not_trigger_extra_lease_requests(self):
        row = {"name": "Office Mac", "host": "192.168.8.20", "port": 8445,
               "seen_at": "2026-11-09T18:00:00Z"}
        self.discovery.snapshot["found"] = [row]
        self.pump(115)
        count = len(self.discovery.reports)
        row["seen_at"] = "2026-11-09T18:00:01Z"
        for now in range(116, 122):
            self.pump(now)
        self.assertEqual(len(self.discovery.reports), count,
                         "a repeated unchanged advertisement triggered another lease request")

    def test_changing_announcements_cannot_request_more_than_once_per_five_seconds(self):
        for now in range(101, 115):
            self.discovery.snapshot["announcing"] = {"id": str(now), "name": "Office Mac", "sent": True}
            self.pump(now)
        asked = [when for when, _ in self.discovery.reports]
        self.assertGreater(len(asked), 1, "changed announcements were never reported")
        self.assertTrue(all(later - earlier >= 5 for earlier, later in zip(asked, asked[1:])),
                        "changing advertisements caused a lease-request storm: " + repr(asked))


if __name__ == "__main__":
    unittest.main()
