"""Exercise smoke orchestration without root, downloads or vendor clients."""

import io
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "infra/image"))
import tool_smoke
import tool_update


class SmokeProcess:
    def __init__(self):
        self.stdin = io.BytesIO()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.stdin.close()

    def terminate(self):
        pass

    def wait(self, *_):
        return 0


class UpdateSmokeAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.etc = self.root / "etc"
        (self.etc / "codex").mkdir(parents=True)
        self.config = b"# derived configuration\n[sandbox_workspace_write]\nnetwork_access = false\n"
        (self.etc / "codex/config.toml").write_bytes(self.config)
        self.entry = self.root / "candidate"
        self.entry.write_text("fixture candidate")
        self.commands, self.owners, self.homes = [], {}, []
        self.command_check = True
        self.owner_check = False
        self.reported = "codex 2.0.0"
        self.addCleanup(mock.patch.stopall)
        mock.patch.object(tool_update.shutil, "chown", self.chown).start()
        mock.patch.object(tool_update.subprocess, "run", self.run_candidate).start()
        mock.patch.object(tool_smoke.subprocess, "Popen", self.popen).start()
        mock.patch.object(tool_smoke, "first_answer", self.answer).start()

    def chown(self, path, user=None, group=None):
        self.owners[str(path)] = user

    def run_candidate(self, command, **_):
        self.commands.append(command)
        home = next(Path(arg[5:]) for arg in command if arg.startswith("HOME="))
        self.homes.append(home)
        self.assertEqual(home.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.owners.get(str(home)), tool_update.CHECK_USER)
        return subprocess.CompletedProcess(command, 0, self.reported, "")

    def popen(self, command, **kwargs):
        self.commands.append(command)
        if self.command_check:
            self.assertEqual(command[0:3], ["runuser", "-u", tool_update.CHECK_USER])
            self.assertEqual(command[command.index("--") + 1], str(self.entry),
                             "runuser must execute the candidate, not an option after --")
        home = Path(kwargs["env"]["CODEX_HOME"])
        self.assertEqual((home / "config.toml").read_bytes(), self.config)
        if self.owner_check:
            self.assertEqual(self.owners.get(str(home)), tool_update.CHECK_USER,
                             "root-created CODEX_HOME must be writable by the dropped user")
        self.codex_home = home
        return SmokeProcess()

    def answer(self, *_):
        return {"id": 1, "result": {"userAgent": "codex 2.0.0", "codexHome": str(self.codex_home)}}

    def check(self, name="codex"):
        tool_update.smoke_check(name, {"version": "2.0.0"}, self.entry, etc=self.etc)

    def test_codex_privilege_prefix_executes_candidate(self):
        self.check()
        self.assertTrue(any("app-server" in command for command in self.commands))
        self.assertTrue(all(not home.exists() for home in self.homes))

    def test_codex_private_config_directory_is_writable_by_check_user(self):
        self.command_check, self.owner_check = False, True
        self.check()

    def test_claude_checks_help_as_well_as_version(self):
        self.check("claude")
        self.assertTrue(any("--help" in command for command in self.commands),
                        "a version print alone does not exercise Claude's CLI")

    def test_version_prefix_is_not_an_exact_version_and_home_is_removed(self):
        self.reported = "codex 12.0.0"
        with self.assertRaises(tool_update.UpdateError) as error:
            self.check()
        self.assertEqual(error.exception.code, "smoke")
        self.assertTrue(all(not home.exists() for home in self.homes))

    def test_each_smoke_has_a_distinct_private_home(self):
        self.check("cs-mcp")
        self.check("cs-mcp")
        self.assertEqual(len(set(self.homes)), 2)
        self.assertTrue(all(not home.exists() for home in self.homes))
