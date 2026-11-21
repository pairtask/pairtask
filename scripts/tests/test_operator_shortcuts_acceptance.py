"""Host shortcuts keep operator authority in secured and preserve literal argv."""

import contextlib
import importlib.util
import io
import json
import os
import subprocess
import tempfile
from pathlib import Path
import sys
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("shortcuts_host_cli", SCRIPTS / "pairtask.py")
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


class OperatorShortcutsAcceptance(unittest.TestCase):
    def command(self, argv):
        steps = cli.plan(argv, system="Linux")
        self.assertEqual(len(steps), 1)
        args = steps[0].argv
        start = next(i for i in range(len(args) - 1) if args[i:i + 2] == ["pairtask", "operator"])
        self.assertEqual(args[start + 1], "operator")
        self.assertNotIn("-c", args[:start])
        self.assertIn("secured", " ".join(args[:start]))
        return args[start:]

    def test_send_targets_one_agent_and_preserves_literal_text(self):
        args = self.command(["send", "@alice", "hello", "$(touch /tmp/never)", ";", "--all"])
        self.assertEqual(args, ["pairtask", "operator", "send", "--to", "alice", "--", "hello $(touch /tmp/never) ; --all"])

    def test_flag_shaped_text_is_not_an_operator_option(self):
        args = self.command(["send", "@alice", "--all"])
        self.assertEqual(args, ["pairtask", "operator", "send", "--to", "alice", "--", "--all"])

    def test_all_is_explicit_broadcast(self):
        self.assertEqual(self.command(["send", "@all", "hello everyone"]),
                         ["pairtask", "operator", "send", "--all", "--", "hello everyone"])

    def test_send_rejects_missing_or_multiple_targets_and_empty_text(self):
        for args in ([], ["alice", "text"], ["@", "text"], ["@a,@b", "text"], ["@alice"], ["@alice", ""], ["@alice", "  "]):
            with self.subTest(args=args), self.assertRaises(SystemExit):
                cli.plan(["send", *args], system="Linux")

    def test_inbox_passes_limit_and_json_without_changing_scope(self):
        self.assertEqual(self.command(["inbox", "--limit", "3", "--json"]),
                         ["pairtask", "operator", "inbox", "summary", "--limit", "3", "--json"])

    def test_target_flags_select_secured_runtime(self):
        steps = cli.plan(["send", "--remote", "lab", "--project", "team", "--secured", "vault", "@alice", "hello"], system="Linux")
        args = steps[0].argv
        self.assertIn("team", args)
        self.assertTrue("lab:vault" in args or {"lab", "vault"}.issubset(args), args)
        self.assertEqual(args[-7:], ["pairtask", "operator", "send", "--to", "alice", "--", "hello"])

    def test_send_propagates_operator_exit_status(self):
        with mock.patch.object(cli, "refuse_root", return_value=False), mock.patch.object(cli, "run", return_value=23):
            self.assertEqual(cli.main(["send", "@alice", "hello"]), 23)

    def test_dry_run_does_not_send_or_query_containers(self):
        with mock.patch.object(cli, "refuse_root", return_value=False), mock.patch.object(cli, "run") as run, \
                mock.patch.object(cli, "list_instances") as query, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "send", "@alice", "hello"]), 0)
        run.assert_not_called()
        query.assert_not_called()
        self.assertIn("alice", output.getvalue())

    def test_status_attempts_agents_and_survives_unavailable_broker(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            log = root / "incus.jsonl"
            executable = root / "incus"
            executable.write_text("#!" + sys.executable + "\n" +
                                  "import json,os,sys\n" +
                                  "with open(os.environ['SHORTCUT_CALLS'], 'a') as stream: stream.write(json.dumps(sys.argv[1:])+'\\n')\n" +
                                  "if '--format=json' in sys.argv: print(json.dumps([{'name':'secured','status':'Running'}])); sys.exit(0)\n" +
                                  "if 'list' in sys.argv: print('fixture containers'); sys.exit(0)\n" +
                                  "print('broker unavailable',file=sys.stderr); sys.exit(17)\n")
            executable.chmod(0o755)
            env = {**os.environ, "PATH": str(root) + os.pathsep + os.environ["PATH"],
                   "PAIRTASK_OPERATOR_HOME": str(root / "operator"), "SHORTCUT_CALLS": str(log)}
            result = subprocess.run([sys.executable, str(SCRIPTS / "pairtask.py"), "status", "--remote", "local"],
                                    env=env, capture_output=True, text=True, timeout=20)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertTrue(any(call[-3:] == ["pairtask", "operator", "agents"] for call in calls), calls)
            self.assertIn("fixture containers", result.stdout)
            self.assertRegex(result.stdout + result.stderr, r"(?i)unavailable|not running|could not|cannot")

    def test_help_and_reference_show_shortcuts(self):
        for name in ("send", "inbox"):
            with self.subTest(name=name):
                self.assertRegex(cli.usage(), r"(?m)^\s+" + name + r"\s+")
                reference = (SCRIPTS.parent / "docs/host-commands.md").read_text()
                self.assertIn("pairtask " + name, reference)


if __name__ == "__main__":
    unittest.main()
