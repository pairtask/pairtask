"""Pure-mock Incus upgrade acceptance; no VM or package actions."""
import sys
from pathlib import Path
sys.path.insert(0, str(Path(__file__).resolve().parent))
import ast
import contextlib
import io
import json
import subprocess
import tempfile
import unittest
from unittest.mock import patch
from incus_upgrade_fixtures import HOST, SCRIPTS, manifest_for, observation, upgrade
from incus_upgrade_runner import installs, recorded_apply
import pairtask


class UpgradeCommandTests(unittest.TestCase):
    def setUp(self):
        fixtures = (patch.object(HOST, "check_remote"), patch.object(HOST.Path, "is_socket", return_value=True),
                    patch.object(HOST.shutil, "which", return_value="/mock/tool"),
                    patch.dict(HOST.os.environ, {}, clear=True),
                    patch.object(HOST.subprocess, "run", side_effect=AssertionError("unexpected real command")))
        for fixture in fixtures:
            fixture.start()
            self.addCleanup(fixture.stop)

    def test_upgrade_scripts_remain_python39_syntax(self):
        for filename in ("sandbox_incus_upgrade.py", "sandbox-host.py", "pairtask.py"):
            with self.subTest(filename=filename):
                ast.parse((SCRIPTS / filename).read_text(), feature_version=(3, 9))

    def test_vm_commands_preserve_manifest_and_yes_flags(self):
        for action in ("incus-plan", "incus-apply"):
            with self.subTest(action=action):
                steps = pairtask.plan(["vm", action, "--manifest", "plan.json", "--yes"], system="Darwin")
                self.assertEqual(steps, [pairtask.Step("script", ["sandbox-host.py", action,
                                                                  "--manifest", "plan.json", "--yes"])])

    def test_linux_local_is_guidance_only(self):
        argv = ["sandbox-host.py", "incus-plan", "--manifest", "unused.json"]
        with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Linux"), \
                patch.object(upgrade, "plan") as plan:
            with self.assertRaisesRegex(ValueError, "Linux.*host"):
                HOST.main()
            plan.assert_not_called()

    def test_plan_explains_supported_dir_only_scope_without_apply(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            argv = ["sandbox-host.py", "incus-plan", "--manifest", str(path)]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "plan", return_value=manifest_for(observation())), \
                    patch.object(upgrade, "apply") as apply, contextlib.redirect_stdout(output):
                HOST.main()
            apply.assert_not_called()
            self.assertTrue(path.exists())
            self.assertIn("dir storage", output.getvalue())
            self.assertIn("manual upgrade", output.getvalue())

    def test_failure_reports_steps_backup_location_and_restore_guidance(self):
        backup = "/var/backups/pairtask-incus-0123456789/incus.tar.gz"
        steps = [upgrade.Step("backup readable", True, backup), upgrade.Step("install", False, "failed"),
                 upgrade.Step("restore units", False, "failed to start")]
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            path.write_text(json.dumps(manifest_for(observation())))
            argv = ["sandbox-host.py", "incus-apply", "--manifest", str(path), "--yes"]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "apply", side_effect=upgrade.UpgradeError("install failed", steps)), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                with self.assertRaises(ValueError):
                    HOST.main()
            self.assertIn(backup, output.getvalue())
            self.assertIn("FAIL", output.getvalue())
            self.assertIn("restore", output.getvalue().lower())
            self.assertIn("docs/sandbox.md", output.getvalue())

    def test_foreign_remote_is_rejected_before_plan_or_apply(self):
        for action in ("incus-plan", "incus-apply"):
            with self.subTest(action=action):
                self.assert_identity_refused(action)

    def assert_identity_refused(self, action):
        manifest = manifest_for(observation())
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            if action == "incus-apply":
                path.write_text(json.dumps(manifest))
            argv = ["sandbox-host.py", action, "--manifest", str(path), "--yes"]
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(HOST, "check_remote", side_effect=ValueError("foreign remote")), \
                    patch.object(upgrade, "plan", return_value=manifest) as plan, \
                    patch.object(upgrade, "apply", return_value=[]) as apply, contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(ValueError, "foreign remote"):
                    HOST.main()
                plan.assert_not_called()
                apply.assert_not_called()

    def test_foreign_colima_home_is_rejected_before_plan(self):
        argv = ["sandbox-host.py", "incus-plan", "--manifest", "unused.json"]
        with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                patch.dict(HOST.os.environ, {"COLIMA_HOME": "/foreign/profile"}), \
                patch.object(upgrade, "plan", return_value=manifest_for(observation())) as plan, \
                patch.object(upgrade, "write_manifest"), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(ValueError, "COLIMA_HOME"):
                HOST.main()
            plan.assert_not_called()

    def test_command_boundary_exceptions_keep_steps_backup_and_nonzero_outcome(self):
        errors = (subprocess.TimeoutExpired("apt-get", upgrade.APT), OSError("install boundary failed"), KeyboardInterrupt())
        for error in errors:
            with self.subTest(error=type(error).__name__):
                self.assert_boundary_error_reported(error)

    def assert_boundary_error_reported(self, error):
        obs = observation()
        with tempfile.TemporaryDirectory() as temp, recorded_apply(obs) as (runner, _):
            path = Path(temp) / "manifest.json"
            path.write_text(json.dumps(manifest_for(obs)))
            runner.exception = (installs, error)
            argv = ["sandbox-host.py", "incus-apply", "--manifest", str(path), "--yes"]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "Runner", return_value=runner), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                with self.assertRaises((upgrade.UpgradeError, SystemExit, subprocess.SubprocessError, OSError, KeyboardInterrupt)) as caught:
                    HOST.main()
            self.assert_structured_outcome(caught.exception, error)
            self.assertIn(upgrade.BACKUP_DIR + "/", output.getvalue())
            self.assertIn("docs/sandbox.md", output.getvalue())
            self.assertEqual(runner.holds, set(obs.holds))
            self.assertEqual(runner.units, obs.units)
            self.assertIsNone(runner.policy)

    def assert_structured_outcome(self, caught, original):
        if isinstance(original, KeyboardInterrupt):
            self.assertIsInstance(caught, SystemExit)
            self.assertEqual(caught.code, 130)
        else:
            self.assertIsInstance(caught, upgrade.UpgradeError)
            self.assertTrue(caught.steps)
            self.assertIn(type(original).__name__, str(caught))

    def test_plan_keeps_extra_manifest_metadata_in_the_private_file(self):
        manifest = manifest_for(observation())
        manifest["metadata_note"] = "not-for-the-summary"
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            argv = ["sandbox-host.py", "incus-plan", "--manifest", str(path)]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "plan", return_value=manifest), contextlib.redirect_stdout(output):
                HOST.main()
            self.assertIn("not-for-the-summary", path.read_text())
            self.assertNotIn("not-for-the-summary", output.getvalue())


if __name__ == "__main__":
    unittest.main()
