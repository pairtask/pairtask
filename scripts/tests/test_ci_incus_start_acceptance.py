"""Exercise Namespace setup without starting services or creating device nodes."""

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


MKNOD_STUB = r'''
import json
import os
from pathlib import Path
import sys

args = sys.argv[1:]
with open(os.environ["TEST_CALLS"], "a") as output:
    output.write(json.dumps(args) + "\n")
target = Path(args[2])
if target.name == os.environ.get("TEST_REFUSE_NODE"):
    print("fixture: operation not permitted", file=sys.stderr)
    sys.exit(1)
target.write_text("fixture node")
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
        return self.invoke_command('start_namespace_incus "$2" "$3"',
                                   self.wrapper, self.log, **changes)

    def invoke_command(self, command, *arguments, **changes):
        return subprocess.run(
            ["bash", "-euo", "pipefail", "-c", 'source "$1"; ' + command,
             "acceptance", str(HELPER), *map(str, arguments)],
            env=dict(self.environment, **changes), capture_output=True,
            text=True, timeout=5,
        )

    def loop_fixture(self):
        devices = self.directory / "dev"
        devices.mkdir()
        mknod = self.directory / "mknod"
        mknod.write_text("#!" + sys.executable + "\n" + MKNOD_STUB)
        mknod.chmod(0o755)
        self.environment["PATH"] = str(self.directory) + os.pathsep + os.environ["PATH"]
        return devices

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

    def test_missing_loop_nodes_use_exact_device_numbers(self):
        devices = self.loop_fixture()
        result = self.invoke_command('ensure_loop_devices "$2"', devices)
        self.assertEqual(result.returncode, 0, result.stderr)
        expected = [["-m", "0660", str(devices / "loop-control"), "c", "10", "237"]]
        expected += [["-m", "0660", str(devices / ("loop" + str(i))), "b", "7", str(i)]
                     for i in range(8)]
        self.assertEqual(self.recorded_calls(), expected)

    def test_existing_loop_paths_are_preserved(self):
        devices = self.loop_fixture()
        for name in ("loop-control", "loop0", "loop4"):
            target = devices / name
            target.write_text("original")
            target.chmod(0o640)
        result = self.invoke_command('ensure_loop_devices "$2"', devices)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual([Path(call[2]).name for call in self.recorded_calls()],
                         ["loop1", "loop2", "loop3", "loop5", "loop6", "loop7"])
        for name in ("loop-control", "loop0", "loop4"):
            self.assertEqual((devices / name).read_text(), "original")
            self.assertEqual((devices / name).stat().st_mode & 0o777, 0o640)

    def test_refused_loop_creation_stops_with_diagnostic(self):
        devices = self.loop_fixture()
        for refused in ("loop-control", "loop3"):
            with self.subTest(refused=refused):
                result = self.invoke_command('ensure_loop_devices "$2"', devices,
                                             TEST_REFUSE_NODE=refused)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("runner forbids creating loop devices", result.stderr)
                self.assertEqual(Path(self.recorded_calls()[-1][2]).name, refused)
                self.assertFalse((devices / refused).exists())
                self.assertFalse((devices / "loop4").exists())
