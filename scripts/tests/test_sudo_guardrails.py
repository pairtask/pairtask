"""Host commands reject sudo/root before touching a user's Pairtask state."""

import contextlib
import importlib.util
import io
import os
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))


def load(name):
    spec = importlib.util.spec_from_file_location(name.replace("-", "_"), SCRIPTS / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class HostUserGuardrailTests(unittest.TestCase):
    def run_script(self, name, *args, sudo_user="operator", **overrides):
        env = dict(os.environ, SUDO_USER=sudo_user, **overrides)
        return subprocess.run([sys.executable, str(SCRIPTS / (name + ".py")), *args],
                              env=env, capture_output=True, text=True, timeout=10)

    def assert_refused(self, result, command):
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertEqual(result.stdout, "")
        self.assertIn(command, result.stderr)
        self.assertIn("normal user", result.stderr)
        self.assertIn("sudo", result.stderr)

    def test_sudo_rejects_commands_and_dry_run_before_planning(self):
        for args in (("status",), ("--dry-run", "status"), ("pairing", "status")):
            with self.subTest(args=args):
                self.assert_refused(self.run_script("pairtask", *args), "pairtask")

    def test_root_rejects_command_before_planning(self):
        cli = load("pairtask")
        with mock.patch("os.geteuid", return_value=0), \
                mock.patch.object(cli, "plan", side_effect=AssertionError("planning reached")), \
                contextlib.redirect_stderr(io.StringIO()) as error, \
                contextlib.redirect_stdout(io.StringIO()) as output:
            code = cli.main(["status"])
        self.assertEqual(code, 1)
        self.assertEqual(output.getvalue(), "")
        self.assertIn("normal user", error.getvalue())

    def test_top_level_help_stays_available_under_sudo(self):
        result = self.run_script("pairtask", "--help")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("usage: pairtask", result.stdout)

    def test_direct_provision_rejects_sudo_before_parsing_or_state_access(self):
        self.assert_refused(self.run_script("sandbox-provision", "plan"), "sandbox-provision")

    def test_ci_opt_in_is_exact_and_allows_sudo_dry_run(self):
        for value in ("", "0", "true"):
            with self.subTest(value=value):
                self.assert_refused(self.run_script("pairtask", "--dry-run", "status",
                                                    PAIRTASK_ALLOW_ROOT=value), "pairtask")
        allowed = self.run_script("pairtask", "--dry-run", "status", PAIRTASK_ALLOW_ROOT="1")
        self.assertEqual(allowed.returncode, 0, allowed.stderr)
        self.assertIn("incus", allowed.stdout)
        self.assertEqual(allowed.stderr, "")


class DiscoveryDiagnosticTests(unittest.TestCase):
    def test_service_manager_keeps_actionable_error_not_root_hint(self):
        import sandbox_discovery_service as service

        manager = service.Manager()
        stderr = ("Bootstrap failed: 5: Input/output error\n"
                  "Try re-running the command as root for richer errors.\n")
        completed = subprocess.CompletedProcess(["launchctl", "bootstrap"], 5, "", stderr)
        with mock.patch.object(service.subprocess, "run", return_value=completed):
            ok, detail = manager.execute(["launchctl", "bootstrap"])
        self.assertFalse(ok)
        self.assertIn("Bootstrap failed: 5", detail)
        self.assertNotIn("Try re-running", detail)

    def test_discovery_lease_shows_find_and_invitation(self):
        discovery = load("sandbox-discovery")
        lease = {"advertise": True, "browse": True, "find": True,
                 "invitation": "Home Mac", "ports": [7445], "expires_at": "later"}
        line = discovery.lease_line(lease)
        self.assertIn("find yes", line)
        self.assertIn("Home Mac", line)
        self.assertIn("7445", line)
        lease.update(find=False, invitation=None)
        self.assertIn("find no", discovery.lease_line(lease))


if __name__ == "__main__":
    unittest.main()
