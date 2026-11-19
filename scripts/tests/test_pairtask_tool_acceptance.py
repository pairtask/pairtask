"""Exercise the real launcher through canonical, legacy and native tool names."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class ToolAliasAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        source = ROOT / "infra/image/pairtask-tool"
        if not source.exists():
            source = ROOT / "infra/image/collab-tool"
        self.launcher = self.root / "pairtask-tool"
        self.launcher.write_bytes(source.read_bytes())
        (self.root / "collab-tool").symlink_to("pairtask-tool")
        (self.root / "go").symlink_to("pairtask-tool")
        self.entry = self.root / "generation" / "go" / "bin" / "go"
        self.entry.parent.mkdir(parents=True)
        self.entry.write_text("#!" + sys.executable + "\nimport json, os, sys\n"
                              "print(json.dumps({'args':sys.argv[1:], 'goroot':os.environ.get('GOROOT')}))\n")
        self.entry.chmod(0o755)
        # Preload only the store dependency; runpy executes the unmodified launcher.
        (self.root / "tool_store.py").write_text(
            "import os\nfrom pathlib import Path\nfrom contextlib import contextmanager\n"
            "held = False\n"
            "@contextmanager\ndef launch_lock():\n"
            " global held\n held = True\n"
            " try: yield\n"
            " finally: held = False\n"
            "class StoreError(ValueError): pass\n"
            "def ready():\n"
            " assert held, 'readiness must run under launch_lock'\n"
            " return os.environ.get('PAIRTASK_TEST_READY_ERROR') or None\n"
            "class Store:\n"
            " def resolve_name(self, name):\n"
            "  assert held, 'entry selection must run under launch_lock'\n"
            "  return Path(os.environ['PAIRTASK_TEST_ENTRY']) if name == 'go' else None\n")

    def invoke(self, name, args, blocked=False):
        program = ("import runpy, sys, tool_store\n"
                   "sys.argv = sys.argv[1:]\n"
                   "runpy.run_path(sys.argv[0], run_name='__main__')\n")
        environment = dict(os.environ, PYTHONPATH=str(self.root), PAIRTASK_TEST_ENTRY=str(self.entry),
                           PAIRTASK_TEST_READY_ERROR="maintenance gate closed" if blocked else "")
        return subprocess.run([sys.executable, "-c", program, str(self.root / name), *args],
                              cwd=self.root, env=environment, capture_output=True, text=True)

    def test_direct_and_symlink_names_preserve_arguments_and_generation(self):
        native_args = ["version", "", "argument with spaces", "--", "literal;$value"]
        for name in ("pairtask-tool", "collab-tool", "go"):
            with self.subTest(name=name):
                args = native_args if name == "go" else ["go", *native_args]
                result = self.invoke(name, args)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(json.loads(result.stdout), {"args": native_args, "goroot": str(self.entry.parent.parent)})

    def test_all_entry_names_obey_the_same_maintenance_gate(self):
        for name in ("pairtask-tool", "collab-tool", "go"):
            with self.subTest(name=name):
                result = self.invoke(name, ["version"] if name == "go" else ["go", "version"], blocked=True)
                self.assertEqual(result.returncode, 75, result.stderr)
                self.assertEqual(result.stdout, "")
                self.assertIn("maintenance gate closed", result.stderr)

    def test_direct_aliases_reject_missing_tools_equally(self):
        for name in ("pairtask-tool", "collab-tool"):
            with self.subTest(name=name):
                result = self.invoke(name, ["not-installed", "--version"])
                self.assertEqual(result.returncode, 127, result.stderr)
                self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
