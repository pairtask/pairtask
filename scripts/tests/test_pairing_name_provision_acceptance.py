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

    def rollout(self, barrier_error=None):
        events = []
        self.events = events
        f = self.fixture

        def barrier_step(stage):
            events.append(stage)
            if stage == barrier_error:
                raise ValueError("fixture " + stage + " failed")

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
            stack.enter_context(patch.object(rollout.sandbox_pairing, "NameBarrier", return_value=SimpleNamespace(
                invalidate=lambda: barrier_step("invalidate"), release=lambda: barrier_step("release"))))
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
                wanted = ["stop", "invalidate", "plan", "apply", "release", "tools", "ssh"]
                if enabled is False:
                    wanted = ["stop", "plan", "apply", "tools", "ssh"]
                self.assertEqual(self.rollout(), wanted)
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

    def test_barrier_failure_aborts_before_apply_or_client_activation(self):
        self.fixture.args.pairing, self.fixture.args.name = True, "Requested"
        for stage, expected in (("invalidate", ["stop", "invalidate"]),
                                ("release", ["stop", "invalidate", "plan", "apply", "release"])):
            with self.subTest(stage=stage), self.assertRaisesRegex(ValueError, "fixture " + stage + " failed"):
                self.rollout(barrier_error=stage)
            self.assertEqual(self.events, expected)


class NameDeployment:
    def __init__(self):
        self.name = proof.INITIAL_NAME
        self.setup, self.broker = 100, 110
        self.observed = []
        self.ready = False
        self.file_name = proof.INITIAL_NAME
        self.ingress_attempts = 0

    def execute(self, role, *command):
        if command[:3] == ("python3", "-c", proof.INGRESS_BLOCKED):
            self.ingress_attempts += 1
            return SimpleNamespace(returncode=int(self.ready), stderr="fixture ingress admitted" if self.ready else "")
        return SimpleNamespace(returncode=0 if self.ready else 1, stderr="")

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
        return SimpleNamespace(returncode=0 if self.ready else 1, stdout=json.dumps({"pairing_result": {"self_name": self.name or "workspace"}}))


class NamePublication:
    def __init__(self, deployment):
        self.deployment, self.events = deployment, []
        self.project, self.remote, self.running = "fixture", "local:", True

    def stop(self, *roles):
        self.events.append(("stop", roles))
        self.running = False

    def set(self, **values):
        self.events.append(("set", values))
        self.pending = values["pairing_name"]

    def apply(self, release=True):
        if release:
            raise AssertionError("name proof must hold the barrier during apply")
        self.events.append(("apply", release))
        self.running = True
        self.deployment.file_name = self.pending
        return 0

    def start(self, *_args, **_kwargs):
        if self.deployment.ready or self.deployment.file_name != proof.INITIAL_NAME:
            raise AssertionError("delayed upload must start with OLD file and ready withheld")
        self.events.append(("start-before-upload",))
        self.running = True

    def barrier(self, project, remote, name):
        if (project, remote) != (self.project, self.remote):
            raise AssertionError("barrier did not target disposable deployment")
        publication = self

        class Barrier:
            def invalidate(self):
                if publication.running:
                    raise AssertionError("invalidation must happen while stopped")
                publication.events.append(("invalidate",))
                publication.deployment.ready = False

            def release(self):
                if not publication.running or publication.deployment.file_name != name:
                    raise AssertionError("release must follow exact upload on a running instance")
                publication.events.append(("release", name))
                publication.deployment.ready = True
                publication.deployment.name = name
                publication.deployment.setup += 100
                publication.deployment.broker += 100

        return Barrier()


class PairingNameProofAcceptance(unittest.TestCase):
    def test_observes_first_enable_special_character_update_and_clear(self):
        deployment = NameDeployment()
        publication = NamePublication(deployment)
        with patch.object(proof.sandbox_pairing, "NameBarrier", side_effect=publication.barrier), \
                patch.object(proof.subprocess, "run", side_effect=publication.start), redirect_stdout(io.StringIO()):
            proof.prove_name(deployment, publication)
        self.assertEqual(deployment.observed, [proof.INITIAL_NAME, proof.CHANGED_NAME, None])
        self.assertEqual(deployment.ingress_attempts, 4)
        self.assertEqual(publication.events, [
            ("release", proof.INITIAL_NAME),
            ("stop", ("workspace", "connector", "secured")), ("invalidate",),
            ("set", {"pairing_name": proof.CHANGED_NAME}), ("start-before-upload",), ("apply", False), ("release", proof.CHANGED_NAME),
            ("stop", ("workspace", "connector", "secured")), ("invalidate",),
            ("set", {"pairing_name": ""}), ("apply", False), ("release", ""),
        ])

    def test_wrong_environment_or_setup_order_fails_the_proof(self):
        for scenario in ("name", "order"):
            deployment = NameDeployment()
            deployment.ready = True
            if scenario == "name":
                deployment.name = "Wrong name"
            else:
                deployment.setup = deployment.broker + 1
            with self.subTest(scenario=scenario), self.assertRaises(ValueError):
                proof.observe_name(deployment, proof.INITIAL_NAME, "first")

    def test_gate_fails_if_broker_or_ingress_accepts_before_release(self):
        deployment = NameDeployment()
        deployment.ready = True
        with self.assertRaisesRegex(ValueError, "delayed: broker started"):
            proof.require_gated(deployment, "delayed")
        deployment.ready = False
        original = deployment.execute

        def admitted_ingress(role, *command):
            if role == "connector":
                return SimpleNamespace(returncode=1, stderr="fixture accepted")
            return original(role, *command)

        with patch.object(deployment, "execute", side_effect=admitted_ingress):
            with self.assertRaisesRegex(ValueError, "delayed: pairing ingress"):
                proof.require_gated(deployment, "delayed")

    def test_mismatch_diagnostics_name_phase_and_only_expected_fixture_value(self):
        deployment = NameDeployment()
        deployment.ready = True
        deployment.name = "old fixture"
        with self.assertRaisesRegex(ValueError, "update: installed pairing name expected 'new fixture', observed 'old fixture'"):
            proof.observe_name(deployment, "new fixture", "update")

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
