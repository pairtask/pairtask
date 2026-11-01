"""Image name-file validation and exact systemd EnvironmentFile encoding."""

from contextlib import ExitStack
import json
import hashlib
import os
from pathlib import Path
import runpy
import stat
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import MagicMock, patch

ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/pairtask-pairing-setup"))
VECTORS = json.loads((ROOT / "testdata/pairing_name_vectors.json").read_text())


class PairingNameSetupAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(dir="/tmp", prefix="pairing-name-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.name = self.root / "pairing-name.json"
        self.env = self.root / "pairing.env"
        self.ready = self.root / "pairing-name.ready"

    def write_name(self, value):
        self.name.write_text(json.dumps({"name": value}))
        self.release_name()

    def release_name(self):
        self.ready.write_text(hashlib.sha256(self.name.read_bytes()).hexdigest())
        self.ready.chmod(0o600)

    def root_marker(self):
        original = Path.lstat

        def lstat(path):
            info = original(path)
            if self.is_marker(path):
                return SimpleNamespace(st_mode=info.st_mode, st_uid=0)
            return info

        return patch.object(Path, "lstat", lstat)

    def is_marker(self, path):
        if path == self.ready:
            return True
        if path.parent != self.root:
            return False
        return path.name.startswith(".pairing-name.ready.")

    def secured(self):
        namespace = SETUP["secured"].__globals__
        with ExitStack() as stack:
            stack.enter_context(patch.dict(namespace, {name: MagicMock() for name in
                ("check_role", "require_ids", "prepare_shared_dir", "private_parent")}))
            stack.enter_context(patch.object(SETUP["pwd"], "getpwnam",
                return_value=SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())))
            stack.enter_context(patch.object(SETUP["os"], "chown"))
            stack.enter_context(self.root_marker())
            SETUP["secured"](state=self.root / "state", ingress=self.root / "ingress",
                             egress=self.root / "egress", env=self.env, name_path=self.name, ready=self.ready)

    def test_read_name_missing_and_shared_vectors(self):
        self.assertIsNone(SETUP["read_name"](self.name))
        for case in VECTORS:
            with self.subTest(case=case["id"]):
                self.write_name(case["name"])
                if case["valid"]:
                    self.assertEqual(SETUP["read_name"](self.name), case["name"])
                else:
                    with self.assertRaises(ValueError):
                        SETUP["read_name"](self.name)

    def test_invalid_name_file_never_leaves_old_environment(self):
        bodies = ["{broken", "[]", "null", "{}", '{"name":42}', '{"name":null}',
                  json.dumps({"name": "界" * 22}), json.dumps({"name": "bad\nname"})]
        for body in bodies:
            with self.subTest(body=body):
                self.name.write_text(body)
                self.release_name()
                self.env.write_text('PAIRTASK_PAIRING_NAME="Old name"\n')
                with self.assertRaises(ValueError):
                    self.secured()
                self.assertFalse(self.env.exists(), "invalid guest file retained a stale name")

    def test_environment_file_quotes_special_values_exactly(self):
        values = ('  "quoted" \\ $cash % #tag;  ', "é界😀", "", "trailing\\", "'single'")
        for value in values:
            with self.subTest(value=value):
                expected = "PAIRTASK_PAIRING_NAME=" + json.dumps(value, ensure_ascii=False) + "\n"
                self.assertEqual(SETUP["env_line"]("PAIRTASK_PAIRING_NAME", value), expected)
        for value in ("bad\nline", "bad\rline", "bad\0line"):
            with self.assertRaises(ValueError):
                SETUP["env_line"]("PAIRTASK_PAIRING_NAME", value)

    def test_secured_generates_exact_name_and_clears_to_default(self):
        value = '  "quoted" \\ $cash % #tag;  '
        self.write_name(value)
        self.secured()
        lines = self.env.read_text().splitlines()
        self.assertIn("PAIRTASK_PAIRING_NAME=" + json.dumps(value), lines)
        self.assertEqual(sum(line.startswith("PAIRTASK_PAIRING_NAME=") for line in lines), 1)
        self.write_name("")
        self.secured()
        self.assertNotIn("PAIRTASK_PAIRING_NAME=", self.env.read_text())
        self.name.unlink()
        with self.assertRaises(FileNotFoundError):
            self.secured()
        self.assertFalse(self.env.exists(), "missing provisioned name retained stale settings")

    def test_missing_or_stale_ready_never_retains_old_environment(self):
        self.write_name("Old name")
        self.secured()
        self.ready.unlink()
        with self.assertRaises(FileNotFoundError):
            self.secured()
        self.assertFalse(self.env.exists())
        self.release_name()
        self.name.write_text(json.dumps({"name": "Changed name"}))
        self.env.write_text('PAIRTASK_PAIRING_NAME="Old name"\n')
        with self.assertRaisesRegex(ValueError, "not the name the host released"):
            self.secured()
        self.assertFalse(self.env.exists())

    def test_marker_refuses_wrong_owner_writable_symlink_or_bad_digest(self):
        self.write_name("Name")
        for mode, owner in ((stat.S_IFREG | 0o600, 1001), (stat.S_IFREG | 0o622, 0), (stat.S_IFLNK | 0o777, 0)):
            with self.subTest(mode=oct(mode), owner=owner):
                with patch.object(Path, "lstat", return_value=SimpleNamespace(st_mode=mode, st_uid=owner)):
                    with self.assertRaisesRegex(ValueError, "unsafe"):
                        SETUP["read_marker"](self.ready)
        self.ready.write_text("not a digest")
        with self.root_marker(), self.assertRaisesRegex(ValueError, "malformed"):
            SETUP["read_marker"](self.ready)

    def test_released_name_hashes_and_parses_one_snapshot(self):
        self.write_name("Expected name")
        original = Path.read_bytes
        reads = []

        def read(path):
            if path == self.name:
                reads.append(path)
                if len(reads) > 1:
                    return b'{"name":"Wrong later bytes"}'
            return original(path)

        with self.root_marker(), patch.object(Path, "read_bytes", read):
            self.assertEqual(SETUP["released_name"](self.name, self.ready), "Expected name")
        self.assertEqual(reads, [self.name])

    def test_publish_ready_flushes_then_atomically_replaces_and_refuses_bad_temp(self):
        self.write_name("Name")
        expected = self.ready.read_bytes()
        temporary = self.root / ".pairing-name.ready.fixture"
        temporary.write_bytes(expected)
        temporary.chmod(0o600)
        events = []
        fsync, replace = os.fsync, os.replace

        def flush(descriptor):
            events.append("flush")
            return fsync(descriptor)

        def publish(before, after):
            events.append("replace")
            return replace(before, after)

        with self.root_marker(), patch.object(os, "fsync", flush), patch.object(os, "replace", publish):
            SETUP["publish_ready"](temporary, self.ready)
        self.assertEqual(events, ["flush", "replace", "flush"])
        self.assertEqual(self.ready.read_bytes(), expected)
        self.assertEqual(self.ready.stat().st_mode & 0o777, 0o600)
        self.assertFalse(temporary.exists())
        temporary.write_text("invalid")
        temporary.chmod(0o600)
        with self.root_marker(), self.assertRaisesRegex(ValueError, "malformed"):
            SETUP["publish_ready"](temporary, self.ready)
        self.assertFalse(temporary.exists())
        self.assertEqual(self.ready.read_bytes(), expected)


if __name__ == "__main__":
    unittest.main()
