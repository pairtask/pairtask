"""Publishing acceptance: persisted intent, exact applied device, CLI and upgrade."""

from contextlib import redirect_stderr
import io
import json
import sys
import unittest
from unittest.mock import patch

import test_pairing_command_acceptance as base

pairing = base.pairing
deployment_state = base.deployment_state
load_script = base.load_script


def published_state(port=7445):
    state = deployment_state(True)
    attrs = state["resources"][-1]["instances"][0]["attributes"]
    attrs["device"] = [{"name": "pairing-relay", "type": "proxy", "properties": {
        "listen": "tcp:0.0.0.0:%d" % port, "connect": "tcp:127.0.0.1:7445",
        "bind": "host", "nat": "false"}}]
    return state


class PublishingIntentAcceptance(base.PairingFixture):
    def test_on_initializes_port_but_preserves_explicit_saved_choice(self):
        for saved in (None, 0, 1024, 8445, 65535):
            with self.subTest(saved=saved):
                original = {"pairing_route": True, "custom": "keep"}
                if saved is not None:
                    original["pairing_host_port"] = saved
                self.write_variables(original)
                before = self.variables.read_bytes()
                change = pairing.pairing_change(self.root, True)
                self.assertEqual(change.variables["pairing_host_port"], 7445 if saved is None else saved)
                self.assertEqual(change.variables["custom"], "keep")
                self.assertEqual(self.variables.read_bytes(), before)

    def test_change_disable_and_reenable_preserve_port(self):
        self.write_variables({"pairing_host_port": 7445, "custom": [1, 2]})
        for port in (8445, 0, 65535):
            pairing.write_change(pairing.pairing_change(self.root, True, host_port=port))
            pairing.write_change(pairing.pairing_change(self.root, False))
            data = json.loads(self.variables.read_text())
            self.assertFalse(data["pairing_route"])
            self.assertEqual(data["pairing_host_port"], port)
            self.assertEqual(data["custom"], [1, 2])
            pairing.write_change(pairing.pairing_change(self.root, True))
            self.assertEqual(json.loads(self.variables.read_text())["pairing_host_port"], port)

    def test_bad_saved_or_requested_ports_are_refused_without_write(self):
        for value in (True, False, -1, 1, 1023, 65536, 7445.0, 7445.5, "7445", [], {}):
            for saved in (False, True):
                with self.subTest(value=value, saved=saved):
                    self.write_variables({"pairing_host_port": value} if saved else {"custom": 1})
                    before = self.variables.read_bytes()
                    with self.assertRaises(ValueError):
                        pairing.pairing_change(self.root, True, **({} if saved else {"host_port": value}))
                    self.assertEqual(self.variables.read_bytes(), before)
        self.write_variables({"pairing_host_port": None})
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)

    def test_off_cannot_take_port_override(self):
        for port in (0, 7445):
            with self.subTest(port=port), self.assertRaises(ValueError):
                pairing.pairing_change(self.root, False, host_port=port)
        self.assertFalse(self.variables.exists())

    def test_port_alone_owns_control_file_and_split_or_hcl_is_refused(self):
        custom = self.write_variables({"pairing_host_port": 8445}, self.root / "custom.auto.tfvars.json")
        self.assertEqual(pairing.pairing_change(self.root, True).path, custom)
        self.write_variables({"secured_runtime": True})
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)
        self.variables.unlink()
        custom.unlink()
        for filename in ("terraform.tfvars", "custom.auto.tfvars"):
            hcl = self.root / filename
            hcl.write_text("/* retained setting */ pairing_host_port = 0\n")
            with self.assertRaises(ValueError):
                pairing.pairing_change(self.root, True)
            hcl.unlink()


class PublishingStateAcceptance(base.PairingFixture):
    def write_state(self, state):
        (self.root / "terraform.tfstate").write_text(json.dumps(state))

    def test_absent_unpublished_and_published_are_distinct(self):
        self.assertIsNone(pairing.published_port(self.root, "proof"))
        self.write_state(deployment_state(False))
        self.assertIsNone(pairing.published_port(self.root, "proof"))
        self.write_state(deployment_state(True))
        self.assertEqual(pairing.published_port(self.root, "proof"), 0)
        for port in (1024, 7445, 8445, 65535):
            self.write_state(published_state(port))
            self.assertEqual(pairing.published_port(self.root, "proof"), port)
            self.assertIs(pairing.applied(self.root, "proof"), True)

    def test_device_must_be_exact_expected_forwarder(self):
        cases = [("type", "nic"), ("listen", "tcp:127.0.0.1:7445"),
                 ("listen", "udp:0.0.0.0:7445"), ("listen", "tcp:0.0.0.0:80"),
                 ("listen", "tcp:0.0.0.0:65536"), ("connect", "tcp:127.0.0.1:7444"),
                 ("bind", "guest"), ("nat", "true")]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                state = published_state()
                device = state["resources"][-1]["instances"][0]["attributes"]["device"][0]
                (device if key == "type" else device["properties"])[key] = value
                self.write_state(state)
                with self.assertRaises(ValueError):
                    pairing.published_port(self.root, "proof")

    def test_incomplete_or_duplicate_relay_devices_are_refused(self):
        for missing in ("listen", "connect", "bind", "nat"):
            with self.subTest(missing=missing):
                state = published_state()
                attrs = state["resources"][-1]["instances"][0]["attributes"]
                del attrs["device"][0]["properties"][missing]
                self.write_state(state)
                with self.assertRaises(ValueError):
                    pairing.published_port(self.root, "proof")
        state = published_state()
        attrs = state["resources"][-1]["instances"][0]["attributes"]
        attrs["device"].append(attrs["device"][0])
        self.write_state(state)
        with self.assertRaises(ValueError):
            pairing.published_port(self.root, "proof")

    def test_wrong_project_and_symlink_state_are_refused(self):
        self.write_state(published_state())
        with self.assertRaises(ValueError):
            pairing.published_port(self.root, "other")
        path = self.root / "terraform.tfstate"
        target = self.root / "actual.json"
        path.rename(target)
        path.symlink_to(target)
        with self.assertRaises(ValueError):
            pairing.published_port(self.root, "proof")

    def test_status_reports_configured_and_applied_ports_without_host_effects(self):
        self.write_variables({"pairing_route": True, "pairing_host_port": 8445})
        self.write_state(published_state(7445))
        before = {p.name: p.read_bytes() for p in self.root.iterdir()}
        with patch("subprocess.run", side_effect=AssertionError("host call")), \
             patch("subprocess.check_output", side_effect=AssertionError("host query")):
            description = "\n".join(pairing.describe(self.root, "proof"))
        self.assertIn("8445", description)
        self.assertIn("7445", description)
        self.assertEqual({p.name: p.read_bytes() for p in self.root.iterdir()}, before)


class PublishingRolloutAcceptance(base.PairingRolloutAcceptance):
    # Inherited cases verify that publishing retains the original rollout contract.
    def test_plain_upgrade_does_not_publish_legacy_route(self):
        original = {"project_name": "proof", "secured_runtime": True, "pairing_route": True}
        self.write_variables(original)
        (self.root / "terraform.tfstate").write_text(json.dumps(deployment_state(True)))
        self.args.pairing = None
        self.args.host_port = None
        self.run_rollout(lambda *_: None)
        self.assertEqual(json.loads(self.variables.read_text()), original)
        self.assertEqual(pairing.published_port(self.root, "proof"), 0)

    def test_requested_port_reaches_apply_and_failure_preserves_intent(self):
        self.args.host_port = 8445
        def operation(args, directory, server):
            self.assertEqual(json.loads(self.variables.read_text())["pairing_host_port"], 8445)
            if args.action == "apply":
                raise ValueError("port collision")
        with self.assertRaisesRegex(ValueError, "port collision"):
            self.run_rollout(operation)
        self.assertEqual(json.loads(self.variables.read_text())["pairing_host_port"], 8445)
        self.assertFalse(pairing.applied(self.root, "proof"))


class PublishingCLICommandsAcceptance(unittest.TestCase):
    def test_port_flag_only_allowed_on_pairing_on(self):
        provision = load_script("sandbox-provision")
        for action in ("pairing-on", "pairing-off", "pairing-status", "rollout", "plan", "apply"):
            with self.subTest(action=action), patch.object(sys, "argv", ["sandbox-provision", action, "--host-port", "8445"]):
                if action == "pairing-on":
                    self.assertEqual(provision.arguments().host_port, 8445)
                else:
                    with redirect_stderr(io.StringIO()), self.assertRaises(SystemExit) as raised:
                        provision.arguments()
                    self.assertEqual(raised.exception.code, 2)


if __name__ == "__main__":
    unittest.main()
