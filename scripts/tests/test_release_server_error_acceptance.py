"""A failed release service must not be reported as an unpublished build."""

from pathlib import Path
import sys
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_releases as releases


class ReleaseServerErrorAcceptance(unittest.TestCase):
    def test_server_errors_identify_service_failure_and_retry_without_downloading(self):
        for selection in ("latest", "workspace-v0.0.0-build.877.1"):
            for code in (500, 502, 503, 504):
                error = HTTPError("https://api.github.com/", code, "Server error", {}, None)
                with self.subTest(selection=selection, code=code):
                    with patch.object(releases, "urlopen", side_effect=error), \
                         patch.object(releases, "prepare_image") as prepare:
                        with self.assertRaises(ValueError) as raised:
                            releases.download_release("aarch64", selection, "pairtask/pairtask")
                    message = str(raised.exception)
                    self.assertIn("HTTP %d" % code, message)
                    self.assertIn("pairtask/pairtask", message)
                    self.assertIn(selection, message)
                    self.assertIn("retry", message.lower())
                    self.assertNotIn("Cannot find published release", message)
                    self.assertNotIn("successful main release build", message)
                    self.assertIs(raised.exception.__cause__, error)
                    prepare.assert_not_called()


if __name__ == "__main__":
    unittest.main()
