"""Verify the operator executable against pins before it reaches a container."""

from contextlib import ExitStack
import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch
import zipfile

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "scripts"))
import sandbox_operator_runtime as runtime


class OperatorBinaryAcceptanceTests(unittest.TestCase):
    def setUp(self):
        stack = ExitStack()
        self.addCleanup(stack.close)
        self.directory = Path(stack.enter_context(tempfile.TemporaryDirectory()))
        self.lock = self.directory / "infra/operator/tools.lock.json"
        self.lock.parent.mkdir(parents=True)
        self.payload = b"verified operator executable"
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("tofu", self.payload)
        self.archive = buffer.getvalue()
        stack.enter_context(patch.object(runtime, "ROOT", self.directory))
        stack.enter_context(patch.object(runtime, "download_archive", return_value=self.archive))
        self.operator = runtime.Operator("local", {"architectures": ["x86_64"]}, "pool", "network")
        self.operator.push, self.operator.execute = Mock(), Mock()

    def write_lock(self, **overrides):
        artifact = {
            "archive": "tofu-test.zip",
            "sha256": hashlib.sha256(self.archive).hexdigest(),
            "binary_sha256": hashlib.sha256(self.payload).hexdigest(),
        }
        artifact.update(overrides)
        self.lock.write_text(json.dumps({
            "tofu_version": "1.12.6", "architectures": {"x86_64": artifact},
        }))

    def test_valid_independent_archive_and_binary_pins_install(self):
        self.write_lock()
        self.operator.install(self.directory)
        source, destination = self.operator.push.call_args.args
        self.assertEqual(source.read_bytes(), self.payload)
        self.assertEqual(destination, "/operator/bin/tofu")
        self.operator.execute.assert_any_call("/operator/bin/tofu", "version")

    def test_wrong_archive_hash_never_pushes_an_executable(self):
        self.write_lock(sha256="0" * 64)
        with self.assertRaises(ValueError):
            self.operator.install(self.directory)
        self.operator.push.assert_not_called()
        self.operator.execute.assert_not_called()

    def test_valid_archive_cannot_bypass_wrong_binary_pin(self):
        self.write_lock(binary_sha256="0" * 64)
        with self.assertRaises(ValueError):
            self.operator.install(self.directory)
        self.operator.push.assert_not_called()
        self.operator.execute.assert_not_called()

    def test_repacked_archive_can_keep_the_same_binary_pin(self):
        buffer = io.BytesIO()
        with zipfile.ZipFile(buffer, "w") as archive:
            archive.writestr("tofu", self.payload)
            archive.comment = b"new archive metadata, same executable"
        changed = buffer.getvalue()
        self.assertNotEqual(changed, self.archive)
        self.write_lock(sha256=hashlib.sha256(changed).hexdigest())
        with patch.object(runtime, "download_archive", return_value=changed):
            self.operator.install(self.directory)
        source, _ = self.operator.push.call_args.args
        self.assertEqual(source.read_bytes(), self.payload)
