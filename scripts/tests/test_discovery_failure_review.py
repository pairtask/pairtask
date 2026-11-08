"""An active advertising lease must not report success when setup fails."""

import contextlib
import importlib.util
import io
from pathlib import Path
import time
import unittest
from unittest import mock


class DiscoveryFailureReview(unittest.TestCase):
    def test_advertising_setup_failure_exits_unsuccessfully(self):
        path = Path(__file__).resolve().parents[1] / "sandbox-discovery.py"
        spec = importlib.util.spec_from_file_location("failure_review", path)
        host = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(host)
        operator = mock.Mock()
        operator.call.return_value = {"contact_discovery": {
            "advertise": True, "browse": False, "ports": [],
            "expires_at": host.stamp(time.time() + 60)}}
        discovery = host.Discovery(host.Options(operator, 7445, mock.Mock(), None,
                                               "224.0.0.251", 5353))
        with mock.patch.object(host.engine, "Session", side_effect=OSError("multicast setup failed")):
            with contextlib.redirect_stderr(io.StringIO()) as stderr:
                result = host.run_once(discovery)
        self.assertNotEqual(result, 0, "an active advert that could not open a socket reported success")
        self.assertIn("multicast setup failed", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
