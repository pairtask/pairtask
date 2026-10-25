import hashlib
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import call, patch
from urllib.error import HTTPError
import zipfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import sandbox_operator_download as download
import sandbox_operator_runtime as runtime


URL = "https://example.invalid/tofu.zip"


def http_error(code):
    return HTTPError(URL, code, "download failed", {}, io.BytesIO())


class OperatorDownloadTests(unittest.TestCase):
    def test_transient_failure_retries_then_installs_verified_binary(self):
        archive = io.BytesIO()
        with zipfile.ZipFile(archive, "w") as package:
            package.writestr("tofu", b"verified binary")
        contents = archive.getvalue()
        lock = {"tofu_version": "1.0.0", "architectures": {"x86_64": {
            "archive": "tofu.zip", "sha256": hashlib.sha256(contents).hexdigest(),
            "binary_sha256": hashlib.sha256(b"verified binary").hexdigest()}}}
        operator = runtime.Operator("local", {"architectures": ["x86_64"]}, "pool", "network")
        error = http_error(504)
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(Path, "read_text", return_value=json.dumps(lock)), \
                patch.object(download, "urlopen", side_effect=[error, io.BytesIO(contents)]) as request, \
                patch.object(download.time, "sleep") as sleep, \
                patch.object(operator, "push") as push, patch.object(operator, "execute"):
            operator.install(Path(directory))
            self.assertEqual((Path(directory) / "tofu").read_bytes(), b"verified binary")
            push.assert_called_once_with(Path(directory) / "tofu", "/operator/bin/tofu")
        self.assertEqual(request.call_count, 2)
        sleep.assert_called_once_with(1)
        self.assertTrue(error.fp.closed)

    def test_repeated_transient_failures_stop_after_three_attempts(self):
        with patch.object(download, "urlopen", side_effect=http_error(503)) as request, \
                patch.object(download.time, "sleep") as sleep:
            with self.assertRaises(HTTPError):
                download.download_archive(URL)
        self.assertEqual(request.call_count, 3)
        self.assertEqual(sleep.call_args_list, [call(1), call(2)])

    def test_permanent_http_errors_are_not_retried(self):
        for code in (401, 403, 404):
            with self.subTest(code=code), \
                    patch.object(download, "urlopen", side_effect=http_error(code)) as request, \
                    patch.object(download.time, "sleep") as sleep:
                with self.assertRaises(HTTPError):
                    download.download_archive(URL)
                request.assert_called_once()
                sleep.assert_not_called()

    def test_retry_does_not_bypass_checksum_verification(self):
        operator = runtime.Operator("local", {"architectures": ["x86_64"]}, "pool", "network")
        with tempfile.TemporaryDirectory() as directory, \
                patch.object(download, "urlopen", side_effect=[http_error(504), io.BytesIO(b"wrong")]), \
                patch.object(download.time, "sleep"), \
                patch.object(operator, "push") as push, patch.object(operator, "execute") as execute:
            with self.assertRaisesRegex(ValueError, "checksum mismatch"):
                operator.install(Path(directory))
            push.assert_not_called()
            execute.assert_not_called()


if __name__ == "__main__":
    unittest.main()
