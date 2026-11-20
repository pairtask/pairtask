"""pk status says why the agents step failed, safely and briefly."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
FAKE_INCUS = r'''
import os, sys
args = sys.argv[1:]
if 'list' in args:
    print('[]')
    sys.exit(0)
if 'exec' in args and args[-2:] == ['operator', 'agents']:
    sys.stderr.write(os.environ['AGENTS_STDERR'])
    sys.exit(int(os.environ.get('AGENTS_EXIT', '1')))
sys.exit(0)
'''


class StatusReasonAcceptance(unittest.TestCase):
    def status(self, stderr, code="1"):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            executable = root / "incus"
            executable.write_text("#!" + sys.executable + "\n" + FAKE_INCUS)
            executable.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "PAIRTASK_OPERATOR_HOME": str(root / "operator"), "PAIRTASK_ALLOW_ROOT": "1",
                   "AGENTS_STDERR": stderr, "AGENTS_EXIT": code}
            result = subprocess.run([sys.executable, str(ROOT / "scripts/pairtask.py"), "status", "--remote", "local",
                                     "--project", "status-reason-proof"], env=env, capture_output=True, text=True, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        lines = [line for line in result.stdout.splitlines() if line.startswith("agents:")]
        self.assertEqual(len(lines), 1, result.stdout)
        return lines[0]

    def test_the_commands_own_reason_is_shown(self):
        line = self.status("noise first\noperator agents unavailable: this broker doesn't support agent status\n")
        self.assertIn("unavailable", line)
        self.assertIn("this broker doesn't support agent status", line)
        self.assertNotIn("noise first", line)

    def test_different_failures_read_differently(self):
        unsupported = self.status("operator agents unavailable: unsupported by this broker\n")
        unreachable = self.status("Error: Instance is not running\n")
        self.assertNotEqual(unsupported, unreachable)
        self.assertIn("Instance is not running", unreachable)

    def test_reason_is_one_bounded_line_without_terminal_controls(self):
        line = self.status("operator agents unavailable: \x1b[2J\x07bad reason " + "x" * 5000 + "\n")
        self.assertNotRegex(line, r"[\x00-\x1f\x7f]")
        self.assertIn("bad reason", line)
        self.assertLessEqual(len(line), 300)

    def test_no_reason_still_says_unavailable(self):
        self.assertIn("unavailable", self.status(""))


if __name__ == "__main__":
    unittest.main()
