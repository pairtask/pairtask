"""Image name-file validation and exact systemd EnvironmentFile encoding."""

from contextlib import ExitStack
import json
import os
from pathlib import Path
import runpy
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

    def write_name(self, value):
        self.name.write_text(json.dumps({"name": value}))

    def secured(self):
        namespace = SETUP["secured"].__globals__
        with ExitStack() as stack:
            stack.enter_context(patch.dict(namespace, {name: MagicMock() for name in
                ("check_role", "require_ids", "prepare_shared_dir", "private_parent")}))
            stack.enter_context(patch.object(SETUP["pwd"], "getpwnam",
                return_value=SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())))
            stack.enter_context(patch.object(SETUP["os"], "chown"))
            SETUP["secured"](state=self.root / "state", ingress=self.root / "ingress",
                             egress=self.root / "egress", env=self.env, name_path=self.name)

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
        self.secured()
        self.assertNotIn("PAIRTASK_PAIRING_NAME=", self.env.read_text())


if __name__ == "__main__":
    unittest.main()
