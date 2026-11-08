"""Broken optional metadata must not break the public offline summary."""

import contextlib
import io
import json
import os
import runpy
import sys
import unittest
from unittest import mock

from update_host_fixture import HostBoundary, NEW, SCRIPTS


class ManualUpdateCacheAcceptance(unittest.TestCase):
    def setUp(self):
        self.host = HostBoundary(self)

    def summary_with_cache(self, entry):
        path = self.host.root / "pairtask" / "version-cache.json"
        path.parent.mkdir(exist_ok=True)
        path.write_text(json.dumps({self.host.repo: entry}))
        output = io.StringIO()
        with mock.patch.dict(os.environ, {"XDG_STATE_HOME": str(self.host.root)}), \
                mock.patch.object(sys, "path", [str(SCRIPTS), *sys.path]), \
                mock.patch.object(sys, "argv", ["pairtask"]), \
                mock.patch("subprocess.run", side_effect=self.host.process), \
                mock.patch("urllib.request.urlopen", side_effect=AssertionError("offline summary fetched GitHub")), \
                contextlib.redirect_stdout(output):
            cli = runpy.run_path(str(SCRIPTS / "pairtask.py"))
            try:
                code = cli["main"](["version", "--summary", "--remote", "local"])
            except SystemExit as error:
                code = error.code
        self.assertEqual(code, 0)
        self.assertIn("version:", output.getvalue())
        self.assertNotIn("up to date", output.getvalue().lower())

    def test_cached_tag_without_a_check_time_is_discarded(self):
        self.summary_with_cache({"latest": {"tag": NEW}})

    def test_cached_check_time_with_wrong_type_is_discarded(self):
        self.summary_with_cache({"latest": {"tag": NEW, "checked_at": "yesterday"}})

    def test_cached_mappings_with_wrong_type_are_discarded(self):
        self.summary_with_cache({"releases": ["not a mapping"]})

    def test_nonfinite_cache_time_is_discarded(self):
        self.summary_with_cache({"latest": {"tag": NEW, "checked_at": float("nan")}})


if __name__ == "__main__":
    unittest.main()
