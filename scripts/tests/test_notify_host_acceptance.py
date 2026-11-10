"""Slack decision notifications: private host input and secured broker storage."""

from contextlib import redirect_stdout
from importlib.machinery import SourceFileLoader
from importlib.util import module_from_spec, spec_from_loader
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[2]
SCRIPTS = ROOT / "scripts"
sys.path.insert(0, str(SCRIPTS))


def load(name, path):
    loader = SourceFileLoader(name, str(path))
    spec = spec_from_loader(name, loader)
    module = module_from_spec(spec)
    loader.exec_module(module)
    return module


host = load("notify_host", SCRIPTS / "sandbox-notify.py")
setup = load("notify_setup", ROOT / "infra/image/pairtask-broker-setup")
WEBHOOK = "https://hooks.slack.com/services/T000/B000/secret-marker"


class NotifyHostAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.secret = self.root / "webhook"
        self.secret.write_text(WEBHOOK + "\n")
        self.secret.chmod(0o600)

    def test_cli_dispatches_notify_actions(self):
        import pairtask
        for action in ("on", "off", "status"):
            with self.subTest(action=action):
                args = ["notify", action] + (["--slack"] if action == "on" else [])
                steps = pairtask.plan(args, system="Linux")
                self.assertEqual(steps[0].argv, ["sandbox-notify.py"] + args[1:])

    def test_route_off_refuses_before_secret_read_or_send(self):
        with patch.object(host.sandbox_host_user, "refuse_root", return_value=0), \
             patch.object(host, "read_secret", side_effect=AssertionError("read secret")), \
             patch.object(host, "turn_on", side_effect=AssertionError("sent secret")):
            with self.assertRaises(SystemExit) as result:
                host.main(["on", "--slack", "--state-dir", str(self.root), "--secret-file", str(self.secret)])
        self.assertIn("pairtask pairing on", str(result.exception))

    def test_private_file_and_hidden_prompt_accept_one_webhook(self):
        args = SimpleNamespace(secret_file=str(self.secret))
        self.assertEqual(host.read_secret(args), WEBHOOK)
        args.secret_file = None
        with patch.object(host, "getpass") as hidden:
            hidden.getpass.return_value = WEBHOOK
            prompted = host.read_secret(args, stdin=SimpleNamespace(isatty=lambda: True), prompt=hidden.getpass)
        self.assertEqual(prompted, WEBHOOK)
        hidden.getpass.assert_called_once()

    def test_secret_file_rejects_symlink_world_readable_and_wrong_destination(self):
        args = SimpleNamespace(secret_file=str(self.secret))
        link = self.root / "link"
        link.symlink_to(self.secret)
        args.secret_file = str(link)
        with self.assertRaises(ValueError):
            host.read_secret(args)
        args.secret_file = str(self.secret)
        self.secret.chmod(0o644)
        with self.assertRaisesRegex(ValueError, "chmod 600"):
            host.read_secret(args)
        self.secret.chmod(0o600)
        for invalid in ("https://evil.example/services/T/B/C", "https://hooks.slack.com/services/%zz",
                        "https://hooks.slack.com/services/T/B/C?token=extra", ""):
            with self.subTest(invalid=invalid):
                self.secret.write_text(invalid + "\n")
                with self.assertRaises(ValueError):
                    host.read_secret(args)
        args.secret_file = None
        with self.assertRaisesRegex(ValueError, "--secret-file"):
            host.read_secret(args, stdin=SimpleNamespace(isatty=lambda: False))

    def test_on_sends_secret_only_on_stdin_and_status_never_displays_it(self):
        calls = []

        def fake_run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0, stdout="notify=slack\nhidden=1\nsecret=stored\n", stderr="")

        secured = host.Secured("pairtask", "local", run=fake_run)
        with redirect_stdout(io.StringIO()) as output:
            host.turn_on(secured, SimpleNamespace(hidden=True), WEBHOOK)
            host.status(secured, SimpleNamespace(json=False))
        self.assertEqual(len(calls), 2)
        argv, kwargs = calls[0]
        self.assertEqual(argv[-5:], [host.SETUP, "notify", "on", "slack", "--hidden"])
        self.assertNotIn(WEBHOOK, " ".join(argv))
        self.assertEqual(kwargs["input"], WEBHOOK + "\n")
        self.assertNotIn(WEBHOOK, output.getvalue())
        self.assertIn("secret stored", output.getvalue())

    def test_json_status_has_no_webhook(self):
        def fake_run(argv, **kwargs):
            return subprocess.CompletedProcess(argv, 0, stdout="notify=slack\nhidden=0\nsecret=stored\n", stderr="")

        secured = host.Secured("pairtask", "local", run=fake_run)
        with redirect_stdout(io.StringIO()) as output:
            host.status(secured, SimpleNamespace(json=True))
        self.assertEqual(json.loads(output.getvalue())["notify"]["secret"], "stored")
        self.assertNotIn(WEBHOOK, output.getvalue())


class NotifySecuredSetupAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.notify = self.root / "notify"
        files = patch.multiple(setup, NOTIFY=self.notify, NOTIFY_SECRET=self.notify / "slack-webhook",
                               NOTIFY_ENV=self.notify / "notify.env")
        files.start()
        self.addCleanup(files.stop)
        identity = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
        broker = patch.object(setup.pwd, "getpwnam", return_value=identity)
        broker.start()
        self.addCleanup(broker.stop)
        chown = patch.object(setup.os, "chown")
        chown.start()
        self.addCleanup(chown.stop)

    def test_on_status_off_keep_webhook_private_and_restart(self):
        restarts = []

        def fake_run(argv, **kwargs):
            restarts.append(argv)
            return subprocess.CompletedProcess(argv, 0)

        with patch.object(setup.sys, "stdin", io.StringIO(WEBHOOK + "\n")), \
             patch.object(subprocess, "run", side_effect=fake_run):
            setup.notify(["on", "slack", "--hidden"])
        self.assertEqual(setup.NOTIFY_SECRET.read_text(), WEBHOOK + "\n")
        self.assertEqual(stat.S_IMODE(setup.NOTIFY.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(setup.NOTIFY_SECRET.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(setup.NOTIFY_ENV.stat().st_mode), 0o600)
        env = setup.NOTIFY_ENV.read_text()
        self.assertIn("PAIRTASK_NOTIFY=slack\n", env)
        self.assertIn("PAIRTASK_NOTIFY_HIDDEN=1\n", env)
        self.assertNotIn(WEBHOOK, env)
        self.assertEqual(restarts, [["systemctl", "restart", setup.BROKER_UNIT]])
        with redirect_stdout(io.StringIO()) as output:
            setup.notify(["status"])
        self.assertIn("secret=stored", output.getvalue())
        self.assertNotIn(WEBHOOK, output.getvalue())
        with patch.object(subprocess, "run", side_effect=fake_run):
            setup.notify(["off"])
        self.assertFalse(setup.NOTIFY_SECRET.exists())
        self.assertFalse(setup.NOTIFY_ENV.exists())
        self.assertEqual(len(restarts), 2)

    def test_invalid_webhook_is_rejected_before_creating_any_file(self):
        for invalid in ("https://evil.example/services/T/B/C", "https://hooks.slack.com/services/%zz",
                        "https://hooks.slack.com/services/T/B/C#fragment"):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    setup.notify_on("slack", False, invalid)
                self.assertFalse(self.notify.exists())

    def test_symlinked_secret_directory_is_refused(self):
        target = self.root / "target"
        target.mkdir()
        self.notify.symlink_to(target)
        with self.assertRaises(ValueError):
            setup.notify_on("slack", False, WEBHOOK)
        self.assertEqual(list(target.iterdir()), [])


if __name__ == "__main__":
    unittest.main()
