"""Synthetic acceptance tests for pairing-code detection in fixture logs."""

from contextlib import redirect_stdout
import io
from pathlib import Path
import runpy
import tempfile
from types import SimpleNamespace
import unittest


ROOT = Path(__file__).resolve().parents[2]
ROUTE = runpy.run_path(str(ROOT / "scripts/sandbox-pairing-route.py"))
CODE = "123-456"
TIMESTAMP_LOG = '{"time":"2026-11-01T09:30:19.123456789Z","msg":"session complete"}'


class PairingCodeLogAcceptanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="pairing-code-log-", dir="/tmp")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.journals = {"secured": "", "connector": ""}
        self.host = SimpleNamespace(workdir=self.root)
        self.deployment = SimpleNamespace(output=self.journal)
        self.log_path = self.root / "host-broker.log"
        self.log_path.write_text("")

    def journal(self, role, *command):
        self.assertEqual(command, ("journalctl", "--no-pager", "-o", "cat"))
        return self.journals[role]

    def set_log(self, role, text):
        self.journals = {"secured": "", "connector": ""}
        self.log_path.write_text("")
        if role == "host":
            self.log_path.write_text(text)
        else:
            self.journals[role] = text

    def test_codes_absent_ignores_timestamp_in_every_log(self):
        for role in ("secured", "connector", "host"):
            with self.subTest(role=role):
                self.set_log(role, TIMESTAMP_LOG)
                with redirect_stdout(io.StringIO()):
                    ROUTE["codes_absent"](self.deployment, self.host, [CODE])

    def test_codes_absent_rejects_real_code_in_every_log_without_disclosure(self):
        for role in ("secured", "connector", "host"):
            for code in (CODE, CODE.replace("-", "")):
                with self.subTest(role=role, form="grouped" if "-" in code else "bare"):
                    self.set_log(role, "private-log-detail code=" + code)
                    output = io.StringIO()
                    with redirect_stdout(output), self.assertRaises(ValueError) as caught:
                        ROUTE["codes_absent"](self.deployment, self.host, [CODE])
                    location = "the host fixture's log" if role == "host" else role + "'s journal"
                    self.assertEqual(str(caught.exception), "a pairing code reached " + location)
                    self.assertEqual(output.getvalue(), "")

    def test_codes_absent_keeps_identifier_and_grouped_matches_in_every_log(self):
        for role in ("secured", "connector", "host"):
            for text in ("a123456f", "9123-4567"):
                with self.subTest(role=role, text=text):
                    self.set_log(role, text)
                    with redirect_stdout(io.StringIO()), self.assertRaises(ValueError):
                        ROUTE["codes_absent"](self.deployment, self.host, [CODE])

    def test_contains_pairing_code_detects_literal_and_bare_forms(self):
        texts = (
            "123456", "123-456", 'code="123456"', "code=123-456.",
            "a123456f", "prefix123456suffix", "9123-4567", "123-4567", "9123-456",
        )
        for text in texts:
            with self.subTest(text=text):
                self.assertTrue(ROUTE["contains_pairing_code"](text, [CODE]))

    def test_contains_pairing_code_ignores_only_longer_digit_runs(self):
        texts = (
            TIMESTAMP_LOG, "0123456", "1234567", "01234567", "",
            "session completed without a code", "123-457",
        )
        for text in texts:
            with self.subTest(text=text):
                self.assertFalse(ROUTE["contains_pairing_code"](text, [CODE]))

    def test_contains_pairing_code_preserves_leading_zeroes(self):
        for text in ("000042", "000-042", "a000042f", "9000-0427"):
            with self.subTest(text=text):
                self.assertTrue(ROUTE["contains_pairing_code"](text, ["000-042"]))
        self.assertFalse(ROUTE["contains_pairing_code"]("10000427", ["000-042"]))

    def test_contains_pairing_code_accepts_bare_input_without_substring_matching(self):
        self.assertTrue(ROUTE["contains_pairing_code"]("code=123456", ["123456"]))
        self.assertFalse(ROUTE["contains_pairing_code"](TIMESTAMP_LOG, ["123456"]))

    def test_contains_pairing_code_checks_all_codes_and_handles_none(self):
        codes = [CODE, "000-042"]
        self.assertTrue(ROUTE["contains_pairing_code"]("code=000042", codes))
        self.assertTrue(ROUTE["contains_pairing_code"](TIMESTAMP_LOG + " code=000-042", codes))
        self.assertFalse(ROUTE["contains_pairing_code"](TIMESTAMP_LOG, codes))
        self.assertFalse(ROUTE["contains_pairing_code"]("code=123456", []))


if __name__ == "__main__":
    unittest.main()
