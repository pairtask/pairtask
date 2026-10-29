from pathlib import Path
import runpy
import subprocess
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SETUP = runpy.run_path(str(ROOT / "infra/image/pairtask-rtk-setup"))
OLD_MARKER = Path(".local/state/collab-ai/rtk-initialized")
PI_MARKER = Path(".local/state/collab-ai/rtk-pi-initialized")


class RTKSetupTests(unittest.TestCase):
    def test_initializes_all_agents_once_without_resetting_user_opt_out(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
                SETUP["initialize"](home)
            self.assertEqual([call.args[0] for call in run.call_args_list], [
                ["/usr/local/bin/rtk", "init", "-g", "--codex"],
                ["/usr/local/bin/rtk", "init", "-g", "--auto-patch"],
                ["/usr/local/bin/rtk", "init", "-g", "--agent", "pi"],
            ])
            for call in run.call_args_list:
                self.assertEqual(call.kwargs["env"]["HOME"], str(home))
                self.assertEqual(call.kwargs["env"]["RTK_TELEMETRY_DISABLED"], "1")
                self.assertEqual(call.kwargs["stdin"], subprocess.DEVNULL)
            for relative in (".codex", ".claude", ".pi", ".local/share/rtk"):
                self.assertEqual((home / relative).stat().st_mode & 0o777, 0o700)
            self.assertTrue((home / OLD_MARKER).exists())
            self.assertTrue((home / PI_MARKER).exists())

    def test_existing_home_with_old_marker_initializes_only_pi_once(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            marker = home / OLD_MARKER
            marker.parent.mkdir(mode=0o700, parents=True)
            marker.write_text("existing\n")
            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
                SETUP["initialize"](home)
            self.assertEqual([call.args[0] for call in run.call_args_list], [
                ["/usr/local/bin/rtk", "init", "-g", "--agent", "pi"],
            ])
            self.assertEqual((home / ".pi").stat().st_mode & 0o777, 0o700)
            self.assertEqual(marker.read_text(), "existing\n")
            self.assertTrue((home / PI_MARKER).exists())

    def test_failed_setup_can_be_retried(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            with patch.object(subprocess, "run", side_effect=subprocess.CalledProcessError(1, ["rtk"])):
                with self.assertRaises(subprocess.CalledProcessError):
                    SETUP["initialize"](home)
            self.assertFalse((home / OLD_MARKER).exists())
            self.assertFalse((home / PI_MARKER).exists())
            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
            self.assertEqual(run.call_count, 3)

    def test_failed_pi_step_preserves_old_marker_and_retries_only_pi(self):
        with tempfile.TemporaryDirectory() as directory:
            home = Path(directory)
            calls = []

            def fail_on_pi(cmd, **kwargs):
                calls.append(cmd)
                if "--agent" in cmd:
                    raise subprocess.CalledProcessError(1, cmd)

            with patch.object(subprocess, "run", side_effect=fail_on_pi):
                with self.assertRaises(subprocess.CalledProcessError):
                    SETUP["initialize"](home)
            self.assertEqual(len(calls), 3)
            self.assertTrue((home / OLD_MARKER).exists())
            self.assertFalse((home / PI_MARKER).exists())

            with patch.object(subprocess, "run") as run:
                SETUP["initialize"](home)
            self.assertEqual([call.args[0] for call in run.call_args_list], [
                ["/usr/local/bin/rtk", "init", "-g", "--agent", "pi"],
            ])
            self.assertTrue((home / PI_MARKER).exists())
