"""Tool launchers can read the root-managed lock and still exclude updates."""

import fcntl
import os
from pathlib import Path
import sys
import tempfile
import threading
from types import SimpleNamespace
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "infra/image"))
import tool_store


class LaunchPermissionsAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.lock = self.root / "lock"
        self.lock.write_text("stable lock inode\n")
        self.store = SimpleNamespace(root=self.root)

    @unittest.skipIf(os.geteuid() == 0, "permission enforcement needs a non-root reader")
    def test_shared_launcher_needs_only_read_permission(self):
        self.lock.chmod(0o444)
        self.addCleanup(self.lock.chmod, 0o644)
        before = self.lock.stat()
        with tool_store.launch_lock(self.store):
            self.assertEqual(self.lock.read_text(), "stable lock inode\n")
        self.assertEqual(self.lock.stat().st_ino, before.st_ino)
        self.assertEqual(self.lock.stat().st_mode, before.st_mode)

    def test_shared_reader_does_not_create_an_unactivated_store(self):
        absent = self.root / "missing"
        with self.assertRaises(OSError):
            with tool_store.launch_lock(SimpleNamespace(root=absent)):
                self.fail("a reader created an update lock")
        self.assertFalse(absent.exists())

    def test_exclusive_lock_is_readable_even_with_restrictive_umask(self):
        self.lock.unlink()
        old_umask = os.umask(0o077)
        try:
            with tool_store.StoreLock(self.root, exclusive=True):
                self.assertEqual(self.lock.stat().st_mode & 0o777, 0o644)
        finally:
            os.umask(old_umask)

    def test_shared_reader_waits_for_exclusive_writer(self):
        attempted, entered = threading.Event(), threading.Event()
        errors = []

        def reader():
            attempted.set()
            try:
                with tool_store.launch_lock(self.store):
                    entered.set()
            except Exception as error:
                errors.append(error)

        with self.lock.open("r") as writer:
            fcntl.flock(writer, fcntl.LOCK_EX)
            thread = threading.Thread(target=reader, daemon=True)
            thread.start()
            self.assertTrue(attempted.wait(2))
            self.assertFalse(entered.wait(0.05), "shared lock bypassed writer")
            fcntl.flock(writer, fcntl.LOCK_UN)
            thread.join(2)
        self.assertFalse(thread.is_alive())
        self.assertEqual(errors, [])
        self.assertTrue(entered.is_set())


if __name__ == "__main__":
    unittest.main()
