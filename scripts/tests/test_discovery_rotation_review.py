"""Repeated bounded submissions eventually try every available endpoint source."""

import importlib.util
from pathlib import Path
import unittest
from unittest import mock


class DiscoveryRotationReview(unittest.TestCase):
    def test_unrelated_tailnet_peers_cannot_starve_known_peer_or_lan(self):
        path = Path(__file__).resolve().parents[1] / "sandbox-discovery.py"
        spec = importlib.util.spec_from_file_location("rotation_review", path)
        host = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(host)
        operator = mock.Mock()
        operator.call.return_value = {"contact_candidates_result": []}
        discovery = host.Discovery(host.Options(operator, 0, mock.Mock(), "tailscale", "224.0.0.251", 5353))
        peers = {str(i): {"DNSName": "peer%d.tailnet.ts.net." % i, "Online": True}
                 for i in range(9)}
        lan = "192.168.50.42:8445"
        with mock.patch.object(host.engine, "tailscale_status", return_value={"Peer": peers}):
            for _ in range(4):
                discovery.submit([lan], [8445])
        batches = [call.args[1] for call in operator.call.call_args_list]
        self.assertTrue(all(len(batch) <= 8 for batch in batches))
        attempted = {endpoint for batch in batches for endpoint in batch}
        self.assertIn("peer8.tailnet.ts.net:8445", attempted,
                      "the paired ninth peer is excluded forever by unrelated peers")
        self.assertIn(lan, attempted, "tailnet candidates permanently crowd out LAN recovery")


if __name__ == "__main__":
    unittest.main()
