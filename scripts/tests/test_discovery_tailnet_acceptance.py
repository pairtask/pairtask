import unittest

from discovery_test_support import engine


class DiscoveryTailnetAcceptance(unittest.TestCase):
    def test_actual_dns_name_and_overlay_ips_use_remote_port_hints(self):
        status = {"Peer": {"node": {"DNSName": "work.tailnet.ts.net.",
                                    "HostName": "display-is-not-an-address",
                                    "TailscaleIPs": ["100.70.1.2", "fd7a:115c:a1e0::2"]}}}
        candidates = engine(self).tailnet_candidates(status, [8445])
        self.assertIn("work.tailnet.ts.net:8445", candidates)
        self.assertIn("100.70.1.2:8445", candidates)
        self.assertIn("[fd7a:115c:a1e0::2]:8445", candidates)
        self.assertLess(candidates.index("work.tailnet.ts.net:8445"), candidates.index("100.70.1.2:8445"))
        self.assertFalse(any("display-is-not-an-address" in value for value in candidates))

    def test_no_remote_port_is_invented(self):
        status = {"Peer": {"node": {"DNSName": "work.tailnet.ts.net.", "TailscaleIPs": ["100.70.1.2"]}}}
        self.assertEqual(engine(self).tailnet_candidates(status, []), [])

    def test_display_names_and_invented_aliases_are_not_endpoints(self):
        status = {"Peer": {"node": {"HostName": "reviewer", "Aliases": ["arbitrary.example"]}}}
        self.assertEqual(engine(self).tailnet_candidates(status, [7445]), [])

    def test_invalid_status_cannot_make_loopback_endpoint(self):
        status = {"Peer": {"node": {"DNSName": "", "TailscaleIPs": ["127.0.0.1", "169.254.1.2", "not-an-ip"]}}}
        self.assertEqual(engine(self).tailnet_candidates(status, [7445]), [])
