"""Host CLI acceptance: routing, endpoint selection, and side-effect boundaries."""

import ast
import contextlib
import importlib.util
import io
import json
import os
import re
import runpy
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
spec = importlib.util.spec_from_file_location("host_cli", SCRIPTS / "pairtask.py")
cli = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cli)


def parser_call(node):
    """Return the parser method and its literal first argument, if present."""
    if not isinstance(node, ast.Call) or not isinstance(node.func, ast.Attribute):
        return None, None
    if not node.args or not isinstance(node.args[0], ast.Constant):
        return None, None
    return node.func.attr, node.args[0].value


def argument_choices(node):
    for keyword in node.keywords:
        if keyword.arg == "choices":
            return set(ast.literal_eval(keyword.value))
    return set()


def declared_actions(path):
    actions = set()
    for node in ast.walk(ast.parse(path.read_text())):
        method, argument = parser_call(node)
        if method == "add_parser":
            actions.add(argument)
        if (method, argument) == ("add_argument", "action"):
            actions.update(argument_choices(node))
    return actions


def routed_actions():
    routed = {}
    for script, leading, _description in cli.SCRIPTED.values():
        if leading:
            routed.setdefault(script, set()).add(leading[0])
    for script, actions, _description in cli.ACTIONS.values():
        routed.setdefault(script, set()).update(actions.values())
    return routed


class HostRoutingTests(unittest.TestCase):
    def test_short_shell_alias_preserves_arguments_and_dry_run_boundary(self):
        flags = ["--remote", "local", "--", "printf", "%s", "a b;$(literal)", "--dry-run"]
        for system in ("Linux", "Darwin"):
            with self.subTest(system=system):
                self.assertEqual(cli.plan(["sh", *flags], system=system),
                                 cli.plan(["shell", *flags], system=system))
        with mock.patch.object(cli, "run", return_value=0) as run:
            self.assertEqual(cli.main(["sh", "printf", "--dry-run", "a b"]), 0)
        self.assertEqual(run.call_args.args[0].argv,
                         ["sandbox-session.py", "shell", "printf", "--dry-run", "a b"])
        with mock.patch.object(cli, "run") as run, contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "sh", "printf", "a b"]), 0)
        run.assert_not_called()
        self.assertIn("shell", output.getvalue())

    def test_shell_alias_is_discoverable_in_help(self):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--help"]), 0)
        self.assertRegex(output.getvalue(), r"(?m)^\s+sh\s+.*shell")

    def test_help_mentions_optional_pk_command_alias(self):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--help"]), 0)
        self.assertRegex(output.getvalue(), r"\bpk\b")

    def test_executable_entrypoint_help_dry_run_and_unknown_command(self):
        cases = [( ["--help"], 0, "usage: pairtask"),
                 (["--dry-run", "start"], 0, "start"),
                 (["no-such-command"], 2, "usage: pairtask")]
        for argv, code, expected in cases:
            with self.subTest(argv=argv):
                result = subprocess.run(
                    [sys.executable, str(SCRIPTS / "pairtask.py"), *argv],
                    env=dict(os.environ, PATH=""),
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, code, result.stderr)
                self.assertIn(expected, result.stdout + result.stderr)

    def test_all_legacy_actions_have_a_host_route(self):
        routes = {
            ("upgrade",): ("sandbox-provision.py", "rollout"),
            ("plan",): ("sandbox-provision.py", "plan"),
            ("apply",): ("sandbox-provision.py", "apply"),
            ("recover",): ("sandbox-provision.py", "recover"),
            ("tools",): ("sandbox-provision.py", "tools-retry"),
            ("ssh",): ("sandbox-ssh.py",),
            ("download",): ("sandbox-download.py",),
            ("shell",): ("sandbox-session.py", "shell"),
            ("chat",): ("sandbox-session.py", "chat"),
            ("vm", "plan"): ("sandbox-host.py", "plan"),
            ("vm", "apply"): ("sandbox-host.py", "apply"),
            ("mounts", "plan"): ("sandbox-host.py", "mounts-plan"),
            ("mounts", "apply"): ("sandbox-host.py", "mounts-apply"),
            ("share", "plan"): ("sandbox-share.py", "plan"),
            ("share", "setup"): ("sandbox-share.py", "setup"),
            ("share", "run"): ("sandbox-share.py", "run"),
        }
        flags = ["--example", "a directory/$(literal);file", "--", "-n"]
        for command, expected in routes.items():
            with self.subTest(command=command):
                passed = ["--remote", "local", *flags] if command == ("ssh",) else flags
                steps = cli.plan([*command, *passed], system="Linux")
                self.assertEqual(len(steps), 1)
                self.assertEqual(steps[0].kind, "script")
                self.assertEqual(steps[0].argv, [*expected, *passed])

    def test_ssh_defaults_to_host_os_and_preserves_explicit_remote(self):
        main = runpy.run_path(str(SCRIPTS / "sandbox-ssh.py"))["main"]
        for system, remote in (("Linux", "local"), ("Darwin", "colima-pairtask")):
            for flags, wanted in (([], remote), (["--remote", "custom"], "custom"),
                                  (["--remote=custom"], "custom")):
                with self.subTest(system=system, flags=flags), \
                        mock.patch.object(cli.platform, "system", return_value=system), \
                        mock.patch("sys.argv", ["sandbox-ssh.py", *flags]):
                    self.assertEqual(cli.plan(["ssh", *flags], system=system)[0].argv,
                                     ["sandbox-ssh.py", *flags])
                    setup = mock.Mock()
                    with mock.patch.dict(main.__globals__, {"setup": setup, "os": mock.Mock()}):
                        main()
                    self.assertEqual(setup.call_args.args[0].remote, wanted)

    def test_script_executes_as_main_with_literal_argv(self):
        original = sys.argv
        arguments = ["shell", "printf", "%s", "a path;$(literal)"]
        seen = []
        def execute(path, run_name):
            seen.append((path, run_name, list(sys.argv)))
        try:
            with mock.patch.object(cli.runpy, "run_path", side_effect=execute):
                cli.run(cli.script("sandbox-session.py", *arguments))
            script = str(SCRIPTS / "sandbox-session.py")
            self.assertEqual(seen, [(script, "__main__", [script, *arguments])])
        finally:
            sys.argv = original

    def test_failure_stops_install_before_later_steps(self):
        with mock.patch.object(cli.platform, "system", return_value="Darwin"), \
                mock.patch.object(cli, "run", return_value=23) as run:
            self.assertEqual(cli.main(["install"]), 23)
        self.assertEqual(run.call_count, 1)

    def test_remote_shell_dry_run_flag_is_not_consumed_by_host(self):
        arguments = ["shell", "my-command", "--dry-run", "a b"]
        with mock.patch.object(cli, "run", return_value=0) as run, \
                contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(cli.main(arguments), 0)
        run.assert_called_once()
        self.assertEqual(run.call_args.args[0].argv,
                         ["sandbox-session.py", *arguments])

    def test_install_status_uses_same_endpoint_as_rollout(self):
        for flags in (["--remote", "local", "--project", "team-two"],
                      ["--remote=local", "--project=team-two"]):
            with self.subTest(flags=flags):
                steps = cli.plan(["install", *flags], system="Linux")
                status = steps[-1].argv
                self.assertEqual(status[status.index("--project") + 1], "team-two")
                self.assertIn("local:", status)

    def test_composite_help_and_invalid_flags_never_run_a_step(self):
        cases = [["install", "--help"], ["install", "--not-an-option"],
                 ["install", "--release"], ["start", "--not-an-option"],
                 ["stop", "--project"], ["status", "--remote"],
                 ["stop", "--remote", "--project", "another"],
                 ["stop", "unexpected-positional"], ["stop", "--project=bad:name"],
                 ["start", "--remote="]]
        for argv in cases:
            with self.subTest(argv=argv), \
                    mock.patch.object(cli.platform, "system", return_value="Darwin"), \
                    mock.patch.object(cli, "run", return_value=0) as run, \
                    mock.patch.object(cli, "list_instances", return_value={"workspace", "secured"}) as query, \
                    contextlib.redirect_stdout(io.StringIO()), \
                    contextlib.redirect_stderr(io.StringIO()):
                try:
                    cli.main(argv)
                except SystemExit:
                    pass
                run.assert_not_called()
                query.assert_not_called()

    def test_dry_run_does_not_query_or_execute(self):
        with mock.patch.object(cli, "run") as run, \
                mock.patch.object(cli, "list_instances") as query, \
                contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "start"]), 0)
        run.assert_not_called()
        query.assert_not_called()
        self.assertIn("start", output.getvalue())

    def test_mac_start_brings_up_host_before_querying_containers(self):
        events = []
        execute = cli.run
        def run(step):
            events.append(("run", step))
            return 0 if step.kind == "script" else execute(step)
        def query(*args):
            events.append(("query", args))
            return {"workspace", "secured"}
        with mock.patch.object(cli.platform, "system", return_value="Darwin"), \
                mock.patch.object(cli, "run", side_effect=run), \
                mock.patch.object(cli.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)), \
                mock.patch.object(cli, "list_instances", side_effect=query):
            self.assertEqual(cli.main(["start"]), 0)
        self.assertEqual(events[0][0], "run")
        self.assertEqual(events[0][1].argv, ["sandbox-host.py", "apply"])
        self.assertTrue(any(kind == "query" for kind, _ in events))

    def test_start_and_stop_order_respects_secured_dependency(self):
        for action, wanted in (("start", ["local:secured", "local:workspace"]),
                               ("stop", ["local:workspace", "local:secured"])):
            with self.subTest(action=action), \
                    mock.patch.object(cli.platform, "system", return_value="Linux"), \
                    mock.patch.object(cli.subprocess, "run", return_value=subprocess.CompletedProcess([], 0)) as run, \
                    mock.patch.object(cli, "list_instances", return_value={"workspace", "secured"}):
                self.assertEqual(cli.main([action, "--remote", "local", "--project", "other"]), 0)
                commands = [call.args[0] for call in run.call_args_list
                            if call.args[0][0] == "incus"]
                actual = [arg for command in commands for arg in command if arg in wanted]
                self.assertEqual(actual, wanted)
                for command in commands:
                    self.assertEqual(command[command.index("--project") + 1], "other")

    def test_start_stop_skip_containers_already_in_requested_state(self):
        cases = [("start", {"secured": "Running", "workspace": "Stopped"}, ["workspace"]),
                 ("stop", {"secured": "Running", "workspace": "Stopped"}, ["secured"]),
                 ("start", {"secured": "Running", "workspace": "Running"}, []),
                 ("stop", {"secured": "Stopped", "workspace": "Stopped"}, [])]
        for action, statuses, expected in cases:
            mutations = []
            def incus(argv, **kwargs):
                if "--format=json" in argv:
                    payload = [{"name": name, "status": status} for name, status in statuses.items()]
                    return subprocess.CompletedProcess(argv, 0, stdout=json.dumps(payload))
                target = argv[-1].split(":")[-1]
                mutations.append(target)
                desired = "Running" if action == "start" else "Stopped"
                return subprocess.CompletedProcess(argv, int(statuses[target] == desired))
            with self.subTest(action=action, statuses=statuses), \
                    mock.patch.object(cli.platform, "system", return_value="Linux"), \
                    mock.patch.object(cli.subprocess, "run", side_effect=incus):
                self.assertEqual(cli.main([action]), 0)
                self.assertEqual(mutations, expected)


class LegacyCompatibilityTests(unittest.TestCase):
    def test_every_legacy_parser_action_has_a_dispatch_route(self):
        routed = routed_actions()
        for name in ("sandbox-provision.py", "sandbox-host.py", "sandbox-share.py", "sandbox-session.py"):
            actions = declared_actions(SCRIPTS / name)
            with self.subTest(script=name):
                self.assertTrue(actions, "expected the legacy parser's declared actions")
                self.assertLessEqual(actions, routed.get(name, set()))

    def test_legacy_entrypoints_still_offer_help_without_host_tools(self):
        commands = [("sandbox-provision.py",), ("sandbox-host.py",),
                    ("sandbox-share.py",), ("sandbox-ssh.py",),
                    ("sandbox-download.py",), ("sandbox-session.py", "shell"),
                    ("sandbox-session.py", "chat")]
        for command in commands:
            with self.subTest(command=command):
                result = subprocess.run(
                    [sys.executable, str(SCRIPTS / command[0]), *command[1:], "--help"],
                    capture_output=True, text=True, timeout=10,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("usage:", result.stdout)


class HostCommandReferenceTests(unittest.TestCase):
    def test_every_exposed_command_has_an_executable_reference_example(self):
        reference = (SCRIPTS.parent / "docs/host-commands.md").read_text()
        examples = re.findall(r"```[^\n]*\n(.*?)```|`([^`\n]+)`", reference, re.S)
        code = "\n".join(fenced or inline for fenced, inline in examples)
        commands = [(name,) for name in set(cli.SCRIPTED) | set(cli.COMPOSITE)]
        commands += [(name, action) for name, (_file, actions, _help) in cli.ACTIONS.items()
                     for action in actions]
        for command in commands:
            with self.subTest(command=command):
                pattern = r"\bpairtask\s+" + r"\s+".join(map(re.escape, command)) + r"(?=\s|$)"
                self.assertRegex(code, pattern)

    def test_readme_links_directly_to_host_reference(self):
        readme = (SCRIPTS.parent / "README.md").read_text()
        self.assertRegex(readme, r"\[[^\]]+\]\((?:\./)?docs/host-commands\.md(?:#[^)]*)?\)")


if __name__ == "__main__":
    unittest.main()
