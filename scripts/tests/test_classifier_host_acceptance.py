"""Host classifier activation: real private files, simulated Incus and service restart."""

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


host = load("classifier_host", SCRIPTS / "sandbox-classifier.py")
setup = load("classifier_setup", ROOT / "infra/image/pairtask-broker-setup")


class HostCommandAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.key = self.root / "api-key"
        self.key.write_text("secret-123\n")
        self.key.chmod(0o600)

    def test_cli_dispatches_classifier_actions(self):
        import pairtask
        for action in ("on", "off", "status"):
            with self.subTest(action=action):
                steps = pairtask.plan(["classifier", action], system="Linux")
                self.assertEqual(steps[0].argv,
                                 ["sandbox-classifier.py", action])

    def test_default_remote_matches_the_host_platform(self):
        import sandbox_remote
        with patch.dict(os.environ, {}, clear=True):
            for system, remote in (("Linux", "local"), ("Darwin", "colima-pairtask")):
                with self.subTest(system=system), patch.object(sandbox_remote.platform,
                                                               "system", return_value=system):
                    self.assertEqual(host.parser().parse_args(["status"]).remote, remote)

    def test_root_refusal_stops_before_opening_the_deployment(self):
        with patch.object(host.sandbox_host_user, "refuse_root", return_value=1), \
             patch.object(host, "Secured", side_effect=AssertionError("opened Incus")):
            self.assertEqual(host.main(["status"]), 1)

    def test_pairing_route_accepts_the_pairing_commands_settings_files(self):
        args = SimpleNamespace(state_dir=str(self.root), project="pairtask")
        for name in ("operator.auto.tfvars.json", "custom.auto.tfvars.json"):
            with self.subTest(name=name):
                settings = self.root / name
                settings.write_text(json.dumps({"pairing_route": True, "secured_runtime": True}))
                self.assertTrue(host.pairing_route_on(args))
                settings.unlink()
        self.assertFalse(host.pairing_route_on(args))

    def test_pairing_route_uses_registry_and_rejects_deployed_route_that_is_off(self):
        settings = self.root / "operator.auto.tfvars.json"
        settings.write_text(json.dumps({"pairing_route": True}))
        registry = self.root / "locations.json"
        registry.write_text(json.dumps({"pairtask": str(self.root)}))
        args = SimpleNamespace(state_dir=None, project="pairtask")
        self.assertTrue(host.pairing_route_on(args, registry=registry))

        state = {"resources": [{"mode": "managed", "type": "incus_project", "name": "sandbox",
                "instances": [{"attributes": {"name": "pairtask"}}]}]}
        (self.root / "terraform.tfstate").write_text(json.dumps(state))
        self.assertFalse(host.pairing_route_on(args, registry=registry))

    def test_pairing_route_off_refuses_before_the_key_is_read_or_sent(self):
        with patch.object(host.sandbox_host_user, "refuse_root", return_value=0), \
             patch.object(host, "read_key", side_effect=AssertionError("read key")), \
             patch.object(host, "turn_on", side_effect=AssertionError("sent key")):
            with self.assertRaises(SystemExit) as result:
                host.main(["on", "--state-dir", str(self.root), "--key-file", str(self.key)])
        self.assertIn("pairtask pairing on", str(result.exception))

    def test_key_file_and_hidden_prompt_keep_secret_out_of_argv_and_output(self):
        args = SimpleNamespace(provider="jev", account="", key_file=str(self.key))
        key = host.read_key(args)
        calls = []

        def fake_run(argv, **kwargs):
            calls.append((argv, kwargs))
            return subprocess.CompletedProcess(argv, 0, stdout="", stderr="")

        secured = host.Secured("pairtask", "local", run=fake_run)
        with redirect_stdout(io.StringIO()) as output:
            host.turn_on(secured, args, key)
        self.assertEqual(len(calls), 1)
        argv, kwargs = calls[0]
        self.assertIn("classifier", argv)
        self.assertIn("on", argv)
        self.assertNotIn(key, " ".join(argv))
        self.assertEqual(kwargs["input"], key + "\n")
        self.assertNotIn(key, output.getvalue())

        args.key_file = None
        with patch.object(host, "getpass") as hidden:
            hidden.getpass.return_value = key
            prompted = host.read_key(args, stdin=SimpleNamespace(isatty=lambda: True),
                                     prompt=hidden.getpass)
        self.assertEqual(prompted, key)
        hidden.getpass.assert_called_once()

    def test_key_file_refuses_symlink_empty_or_multiline_and_prompt_requires_tty(self):
        args = SimpleNamespace(provider="jev", key_file=str(self.key))
        link = self.root / "link"
        link.symlink_to(self.key)
        args.key_file = str(link)
        with self.assertRaises(ValueError):
            host.read_key(args)
        args.key_file = str(self.key)
        for body in ("", "first\nsecond\n", "bad key\n"):
            with self.subTest(body=body):
                self.key.write_text(body)
                with self.assertRaises(ValueError):
                    host.read_key(args)
        args.key_file = None
        with self.assertRaisesRegex(ValueError, "--key-file"):
            host.read_key(args, stdin=SimpleNamespace(isatty=lambda: False))

    def test_key_file_must_not_be_readable_by_other_users(self):
        self.key.chmod(0o644)
        args = SimpleNamespace(provider="jev", key_file=str(self.key))
        with self.assertRaisesRegex(ValueError, "chmod 600"):
            host.read_key(args)

    def test_status_never_displays_the_secret(self):
        commands = []

        def fake_run(argv, **kwargs):
            commands.append(argv)
            if argv[-3:] == [setup_path, "classifier", "status"]:
                return subprocess.CompletedProcess(argv, 0,
                    stdout="classifier=shadow\nprovider=jev\naccount=\nkey=stored\n", stderr="")
            return subprocess.CompletedProcess(argv, 0, stdout="samples=2\n", stderr="")

        setup_path = host.SETUP
        secured = host.Secured("pairtask", "local", run=fake_run)
        with redirect_stdout(io.StringIO()) as output:
            host.status(secured, SimpleNamespace(since="24h", json=False))
        self.assertIn("Classifier: shadow, jev, key stored", output.getvalue())
        self.assertIn("samples=2", output.getvalue())
        self.assertNotIn("secret", output.getvalue())
        self.assertEqual(len(commands), 2)

    def test_json_status_is_one_parseable_object_without_the_key(self):
        def fake_run(argv, **kwargs):
            if argv[-3:] == [host.SETUP, "classifier", "status"]:
                return subprocess.CompletedProcess(argv, 0,
                    stdout="classifier=shadow\nprovider=jev\naccount=\nkey=stored\n", stderr="")
            return subprocess.CompletedProcess(argv, 0,
                stdout='{"samples":2,"errors":0}', stderr="")

        secured = host.Secured("pairtask", "local", run=fake_run)
        with redirect_stdout(io.StringIO()) as output:
            host.status(secured, SimpleNamespace(since="24h", json=True))
        parsed = json.loads(output.getvalue())
        self.assertEqual(parsed["classifier"]["provider"], "jev")
        self.assertEqual(parsed["report"]["samples"], 2)
        self.assertNotIn("secret", output.getvalue())


class SecuredSetupAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.classifier = self.root / "classifier"
        self.patch = patch.multiple(setup, CLASSIFIER=self.classifier,
                                    CLASSIFIER_KEY=self.classifier / "key",
                                    CLASSIFIER_ENV=self.classifier / "classifier.env")
        self.patch.start()
        self.addCleanup(self.patch.stop)
        identity = SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())
        self.broker = patch.object(setup.pwd, "getpwnam", return_value=identity)
        self.broker.start()
        self.addCleanup(self.broker.stop)
        self.chown = patch.object(setup.os, "chown")
        self.chown.start()
        self.addCleanup(self.chown.stop)

    def test_on_status_off_store_private_settings_and_restart_without_key_in_args(self):
        restarts = []

        def fake_run(argv, **kwargs):
            restarts.append(argv)
            return subprocess.CompletedProcess(argv, 0)

        with patch.object(setup.sys, "stdin", io.StringIO("secret-123\n")), \
             patch.object(subprocess, "run", side_effect=fake_run):
            setup.classifier(["on", "jev"])
        self.assertEqual(setup.CLASSIFIER_KEY.read_text(), "secret-123\n")
        self.assertEqual(stat.S_IMODE(setup.CLASSIFIER.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(setup.CLASSIFIER_KEY.stat().st_mode), 0o600)
        self.assertEqual(stat.S_IMODE(setup.CLASSIFIER_ENV.stat().st_mode), 0o600)
        env = setup.CLASSIFIER_ENV.read_text()
        self.assertIn("PAIRTASK_CLASSIFIER=shadow\n", env)
        self.assertIn("PAIRTASK_CLASSIFIER_PROVIDER=typesafe\n", env)
        self.assertIn("PAIRTASK_CLASSIFIER_KEY_FILE=" + str(setup.CLASSIFIER_KEY), env)
        self.assertNotIn("secret-123", env)
        self.assertEqual(restarts, [["systemctl", "restart", setup.BROKER_UNIT]])
        with redirect_stdout(io.StringIO()) as output:
            setup.classifier(["status"])
        self.assertIn("key=stored", output.getvalue())
        self.assertNotIn("secret-123", output.getvalue())
        with patch.object(subprocess, "run", side_effect=fake_run):
            setup.classifier(["off"])
        self.assertFalse(setup.CLASSIFIER_KEY.exists())
        self.assertFalse(setup.CLASSIFIER_ENV.exists())
        self.assertEqual(len(restarts), 2)

    def test_invalid_account_cannot_inject_environment_lines(self):
        bad = "account\nPAIRTASK_CLASSIFIER_KEY_FILE=/tmp/other"
        with self.assertRaises(ValueError):
            setup.classifier_on("clef", bad, "secret-123")
        self.assertFalse(setup.CLASSIFIER_ENV.exists())
        with self.assertRaises(ValueError):
            setup.classifier_on("jev", "account", "secret-123")

    def test_symlinked_classifier_directory_and_key_are_refused(self):
        target = self.root / "target"
        target.mkdir()
        self.classifier.symlink_to(target)
        with self.assertRaises(ValueError):
            setup.classifier_on("jev", "", "secret-123")
        self.assertEqual(list(target.iterdir()), [])
        self.classifier.unlink()
        self.classifier.mkdir()
        key_target = self.root / "other-key"
        key_target.write_text("leave alone")
        setup.CLASSIFIER_KEY.symlink_to(key_target)
        with self.assertRaises(ValueError):
            setup.classifier_on("jev", "", "secret-123")
        self.assertEqual(key_target.read_text(), "leave alone")


if __name__ == "__main__":
    unittest.main()
