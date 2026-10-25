"""Exercise Namespace daemon startup with a stub, without starting any service."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
HELPER = ROOT / "scripts/ci-incus-start.sh"

STUB = r'''
import json
import os
from pathlib import Path
import sys
import time

args = sys.argv[1:]
with open(os.environ["TEST_CALLS"], "a") as output:
    output.write(json.dumps(args) + "\n")
marker = Path(os.environ["TEST_STARTED"])
if args[0] == "waitready":
    deadline = time.monotonic() + 2
    while not marker.exists() and time.monotonic() < deadline:
        time.sleep(0.01)
    sys.exit(int(os.environ["TEST_READY_EXIT"]))
print("fixture daemon startup diagnostic", file=sys.stderr, flush=True)
if os.environ["TEST_LAUNCH_EXIT"] == "0":
    Path(args[args.index("--logfile") + 1]).write_text("fixture daemon log\n")
marker.write_text("started")
sys.exit(int(os.environ["TEST_LAUNCH_EXIT"]))
'''


class NamespaceIncusStartupAcceptanceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="incus-start-")
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name) / "paths with spaces"
        self.directory.mkdir()
        self.wrapper = self.directory / "incusd"
        self.wrapper.write_text("#!" + sys.executable + "\n" + STUB)
        self.wrapper.chmod(0o755)
        self.log = self.directory / "incusd.log"
        self.calls = self.directory / "calls.jsonl"
        self.environment = dict(os.environ, PAIRTASK_EPHEMERAL_RUNNER="namespace",
                                TEST_CALLS=str(self.calls),
                                TEST_STARTED=str(self.directory / "started"),
                                TEST_READY_EXIT="0", TEST_LAUNCH_EXIT="0")

    def invoke(self, **changes):
        return subprocess.run(
            ["bash", "-euo", "pipefail", "-c",
             'source "$1"; start_namespace_incus "$2" "$3"',
             "acceptance", str(HELPER), str(self.wrapper), str(self.log)],
            env=dict(self.environment, **changes), capture_output=True,
            text=True, timeout=5,
        )

    def recorded_calls(self):
        if not self.calls.exists():
            return []
        return [json.loads(line) for line in self.calls.read_text().splitlines()]

    def test_explicit_daemon_arguments_and_readiness_use_the_packaged_wrapper(self):
        result = self.invoke(INCUS_OPTS="--unexpected-option")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertCountEqual(self.recorded_calls(), [
            ["--group", "incus-admin", "--logfile", str(self.log)],
            ["waitready", "--timeout=120"],
        ])
        self.assertIn("fixture daemon startup diagnostic", Path(str(self.log) + ".out").read_text())

    def test_unmarked_runner_cannot_start_daemon(self):
        for marker in ("", "github-hosted"):
            with self.subTest(marker=marker):
                result = self.invoke(PAIRTASK_EPHEMERAL_RUNNER=marker)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.recorded_calls(), [])

    def test_missing_packaged_wrapper_fails_before_readiness(self):
        self.wrapper.unlink()
        result = self.invoke()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.recorded_calls(), [])

    def test_failed_readiness_returns_failure_and_prints_startup_diagnostics(self):
        result = self.invoke(TEST_READY_EXIT="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("fixture daemon startup diagnostic", result.stderr)
        self.assertIn("fixture daemon log", result.stderr)

    def test_wrapper_failure_before_incus_log_is_preserved(self):
        result = self.invoke(TEST_LAUNCH_EXIT="1", TEST_READY_EXIT="1")
        self.assertFalse(self.log.exists())
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("fixture daemon startup diagnostic", result.stderr)
