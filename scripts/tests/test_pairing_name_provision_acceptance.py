"""Rollout ordering and the installed-name proof's observations and failures."""

from contextlib import ExitStack, redirect_stdout
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import pairing_name_checks as proof
import sandbox_rollout as rollout
import test_pairing_command_acceptance as pairing_fixtures


class PairingNameRolloutAcceptance(unittest.TestCase):
    def setUp(self):
        self.fixture = pairing_fixtures.PairingRolloutAcceptance("test_on_upgrade_off_accepts_owned_connector_and_preserves_settings")
        self.fixture.setUp()
        self.addCleanup(self.fixture.doCleanups)

    def rollout(self):
        events = []
        f = self.fixture

        def operation(args, *_):
            events.append(args.action)
            if args.action == "apply":
                state = pairing_fixtures.deployment_state(bool(json.loads(f.variables.read_text())["pairing_route"]))
                (f.root / "terraform.tfstate").write_text(json.dumps(state))

        with ExitStack() as stack:
            stack.enter_context(patch.object(rollout, "stop_managed", side_effect=lambda *_: events.append("stop")))
            stack.enter_context(patch.object(rollout, "run_locked_operation", side_effect=operation))
            stack.enter_context(patch.object(rollout, "refresh_recipe"))
            stack.enter_context(patch.object(rollout, "install_tools", side_effect=lambda *_: events.append("tools")))
            stack.enter_context(patch.object(rollout, "configure_ssh", side_effect=lambda *_: events.append("ssh")))
            stack.enter_context(redirect_stdout(io.StringIO()))
            rollout.rollout(f.args, f.root, f.server)
        return events

    def test_install_name_change_upgrade_off_and_clear_keep_order_and_intent(self):
        f = self.fixture
        cases = ((True, "First", "First"), (True, "Changed", "Changed"), (None, None, "Changed"),
                 (False, None, "Changed"), (True, None, "Changed"), (True, "", ""))
        for enabled, name, expected in cases:
            with self.subTest(enabled=enabled, name=name):
                f.args.pairing, f.args.name = enabled, name
                self.assertEqual(self.rollout(), ["stop", "plan", "apply", "tools", "ssh"])
                self.assertEqual(json.loads(f.variables.read_text())["pairing_name"], expected)

    def test_invalid_name_precedes_initialization_and_stop(self):
        f = self.fixture
        f.args.name = "界" * 22
        before = f.variables.read_bytes()
        with patch.object(rollout, "initialize_variables") as initialize, patch.object(rollout, "stop_managed") as stop:
            with self.assertRaises(ValueError):
                rollout.rollout(f.args, f.root, f.server)
        initialize.assert_not_called()
        stop.assert_not_called()
        self.assertEqual(f.variables.read_bytes(), before)


class NameDeployment:
    def __init__(self):
        self.name = proof.INITIAL_NAME
        self.setup, self.broker = 100, 110
        self.observed = []

    def await_active(self, role, unit):
        if role != "secured" or unit not in (proof.SETUP_UNIT, proof.BROKER_UNIT):
            raise AssertionError("unexpected observed service")

    def main_pid(self, role, unit):
        return "42"

    def output(self, role, *command):
        if command[0] == "systemctl":
            return str(self.setup if command[-1] == proof.SETUP_UNIT else self.broker)
        if command[:3] != ("python3", "-c", proof.READ_NAME):
            raise AssertionError("proof must filter the one environment key in the guest")
        self.observed.append(self.name or None)
        return json.dumps(self.name or None)

    def operator(self, *command):
        return SimpleNamespace(returncode=0, stdout=json.dumps({"pairing_result": {"self_name": self.name or "workspace"}}))


class NamePublication:
    def __init__(self, deployment):
        self.deployment, self.events = deployment, []

    def stop(self, *roles):
        self.events.append(("stop", roles))

    def set(self, **values):
        self.events.append(("set", values))
        self.pending = values["pairing_name"]

    def apply(self):
        self.events.append(("apply",))
        self.deployment.name = self.pending
        self.deployment.setup += 100
        self.deployment.broker += 100
        return 0


class PairingNameProofAcceptance(unittest.TestCase):
    def test_observes_first_enable_special_character_update_and_clear(self):
        deployment = NameDeployment()
        publication = NamePublication(deployment)
        with redirect_stdout(io.StringIO()):
            proof.prove_name(deployment, publication)
        self.assertEqual(deployment.observed, [proof.INITIAL_NAME, proof.CHANGED_NAME, None])
        self.assertEqual(publication.events, [
            ("stop", ("workspace", "connector", "secured")), ("set", {"pairing_name": proof.CHANGED_NAME}), ("apply",),
            ("stop", ("workspace", "connector", "secured")), ("set", {"pairing_name": ""}), ("apply",),
        ])

    def test_wrong_environment_or_setup_order_fails_the_proof(self):
        for scenario in ("name", "order"):
            deployment = NameDeployment()
            if scenario == "name":
                deployment.name = "Wrong name"
            else:
                deployment.setup = deployment.broker + 1
            with self.subTest(scenario=scenario), self.assertRaises(ValueError):
                proof.prove_name(deployment, NamePublication(deployment))

    def test_guest_filter_outputs_only_name_and_preserves_exact_bytes(self):
        value = proof.CHANGED_NAME
        content = b"UNRELATED=PRIVATE_FIXTURE_VALUE\0PAIRTASK_PAIRING_NAME=" + value.encode() + b"\0"
        output = io.StringIO()
        with patch.object(Path, "read_bytes", return_value=content), patch.object(sys, "argv", ["read-name", "42"]):
            with redirect_stdout(output):
                exec(proof.READ_NAME, {"__name__": "__main__"})
        self.assertEqual(json.loads(output.getvalue()), value)
        self.assertNotIn("PRIVATE_FIXTURE_VALUE", output.getvalue())

    def test_docs_limit_changes_to_future_pairings(self):
        text = (SCRIPTS.parent / "docs/pairing.md").read_text().lower()
        self.assertIn("--name", text)
        self.assertIn("workspaces you pair with afterwards", text)
        self.assertIn("not the ones already paired", text)
        self.assertNotIn("which `pairtask pairing on` can't set yet", text)


if __name__ == "__main__":
    unittest.main()
