"""The discovery helper's service PATH: only the folders of the tools it runs, plus the system's."""

from pathlib import Path
import sys
import unittest
from unittest import mock
import os

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_discovery_service as service  # noqa: E402


class ServicePathTests(unittest.TestCase):
    def test_path_holds_only_the_tools_folders_and_the_system(self):
        found = {"python3": "/opt/homebrew/bin/python3", "incus": "/opt/homebrew/bin/incus",
                 "git": "/opt/git/bin/git", "tailscale": "/usr/local/bin/tailscale"}
        path = service.service_path(which=found.get)
        self.assertEqual(path, "/opt/homebrew/bin:/opt/git/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")

    def test_missing_tools_leave_the_system_path(self):
        self.assertEqual(service.service_path(which=lambda tool: None), "/usr/bin:/bin:/usr/sbin:/sbin")

    def test_the_installing_shell_path_is_not_copied(self):
        shell = "/Users/someone/sdk/flutter/bin:/Users/someone/go/bin:/Applications/Little Snitch.app/x:/usr/bin"
        with mock.patch.dict(os.environ, {"PATH": shell}):
            with mock.patch.object(service.shutil, "which", lambda tool: None):
                env = service.Manager("pairtask", "local", host=service.Host("Darwin", "/tmp/home", None)).environment()
        self.assertEqual(env["PATH"], "/usr/bin:/bin:/usr/sbin:/sbin")
        self.assertNotIn("flutter", env["PATH"])


if __name__ == "__main__":
    unittest.main()
