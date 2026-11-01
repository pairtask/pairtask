"""Host-side persistence and finite Go/Python pairing-name edge vectors."""

from contextlib import redirect_stderr
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_pairing as pairing
from test_pairing_command_acceptance import load_script

VECTORS = json.loads((SCRIPTS.parent / "testdata/pairing_name_vectors.json").read_text())


class PairingNameHostAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.path = self.root / "operator.auto.tfvars.json"

    def test_shared_validation_vectors_and_invalid_types(self):
        for case in VECTORS:
            with self.subTest(case=case["id"]):
                if case["valid"]:
                    self.assertEqual(pairing.valid_name(case["name"]), case["name"])
                else:
                    with self.assertRaises(ValueError):
                        pairing.valid_name(case["name"])
        for value in (None, 42, False, [], {}, "\ud800"):
            with self.subTest(value=repr(value)), self.assertRaises(ValueError):
                pairing.valid_name(value)

    def test_omitted_supplied_and_empty_names_persist_across_on_off(self):
        original = {"project_name": "proof", "nested": {"keep": [1, 2]}}
        self.path.write_text(json.dumps(original))
        first = pairing.pairing_change(self.root, True)
        self.assertNotIn("pairing_name", first.variables)
        self.assertEqual(json.loads(self.path.read_text()), original)
        for enabled, name, expected in ((True, " My peer ", " My peer "), (False, None, " My peer "),
                                        (True, None, " My peer "), (True, "", ""), (False, None, ""), (True, None, "")):
            change = pairing.pairing_change(self.root, enabled, name=name)
            pairing.write_change(change)
            saved = json.loads(self.path.read_text())
            self.assertEqual(saved["pairing_name"], expected)
            self.assertEqual(saved["pairing_route"], enabled)
            self.assertEqual(saved["nested"], original["nested"])

    def test_invalid_supplied_or_saved_name_never_mutates_files(self):
        for value in ("x" * 65, "界" * 22, "bad\nname", "bad\u00a0name", 42, False, 0, [], {}):
            with self.subTest(value=repr(value)):
                self.path.write_text(json.dumps({"pairing_name": "Kept", "pairing_route": False}))
                before = self.path.read_bytes()
                with self.assertRaises(ValueError):
                    pairing.pairing_change(self.root, True, name=value)
                self.assertEqual(self.path.read_bytes(), before)
                self.path.write_text(json.dumps({"pairing_name": value}))
                before = self.path.read_bytes()
                with self.assertRaises(ValueError):
                    pairing.pairing_change(self.root, True)
                self.assertEqual(self.path.read_bytes(), before)

    def test_name_control_uses_one_owner_and_refuses_hcl_or_split_owners(self):
        custom = self.root / "custom.auto.tfvars.json"
        custom.write_text(json.dumps({"pairing_name": "Saved"}))
        self.assertEqual(pairing.pairing_change(self.root, True).path, custom)
        self.path.write_text(json.dumps({"pairing_route": False}))
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True, name="New")
        self.path.unlink()
        custom.unlink()
        (self.root / "terraform.tfvars").write_text('pairing_name = "HCL name"')
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True, name="New")

    def test_falsey_nonstring_saved_names_are_rejected(self):
        for value in (None, False, 0, [], {}):
            with self.subTest(value=repr(value)):
                self.path.write_text(json.dumps({"pairing_name": value}))
                before = self.path.read_bytes()
                with self.assertRaises(ValueError):
                    pairing.pairing_change(self.root, True)
                self.assertEqual(self.path.read_bytes(), before)

    def test_off_rejects_a_supplied_name(self):
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, False, name="New")
        self.assertEqual(list(self.root.iterdir()), [])

    def test_cli_preserves_absent_empty_and_supplied_name(self):
        provision = load_script("sandbox-provision")
        for flags, expected in (([], None), (["--name", ""], ""), (["--name", " My peer "], " My peer ")):
            with patch.object(sys, "argv", ["sandbox-provision", "pairing-on", *flags]):
                self.assertEqual(provision.arguments().name, expected)
        for action in ("pairing-off", "rollout", "pairing-status"):
            with patch.object(sys, "argv", ["sandbox-provision", action, "--name", "New"]):
                with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                    provision.arguments()


if __name__ == "__main__":
    unittest.main()
