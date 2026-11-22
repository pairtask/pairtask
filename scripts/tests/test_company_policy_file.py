from pathlib import Path
import runpy
import tempfile
import unittest
from unittest.mock import patch
import subprocess


class CompanyPolicyFile(unittest.TestCase):
    def test_host_file_is_streamed_to_secured_command(self):
        script = Path(__file__).resolve().parents[1] / "sandbox-pairbook-policy.py"
        main = runpy.run_path(str(script))["main"]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "policy file.json"
            path.write_bytes(b'{"base_version":3}')
            def receive(argv, stdin):
                self.assertEqual(argv[2:], ["secured", "pairtask", "operator", "space", "admin",
                                           "policy-set", "space-id", "-", "--json", "--pairbook",
                                           "https://directory.example"])
                self.assertEqual(stdin.read(), path.read_bytes())
                return subprocess.CompletedProcess(argv, 0)

            with patch("subprocess.run", receive):
                self.assertEqual(main(["space-id", str(path), "--pairbook",
                                       "https://directory.example", "--json"]), 0)
