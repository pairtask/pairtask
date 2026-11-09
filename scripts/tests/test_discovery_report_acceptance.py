"""The host must tell the broker when the last discoverable peer disappears."""

import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))


def helper():
    spec = importlib.util.spec_from_file_location("sandbox_discovery_report", SCRIPTS / "sandbox-discovery.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class DiscoveryReportAcceptance(unittest.TestCase):
    def test_empty_host_snapshot_clears_previous_invitation_and_device_rows(self):
        marshal = helper().report_fields
        first = marshal({"found": [{"name": "Office Mac", "host": "192.168.8.20", "port": 7445,
                                    "seen_at": "2026-11-09T07:00:00Z"}],
                         "devices": [{"name": "work.tailnet.ts.net", "address": "100.70.1.2"}]})
        self.assertEqual(len(first["contact_found"]), 1)
        self.assertEqual(len(first["contact_devices"]), 1)
        cleared = marshal({"found": [], "devices": []})
        self.assertEqual(cleared["contact_found"], [])
        self.assertEqual(cleared["contact_devices"], [])


if __name__ == "__main__":
    unittest.main()
