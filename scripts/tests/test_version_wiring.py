"""Where pairtask version is wired in: before the chat, after pairtask
status and after a rollout. The report itself is covered by the manual app
update acceptance tests."""

import contextlib
import io
from pathlib import Path
import runpy
import subprocess
import sys
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import pairtask as cli  # noqa: E402
import sandbox_version  # noqa: E402

DEPLOYED = {"app": {"instances": {"secured": {}, "workspace": {}}}}
NOTHING = {"app": {"instances": {}}}


class StatusTests(unittest.TestCase):
    def test_status_lists_containers_then_one_cached_version_line(self):
        steps = cli.plan(["status", "--remote", "lab", "--project", "team-two"], system="Linux")
        self.assertEqual(steps[0].argv, ["incus", "--project", "team-two", "list", "lab:"])
        self.assertEqual(steps[1].argv, ["sandbox-version.py", "--summary", "--project", "team-two", "--remote", "lab"])

    def test_install_still_ends_with_the_container_list(self):
        steps = cli.plan(["install", "--remote", "local"], system="Linux")
        self.assertEqual(steps[-1].argv[:4], ["incus", "--project", "pairtask", "list"])

    def test_summary_without_a_deployment_says_unknown_and_succeeds(self):
        main = runpy.run_path(str(SCRIPTS / "sandbox-version.py"))["main"]
        output = io.StringIO()
        with mock.patch.object(sandbox_version, "report", return_value=NOTHING) as report, \
                mock.patch.object(sandbox_version, "publish") as publish, contextlib.redirect_stdout(output):
            self.assertEqual(main(["--summary", "--remote", "local"]), 0)
        self.assertEqual(output.getvalue().strip(), "version: unknown")
        self.assertFalse(report.call_args.kwargs["network"])
        publish.assert_not_called()


class ChatTests(unittest.TestCase):
    def setUp(self):
        self.main = runpy.run_path(str(SCRIPTS / "sandbox-session.py"))["main"]
        self.run = mock.Mock(return_value=0)
        patch = mock.patch.dict(self.main.__globals__, {"run_session": self.run})
        patch.start()
        self.addCleanup(patch.stop)

    def test_chat_announces_before_the_session_and_shell_does_not(self):
        order = []
        with mock.patch.object(sandbox_version, "announce", side_effect=lambda *a: order.append(("announce", a))):
            self.run.side_effect = lambda argv: order.append(("session", argv[0])) or 0
            self.main(["chat", "--remote", "lab", "--project", "work"])
            self.main(["shell", "--config", "/tmp/c"])
        self.assertEqual(order, [("announce", ("work", "lab")), ("session", "incus"), ("session", "ssh")])

    def test_a_custom_secured_container_is_not_announced_to(self):
        with mock.patch.object(sandbox_version, "announce") as announce:
            self.main(["chat", "--remote", "lab", "--secured", "secured-one"])
        announce.assert_not_called()
        self.run.assert_called_once()


class AnnounceTests(unittest.TestCase):
    def test_no_deployment_sends_nothing_and_starts_nothing(self):
        with mock.patch.object(sandbox_version, "report", return_value=NOTHING), \
                mock.patch.object(sandbox_version, "publish") as publish, \
                mock.patch.object(subprocess, "Popen") as popen:
            self.assertFalse(sandbox_version.announce("pairtask", "local"))
        publish.assert_not_called()
        popen.assert_not_called()

    def test_cached_notice_within_budget_then_detached_refresh(self):
        clock = iter([100.0, 106.5])  # the observation took 6.5 of the 10 seconds
        with mock.patch.object(sandbox_version, "report", return_value=DEPLOYED) as report, \
                mock.patch.object(sandbox_version, "publish") as publish, \
                mock.patch.object(sandbox_version, "now", side_effect=lambda: next(clock)), \
                mock.patch.object(subprocess, "Popen") as popen:
            self.assertTrue(sandbox_version.announce("work", "lab", budget=10))
        scope = report.call_args.args[0]
        self.assertEqual(scope, sandbox_version.Scope("work", "lab", "pairtask/pairtask"))
        self.assertFalse(report.call_args.kwargs["network"], "the chat never waits on GitHub")
        self.assertLess(report.call_args.kwargs["budget"], 10)
        self.assertAlmostEqual(publish.call_args.kwargs["timeout"], 3.5, msg="what is left of the budget")
        argv = popen.call_args.args[0]
        self.assertEqual(argv[2:], ["--refresh", "--quiet", "--project", "work", "--remote", "lab"])
        self.assertTrue(popen.call_args.kwargs["start_new_session"])
        for stream in ("stdin", "stdout", "stderr"):
            self.assertIs(popen.call_args.kwargs[stream], subprocess.DEVNULL)

    def test_a_failed_background_start_is_ignored(self):
        with mock.patch.object(sandbox_version, "report", return_value=DEPLOYED), \
                mock.patch.object(sandbox_version, "publish"), \
                mock.patch.object(subprocess, "Popen", side_effect=OSError("no python")):
            self.assertTrue(sandbox_version.announce("pairtask", "local"))


class RolloutTests(unittest.TestCase):
    def test_after_a_rollout_the_running_release_is_observed_published_and_printed(self):
        module = runpy.run_path(str(SCRIPTS / "sandbox-provision.py"))
        args = mock.Mock(project="team-two", remote="lab", repo="example/another")
        data = {"marker": True}
        output = io.StringIO()
        with mock.patch.object(sandbox_version, "report", return_value=data) as report, \
                mock.patch.object(sandbox_version, "publish") as publish, \
                mock.patch.object(sandbox_version, "summary_line", return_value="version: line") as line, \
                contextlib.redirect_stdout(output):
            module["report_versions"](args)
        self.assertEqual(report.call_args.args[0], sandbox_version.Scope("team-two", "lab", "example/another"))
        self.assertTrue(report.call_args.kwargs["refresh"])
        publish.assert_called_once_with(data)
        line.assert_called_once_with(data)
        self.assertEqual(output.getvalue().strip(), "version: line")


if __name__ == "__main__":
    unittest.main()
