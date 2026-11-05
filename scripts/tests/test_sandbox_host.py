import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
SPEC = importlib.util.spec_from_file_location(
    "sandbox_host", Path(__file__).resolve().parents[1] / "sandbox-host.py"
)
host = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(host)


class HostTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name)
        self.profile = self.home / ".colima" / host.PROFILE
        for target, value in (("platform.system", "Darwin"), ("platform.machine", "arm64")):
            mock = patch.object(getattr(host, target.split(".")[0]), target.split(".")[1], return_value=value)
            mock.start()
            self.addCleanup(mock.stop)
        mock = patch.object(host.Path, "home", return_value=self.home)
        mock.start()
        self.addCleanup(mock.stop)
        mock = patch.dict(host.os.environ, {}, clear=True)
        mock.start()
        self.addCleanup(mock.stop)

    def run_action(self, action):
        with patch.object(host.sys, "argv", ["sandbox-host.py", action]), contextlib.redirect_stdout(io.StringIO()):
            host.main()

    def test_plan_does_not_create_profile_or_invoke_commands(self):
        with patch.object(host.subprocess, "run") as run:
            self.run_action("plan")
        run.assert_not_called()
        self.assertFalse(self.profile.exists())

    def test_apply_is_repeatable_and_uses_explicit_endpoint(self):
        remote = {host.REMOTE: {"Addrs": [f"unix://{self.profile}/incus.sock"], "Protocol": "incus"}}
        server = {"environment": {"server_version": "7.5"}}

        def answer(command, **_):
            reply = server if command[:2] == ["incus", "query"] else remote
            return subprocess.CompletedProcess(command, 0, json.dumps(reply))
        with patch.object(host.shutil, "which", return_value="/bin/tool"), \
                patch.object(host.Path, "is_socket", return_value=True), \
                patch.object(host.subprocess, "run", side_effect=answer) as run:
            self.run_action("apply")
            original = (self.profile / "colima.yaml").read_bytes()
            self.run_action("apply")
        self.assertEqual(original, (self.profile / "colima.yaml").read_bytes())
        config = json.loads(original)
        self.assertIsNone(config["mounts"])
        self.assertFalse(config["forwardAgent"])
        self.assertEqual(config["runtime"], "incus")
        for call in (call for call in run.call_args_list if call.args[0][0] == "colima"):
            command = call.args[0]
            self.assertEqual(command[:3], ["colima", "start", "pairtask"])
            self.assertIn("--save-config=false", command)
            self.assertIn("--activate=false", command)
            self.assertEqual(command[-2:], ["--mount", "none"])
        queries = [call.args[0] for call in run.call_args_list if call.args[0][:2] == ["incus", "query"]]
        self.assertEqual(queries, [["incus", "query", "colima-pairtask:/1.0"]] * 2)

    def test_foreign_remote_is_rejected_before_creating_profile(self):
        remote = {host.REMOTE: {"Addrs": ["https://another-server:8443"], "Protocol": "incus"}}
        with patch.object(host.shutil, "which", return_value="/bin/tool"), \
                patch.object(host.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, json.dumps(remote))) as run, \
                self.assertRaisesRegex(ValueError, "does not point"):
            self.run_action("apply")
        self.assertFalse(self.profile.exists())
        self.assertEqual(run.call_count, 1)

    def test_foreign_profile_and_drift_are_not_overwritten(self):
        self.profile.mkdir(parents=True)
        settings = self.profile / "colima.yaml"
        settings.write_text("runtime: docker\n")
        with self.assertRaisesRegex(ValueError, "not managed"):
            self.run_action("apply")
        self.assertEqual(settings.read_text(), "runtime: docker\n")
        config = host.desired_config()
        (self.profile / "pairtask-owner.json").write_text(json.dumps({"schema": 1, "config": config}))
        settings.write_text(json.dumps({**config, "forwardAgent": True}))
        with self.assertRaisesRegex(ValueError, "drifted"):
            self.run_action("apply")
        self.assertTrue(json.loads(settings.read_text())["forwardAgent"])

    def test_symlink_profile_is_not_followed(self):
        self.profile.parent.mkdir()
        elsewhere = self.home / "elsewhere"
        elsewhere.mkdir()
        self.profile.symlink_to(elsewhere, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "symlinked"):
            self.run_action("apply")
        self.assertEqual(list(elsewhere.iterdir()), [])

    def test_missing_dependency_does_not_write_configuration(self):
        with patch.object(host.shutil, "which", return_value=None), self.assertRaisesRegex(ValueError, "Install"):
            self.run_action("apply")
        self.assertFalse(self.profile.exists())

    def test_start_failure_preserves_configuration_for_retry(self):
        with patch.object(host.shutil, "which", return_value="/bin/tool"), \
                patch.object(host.subprocess, "run", side_effect=[
                    subprocess.CompletedProcess([], 0, "{}"), subprocess.CalledProcessError(1, "colima")]), \
                self.assertRaises(subprocess.CalledProcessError):
            self.run_action("apply")
        host.check_profile(self.profile, host.desired_config())


if __name__ == "__main__":
    unittest.main()
