import json
from pathlib import Path
import sys
import unittest

from discovery_host_fixture import OperatorSocket, lease, run_helper


class DiscoveryHostAcceptance(unittest.TestCase):
    def operator(self, value):
        fixture = OperatorSocket(value)
        self.addCleanup(fixture.close)
        return fixture

    def test_disabled_lease_does_not_submit_candidates_or_open_operator_session(self):
        operator = self.operator(lease())
        run_helper(self, operator)
        self.assertEqual([r.get("contact_op") for r in operator.requests], ["discovery"])
        self.assertTrue(all(r.get("type") == "operator_contact" for r in operator.requests))

    def test_expired_lease_cannot_submit_even_if_browse_was_true(self):
        operator = self.operator(lease(advertise=True, browse=True, seconds=-60))
        run_helper(self, operator)
        self.assertEqual([r.get("contact_op") for r in operator.requests], ["discovery"])

    def test_tailnet_names_and_ip_fallback_go_through_candidate_op(self):
        operator = self.operator(lease(browse=True))
        fake = Path(operator.directory.name) / "tailscale fixture"
        status = {"Peer": {"node": {"DNSName": "work.tailnet.ts.net.", "Online": True,
                                    "TailscaleIPs": ["100.70.1.2"]}}}
        fake.write_text("#!" + sys.executable + "\nprint(" + repr(json.dumps(status)) + ")\n")
        fake.chmod(0o700)
        run_helper(self, operator, {"PAIRTASK_DISCOVERY_TAILSCALE": str(fake)})
        submissions = [r["contact_candidates"] for r in operator.requests if r.get("contact_op") == "candidates"]
        self.assertTrue(submissions, "helper never submitted authorized host endpoint hints")
        candidates = [value for batch in submissions for value in batch]
        self.assertIn("work.tailnet.ts.net:8445", candidates)
        self.assertIn("100.70.1.2:8445", candidates)
        self.assertTrue(all(len(batch) <= 8 for batch in submissions))
        self.assertTrue(all(r.get("type") != "operator_session_open" for r in operator.requests))
