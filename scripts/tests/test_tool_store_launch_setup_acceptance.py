"""Missing store locks fail closed; lock creation leaves a traversable store."""

from contextlib import redirect_stderr
import io
import os
from pathlib import Path
import runpy
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
import tool_store


class LaunchSetupAcceptance(unittest.TestCase):
    def test_missing_lock_reports_maintenance_without_executing(self):
        launcher = runpy.run_path(str(ROOT / "infra/image/pairtask-tool"))
        output = io.StringIO()
        with patch.object(tool_store, "launch_lock", side_effect=FileNotFoundError("lock")), \
                patch.object(os, "execve", side_effect=AssertionError("tool started")), \
                redirect_stderr(output):
            with self.assertRaises(SystemExit) as stopped:
                launcher["main"](["pairtask-tool", "rtk", "init", "-g"])
        self.assertEqual(stopped.exception.code, 75)
        self.assertIn("maintenance", output.getvalue())
        self.assertIn("rollout", output.getvalue())

    def test_created_store_is_traversable_with_restrictive_umask(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "store"
            old_umask = os.umask(0o077)
            try:
                with tool_store.StoreLock(root, exclusive=True):
                    self.assertEqual(root.stat().st_mode & 0o777, 0o755)
                    self.assertEqual((root / "lock").stat().st_mode & 0o777, 0o644)
            finally:
                os.umask(old_umask)


if __name__ == "__main__":
    unittest.main()
