"""Discovery helper service lifecycle: units in a temporary home, a recording runner, no real service manager."""

import contextlib
import importlib.util
import io
import json
import os
from pathlib import Path
import plistlib
import sys
import tempfile
import unittest
from unittest import mock

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_discovery_service as service  # noqa: E402


def load_script(name):
    spec = importlib.util.spec_from_file_location("discovery_service_" + name.replace("-", "_"), SCRIPTS / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


class ServiceFixture(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.home = self.root / "home"
        patcher = mock.patch.dict(os.environ, {"PAIRTASK_HOME": str(self.root / "pairtask home")})
        patcher.start()
        self.addCleanup(patcher.stop)
        self.commands = []

    def manager(self, system, dry_run=False):
        def run(command):
            self.commands.append(command)
            return True, ""
        return service.Manager("pairtask", "local", dry_run=dry_run, host=service.Host(system, self.home, run))


class ServiceUnitTests(ServiceFixture):
    def test_linux_unit_runs_the_installed_launcher_and_enables_now(self):
        manager = self.manager("Linux")
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertTrue(manager.install())
        path = self.home / ".config/systemd/user/pairtask-discovery.service"
        text = path.read_text()
        launcher = str(self.root / "pairtask home" / "bin" / "pairtask")
        self.assertIn('ExecStart="%s" "discovery" "run"' % launcher, text)
        self.assertIn("WantedBy=default.target", text)
        self.assertEqual(self.commands, [["systemctl", "--user", "daemon-reload"],
                                         ["systemctl", "--user", "enable", "--now", "pairtask-discovery.service"]])

    def test_linux_reinstall_restarts_and_remove_disables(self):
        manager = self.manager("Linux")
        with contextlib.redirect_stdout(io.StringIO()):
            manager.install()
            self.commands.clear()
            manager.install()
            self.assertIn(["systemctl", "--user", "restart", "pairtask-discovery.service"], self.commands)
            manager.remove()
        self.assertFalse(manager.path().exists())
        self.assertIn(["systemctl", "--user", "disable", "--now", "pairtask-discovery.service"], self.commands)

    def test_macos_agent_bootstraps_in_the_gui_domain(self):
        manager = self.manager("Darwin")
        with contextlib.redirect_stdout(io.StringIO()):
            manager.install()
        path = self.home / "Library/LaunchAgents/ai.pairtask.discovery.plist"
        agent = plistlib.loads(path.read_bytes())
        self.assertEqual(agent["Label"], "ai.pairtask.discovery")
        self.assertEqual(agent["ProgramArguments"][1:3], ["discovery", "run"])
        self.assertEqual(self.commands[-1], ["launchctl", "bootstrap", "gui/%d" % os.getuid(), str(path)])

    def test_macos_bootstrap_error_is_not_reported_when_job_loaded(self):
        commands = []

        def run(command):
            commands.append(command)
            if command[1] == "bootstrap":
                return False, "Bootstrap failed: 5: Input/output error"
            return command[1] == "print", "state = running" if command[1] == "print" else "not loaded"

        manager = service.Manager(host=service.Host("Darwin", self.home, run))
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertTrue(manager.install())
        self.assertNotIn("failed", output.getvalue())
        self.assertEqual(commands[-1], ["launchctl", "print", "gui/%d/%s" % (os.getuid(), service.LABEL)])

    def test_macos_bootstrap_error_is_reported_when_job_not_loaded(self):
        def run(command):
            if command[1] == "bootstrap":
                return False, "Bootstrap failed: 5: Input/output error"
            return False, "not loaded"

        manager = service.Manager(host=service.Host("Darwin", self.home, run))
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertFalse(manager.install())
        self.assertIn("Bootstrap failed: 5: Input/output error", output.getvalue())

    def test_dry_run_prints_the_unit_and_writes_nothing(self):
        manager = self.manager("Linux", dry_run=True)
        with contextlib.redirect_stdout(io.StringIO()) as output:
            manager.install()
        self.assertIn("ExecStart=", output.getvalue())
        self.assertFalse(self.home.exists())
        self.assertEqual(self.commands, [])

    def test_failing_service_manager_is_reported_not_raised(self):
        manager = service.Manager(host=service.Host("Linux", self.home, lambda command: (False, "no user bus")))
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertFalse(manager.install())
        self.assertIn("pairing route is unaffected", output.getvalue())

    def test_opt_out_is_remembered_in_the_deployment(self):
        self.assertTrue(service.remembered(self.root))
        service.remember(self.root, False)
        self.assertFalse(service.remembered(self.root))
        self.assertEqual(json.loads((self.root / service.SETTING).read_text()), {"enabled": False})


class ProvisionDiscoveryTests(ServiceFixture):
    def setUp(self):
        super().setUp()
        self.provision = load_script("sandbox-provision")

    def parse(self, *argv):
        with mock.patch.object(sys, "argv", ["sandbox-provision", *argv]):
            return self.provision.arguments()

    def test_discovery_flags_are_only_for_pairing_on(self):
        self.assertIs(self.parse("pairing-on", "--no-discovery").discovery, False)
        self.assertIsNone(self.parse("pairing-on").discovery)
        for action in ("pairing-off", "rollout"):
            with self.subTest(action=action), contextlib.redirect_stderr(io.StringIO()), self.assertRaises(SystemExit):
                self.parse(action, "--no-discovery")

    def test_opt_out_persists_through_later_rollouts(self):
        (self.root / "operator.auto.tfvars.json").write_text(json.dumps({"pairing_route": True}))
        calls = []
        with mock.patch.object(self.provision.sandbox_discovery_service, "sync",
                               side_effect=lambda wanted, project, remote: calls.append(wanted)):
            self.provision.sync_discovery(self.parse("pairing-on", "--no-discovery"), self.root)
            self.provision.sync_discovery(self.parse("rollout"), self.root)
            self.provision.sync_discovery(self.parse("pairing-on"), self.root)
            self.provision.sync_discovery(self.parse("pairing-on", "--discovery"), self.root)
            self.provision.sync_discovery(self.parse("pairing-off"), self.root)
        self.assertEqual(calls, [False, False, False, True, False])


if __name__ == "__main__":
    unittest.main()
