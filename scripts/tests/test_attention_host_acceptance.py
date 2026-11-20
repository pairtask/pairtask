"""Attention controls stay in the secured operator path and preserve literal text."""

import contextlib
import importlib.util
import io
from pathlib import Path
import sys
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("attention_host_cli", SCRIPTS / "pairtask.py")
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


class AttentionHostAcceptance(unittest.TestCase):
    def operator_words(self, arguments):
        steps = cli.plan(arguments, system="Linux")
        self.assertEqual(len(steps), 1)
        argv = steps[0].argv
        at = next(i for i in range(len(argv) - 1) if argv[i:i + 2] == ["pairtask", "operator"])
        self.assertNotIn("-c", argv[:at])
        self.assertIn("local:secured", argv[:at])
        return argv[at + 2:]

    def test_dnd_on_off_and_status_use_one_local_agent(self):
        for action in ("on", "off", None):
            with self.subTest(action=action):
                args = ["dnd", "@alice"] + ([action] if action else [])
                words = self.operator_words(args)
                self.assertEqual(words, ["dnd", "--agent", "alice"] + ([action] if action else []))

    def test_dnd_invalid_targets_and_actions_are_refused(self):
        for args in ([], ["alice"], ["@all", "on"], ["@operator", "on"],
                     ["@a@remote", "on"], ["@a,@b", "on"], ["@a", "@b", "on"],
                     ["@a", "later"], ["@a", "on", "10m"]):
            with self.subTest(args=args), self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
                cli.plan(["dnd", *args], system="Linux")

    def test_urgent_flag_before_target_is_an_operator_option(self):
        words = self.operator_words(["send", "--urgent", "@alice", "literal", "$(never)", ";"])
        self.assertEqual(words[0], "send")
        boundary = words.index("--")
        self.assertIn("--urgent", words[:boundary])
        self.assertEqual(words[words.index("--to") + 1], "alice")
        self.assertEqual(words[boundary + 1:], ["literal $(never) ;"])

    def test_urgent_after_target_remains_literal_text(self):
        self.assertEqual(self.operator_words(["send", "@alice", "--urgent"]),
                         ["send", "--to", "alice", "--", "--urgent"])

    def test_dnd_target_flags_and_json_preserve_secured_selection(self):
        args = cli.plan(["dnd", "--remote", "lab", "--project", "team", "--secured", "vault", "--json", "@alice"], system="Linux")[0].argv
        self.assertIn("lab:vault", args)
        self.assertEqual(args[args.index("--project") + 1], "team")
        self.assertIn("--json", args)
        self.assertEqual(args[args.index("--agent") + 1], "alice")

    def test_dry_run_never_sets_dnd_or_sends_urgently(self):
        for args in (["dnd", "@alice", "on"], ["send", "--urgent", "@alice", "hello"]):
            with self.subTest(args=args), mock.patch.object(cli, "refuse_root", return_value=False), \
                    mock.patch.object(cli, "run") as run, mock.patch.object(cli, "list_instances") as query, \
                    contextlib.redirect_stdout(io.StringIO()):
                self.assertEqual(cli.main(["--dry-run", *args]), 0)
                run.assert_not_called()
                query.assert_not_called()

    def test_dnd_propagates_operator_failure(self):
        with mock.patch.object(cli, "refuse_root", return_value=False), mock.patch.object(cli, "run", return_value=23):
            self.assertEqual(cli.main(["dnd", "@alice", "on"]), 23)

    def test_help_and_reference_make_controls_findable(self):
        self.assertRegex(cli.usage(), r"(?m)^\s+dnd\s+")
        reference = (SCRIPTS.parent / "docs/host-commands.md").read_text()
        self.assertIn("pairtask dnd", reference)
        self.assertIn("--urgent", reference)


if __name__ == "__main__":
    unittest.main()
