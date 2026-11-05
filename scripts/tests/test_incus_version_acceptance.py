"""Pure-mock acceptance coverage for the Incus server requirement (#312)."""

import ast
import contextlib
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_incus_version as policy
import sandbox_operator_plan as plan_policy
import sandbox_operator_workflow as workflow


def load_script(name, filename):
    spec = importlib.util.spec_from_file_location(name, SCRIPTS / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


HOST = load_script("incus_acceptance_host", "sandbox-host.py")
PROVISION = load_script("incus_acceptance_provision", "sandbox-provision.py")


def metadata(version):
    return {"environment": {"server_version": version}}


class VersionPolicyTests(unittest.TestCase):
    def test_supported_server_boundaries(self):
        for version in ("7.0.2", "7.0.100", "7.5", "7.5.0", "7.10.9", "8.0"):
            with self.subTest(version=version):
                self.assertEqual(policy.check(metadata(version), "local"), version)

    def test_unsupported_server_boundaries(self):
        for version in ("6.99", "7.0", "7.0.1", "7.1", "7.4.99"):
            with self.subTest(version=version):
                with self.assertRaises(policy.IncusVersionError) as raised:
                    policy.check(metadata(version), "colima-pairtask")
                self.assertIn(version, str(raised.exception))
                self.assertIn("colima-pairtask", str(raised.exception))
                self.assertIn(policy.REQUIREMENT, str(raised.exception))

    def test_parser_accepts_only_plain_numeric_versions(self):
        invalid = (None, True, 7.5, [], {}, "", "7", "v7.5", " 7.5", "7.5\n", "7.5-rc1", "7.5.0.1")
        for value in invalid:
            with self.subTest(value=value):
                self.assertIsNone(policy.parse_version(value))

    def test_missing_or_malformed_server_version_fails_closed(self):
        invalid = (None, [], {}, {"environment": []}, {"environment": {}}, metadata(7.5), metadata("7.5-rc1"))
        for answer in invalid:
            with self.subTest(answer=answer):
                with self.assertRaisesRegex(policy.IncusVersionError, "unverified"):
                    policy.check(answer, "local")

    def test_client_and_api_versions_do_not_override_server(self):
        answer = metadata("7.1")
        answer.update(client_version="8.0", api_version="8.0")
        answer["environment"]["client_version"] = "8.0"
        with self.assertRaisesRegex(policy.IncusVersionError, "server 7.1.*unsupported"):
            policy.check(answer, "local")

    def test_upgrade_advice_moves_forward_on_the_correct_track(self):
        cases = (("6.9", "move to the 7.0 LTS"), ("7.0.1", "within 7.0 LTS"),
                 ("7.1", "forward to 7.5"), ("7.4", "forward to 7.5"))
        for version, advice in cases:
            with self.subTest(version=version):
                with self.assertRaises(policy.IncusVersionError) as raised:
                    policy.check(metadata(version), "local")
                self.assertIn(advice, str(raised.exception))

    def test_docs_and_python39_syntax_match_the_contract(self):
        self.assertIn(policy.REQUIREMENT, (SCRIPTS.parent / "docs/sandbox.md").read_text())
        for filename in ("sandbox_incus_version.py", "sandbox-host.py", "sandbox-provision.py",
                         "sandbox_operator_plan.py", "sandbox_operator_workflow.py"):
            with self.subTest(filename=filename):
                ast.parse((SCRIPTS / filename).read_text(), feature_version=(3, 9))


class ServerQueryTests(unittest.TestCase):
    def test_query_uses_selected_server_with_a_deadline(self):
        run = Mock(return_value=subprocess.CompletedProcess([], 0, json.dumps(metadata("7.5"))))
        self.assertEqual(policy.query_server("colima-pairtask", run), metadata("7.5"))
        run.assert_called_once_with(["incus", "query", "colima-pairtask:/1.0"],
                                    capture_output=True, text=True, check=True, timeout=30)

    def test_default_runner_is_resolved_at_call_time(self):
        with patch.object(policy.subprocess, "run", return_value=SimpleNamespace(stdout="{}")) as run:
            self.assertEqual(policy.query_server("local"), {})
        self.assertEqual(run.call_args.args[0], ["incus", "query", "local:/1.0"])

    def test_query_failures_are_unverified_with_the_original_cause(self):
        errors = (FileNotFoundError("incus"), subprocess.CalledProcessError(1, "incus"),
                  subprocess.TimeoutExpired("incus", 30))
        for error in errors:
            with self.subTest(error=type(error).__name__):
                with self.assertRaisesRegex(policy.IncusVersionError, "local.*unverified") as raised:
                    policy.query_server("local", Mock(side_effect=error))
                self.assertIs(raised.exception.__cause__, error)

    def test_invalid_json_is_unverified(self):
        run = Mock(return_value=SimpleNamespace(stdout="not JSON"))
        with self.assertRaisesRegex(policy.IncusVersionError, "unverified"):
            policy.query_server("local", run)


class HostVersionTests(unittest.TestCase):
    @contextlib.contextmanager
    def fake_start(self, answer):
        output = io.StringIO()
        with contextlib.ExitStack() as stack:
            for name in ("require_host_tools", "check_remote", "apply_profile"):
                stack.enter_context(patch.object(HOST, name))
            stack.enter_context(patch.object(HOST.Path, "is_socket", return_value=True))
            run = stack.enter_context(patch.object(HOST.subprocess, "run",
                         return_value=subprocess.CompletedProcess([], 0, json.dumps(answer))))
            stack.enter_context(contextlib.redirect_stdout(output))
            yield output, run

    def test_bootstrap_reports_verified_server_version(self):
        with self.fake_start(metadata("7.5")) as (output, run):
            HOST.start_host(Path("/mock/profile"), {}, ["colima", "start", "pairtask"])
        self.assertIn("Incus 7.5 ready", output.getvalue())
        self.assertEqual(run.call_args.args[0], ["incus", "query", "colima-pairtask:/1.0"])
        self.assertEqual(run.call_count, 2)

    def test_bootstrap_never_reports_ready_for_unsupported_or_unknown_server(self):
        for answer in (metadata("7.1"), {}, metadata("7.5-rc1")):
            with self.subTest(answer=answer), self.fake_start(answer) as (output, _):
                with self.assertRaises(ValueError):
                    HOST.start_host(Path("/mock/profile"), {}, ["colima", "start", "pairtask"])
                self.assertNotIn("ready", output.getvalue().lower())

    def test_bootstrap_plan_does_not_start_or_query_a_server(self):
        with tempfile.TemporaryDirectory() as temp, contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(HOST.platform, "system", return_value="Darwin"))
            stack.enter_context(patch.object(HOST.platform, "machine", return_value="arm64"))
            stack.enter_context(patch.object(HOST.Path, "home", return_value=Path(temp)))
            stack.enter_context(patch.dict(HOST.os.environ, {}, clear=True))
            run = stack.enter_context(patch.object(HOST.subprocess, "run"))
            stack.enter_context(contextlib.redirect_stdout(io.StringIO()))
            HOST.bootstrap(SimpleNamespace(action="plan"))
            run.assert_not_called()
            self.assertEqual(list(Path(temp).iterdir()), [])


class ProvisionVersionTests(unittest.TestCase):
    @contextlib.contextmanager
    def invocation(self, action, answer, destroy=False):
        args = SimpleNamespace(action=action, remote="local", destroy=destroy, state_dir=None, project="unit")
        operation = Mock()
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(PROVISION, "arguments", return_value=args))
            query = stack.enter_context(patch.object(PROVISION, "query_server", return_value=answer, create=True))
            stack.enter_context(patch.object(PROVISION.subprocess, "check_output",
                                             return_value=json.dumps(answer).encode()))
            directory = stack.enter_context(patch.object(PROVISION, "deployment_directory", return_value=Path("/mock/state")))
            stack.enter_context(patch.object(PROVISION, "remember"))
            for name in ("run_operation", "recover", "tools_retry", "rollout"):
                stack.enter_context(patch.object(PROVISION, name, operation))
            yield args, query, directory, operation

    def test_mutating_actions_refuse_unsupported_server_before_operations(self):
        for action in ("apply", "rollout", "pairing-on", "pairing-off", "tools-retry"):
            with self.subTest(action=action), self.invocation(action, metadata("7.1")) as (_, _, directory, operation):
                with self.assertRaisesRegex(ValueError, "unsupported"):
                    PROVISION.main()
                operation.assert_not_called()

    def test_mutating_actions_accept_supported_server(self):
        for action in ("apply", "rollout", "pairing-on", "pairing-off", "tools-retry"):
            with self.subTest(action=action), self.invocation(action, metadata("7.0.2")) as (args, query, _, operation):
                PROVISION.main()
                query.assert_called_once_with("local")
                operation.assert_called_once_with(args, Path("/mock/state"), metadata("7.0.2")["environment"])

    def test_unknown_version_blocks_apply(self):
        with self.invocation("apply", metadata("unknown")) as (_, _, directory, operation):
            with self.assertRaisesRegex(ValueError, "unverified"):
                PROVISION.main()
            operation.assert_not_called()

    def test_plan_recovery_and_destroy_continue_with_a_warning(self):
        for action, destroy in (("plan", False), ("recover", False), ("plan", True)):
            with self.subTest(action=action, destroy=destroy):
                self.assert_warning_continues(action, destroy)

    def assert_warning_continues(self, action, destroy):
        for version in ("7.1", "unknown"):
            with self.subTest(version=version), self.invocation(action, metadata(version), destroy) as (_, _, _, operation):
                warning = io.StringIO()
                with contextlib.redirect_stderr(warning):
                    PROVISION.main()
                operation.assert_called_once()
                self.assertIn("Warning:", warning.getvalue())
                self.assertIn(policy.REQUIREMENT, warning.getvalue())

    def test_pairing_status_does_not_query_or_gate_server(self):
        with self.invocation("pairing-status", {}) as (_, query, directory, operation):
            with patch.object(PROVISION.sandbox_pairing, "status", return_value="status"):
                self.assertEqual(PROVISION.main(), "status")
            query.assert_not_called()
            directory.assert_not_called()
            operation.assert_not_called()

    def test_query_failure_cannot_become_an_unverified_write(self):
        with self.invocation("apply", {}) as (_, query, directory, operation):
            query.side_effect = policy.IncusVersionError("unverified")
            with self.assertRaisesRegex(ValueError, "unverified"):
                PROVISION.main()
            directory.assert_not_called()
            operation.assert_not_called()


def resource_plan(*actions):
    return {"resource_changes": [{"change": {"actions": value}} for value in actions]}


class DeletionIntentTests(unittest.TestCase):
    def test_only_deletions_and_noops_qualify(self):
        for actions in ((["delete"],), (["no-op"], ["delete"], ["no-op"])):
            with self.subTest(actions=actions):
                self.assertTrue(plan_policy.deletion_only(resource_plan(*actions)))

    def test_creation_updates_replacement_reads_and_empty_plans_do_not_qualify(self):
        invalid = ((), (["no-op"],), (["create"],), (["update"],), (["read"],),
                   (["delete", "create"],), (["create", "delete"],),
                   (["delete"], ["create"]), (["delete"], ["update"]))
        for actions in invalid:
            with self.subTest(actions=actions):
                self.assertFalse(plan_policy.deletion_only(resource_plan(*actions)))

    def test_malformed_rendered_plans_do_not_qualify(self):
        invalid = (None, [], {}, {"resource_changes": None}, {"resource_changes": {}},
                   {"resource_changes": [None]}, {"resource_changes": [{}]},
                   {"resource_changes": [{"change": None}]},
                   {"resource_changes": [{"change": []}]},
                   {"resource_changes": [{"change": {"actions": "delete"}}]},
                   resource_plan(["delete"], []))
        for value in invalid:
            with self.subTest(value=value):
                self.assertFalse(plan_policy.deletion_only(value))


class SavedDeletionApplyTests(unittest.TestCase):
    def setUp(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        self.directory = Path(temp.name)
        self.plan = self.directory / workflow.PLAN
        self.plan.write_bytes(b"opaque saved tofu plan")
        self.current = {"files": {"main.tf": "config-hash"}, "server": "server-pin",
                        "options": {"destroy": False, "replace": False}}
        self.saved = copy.deepcopy(self.current)
        self.saved["options"]["destroy"] = True
        self.saved.update(deletion_only=True, plan_sha256=hashlib.sha256(self.plan.read_bytes()).hexdigest())
        self.save()

    def save(self):
        (self.directory / workflow.IDENTITY).write_text(json.dumps(self.saved))

    @contextlib.contextmanager
    def cli_apply(self):
        argv = ["sandbox-provision.py", "apply", "--remote", "local", "--state-dir", str(self.directory)]
        with contextlib.ExitStack() as stack:
            stack.enter_context(patch.object(PROVISION.sys, "argv", argv))
            stack.enter_context(patch.object(PROVISION, "query_server", return_value=metadata("7.1")))
            stack.enter_context(patch.object(PROVISION, "deployment_directory", return_value=self.directory))
            stack.enter_context(patch.object(PROVISION, "remember"))
            operation = stack.enter_context(patch.object(PROVISION, "run_operation"))
            stack.enter_context(contextlib.redirect_stderr(io.StringIO()))
            yield operation

    def test_real_cli_apply_uses_verified_saved_deletion_without_destroy_flag(self):
        with self.cli_apply() as operation:
            args = PROVISION.arguments()
            self.assertEqual(args.action, "apply")
            self.assertFalse(args.destroy)
            try:
                PROVISION.main()
            except policy.IncusVersionError as error:
                self.fail(f"Verified saved deletion was refused: {error}")
            operation.assert_called_once()
        workflow.require_saved_plan(self.directory, copy.deepcopy(self.current))

    def test_creation_or_mixed_saved_intent_cannot_bypass_the_requirement(self):
        self.saved["deletion_only"] = False
        self.save()
        with self.cli_apply() as operation:
            with self.assertRaisesRegex(ValueError, "unsupported"):
                PROVISION.main()
            operation.assert_not_called()

    def test_changed_plan_bytes_cannot_bypass_the_requirement(self):
        self.plan.write_bytes(b"changed plan")
        with self.cli_apply() as operation:
            with self.assertRaisesRegex(ValueError, "unsupported"):
                PROVISION.main()
            operation.assert_not_called()

    def test_saved_intent_requires_strict_boolean_flags(self):
        for key, value in (("destroy", "true"), ("destroy", 1), ("deletion_only", "true"), ("deletion_only", 1)):
            with self.subTest(key=key, value=value):
                saved = self.saved["options"] if key == "destroy" else self.saved
                saved[key] = value
                self.save()
                self.assertFalse(workflow.saved_deletion(self.directory))
                saved[key] = True

    def test_missing_or_malformed_identity_and_plan_fail_closed(self):
        identity = self.directory / workflow.IDENTITY
        for value in ("not json", "null", "[]", "{}", '{"options": []}',
                      '{"deletion_only": true, "options": ["destroy"]}'):
            with self.subTest(value=value):
                identity.write_text(value)
                self.assertFalse(workflow.saved_deletion(self.directory))
        identity.unlink()
        self.assertFalse(workflow.saved_deletion(self.directory))
        self.save()
        self.plan.unlink()
        self.assertFalse(workflow.saved_deletion(self.directory))

    def test_exemption_still_requires_matching_inputs_before_execution(self):
        executed = Mock()
        changed = copy.deepcopy(self.current)
        changed["files"]["main.tf"] = "different-config"

        def execute_checked(*_):
            workflow.require_saved_plan(self.directory, changed)
            executed()

        with self.cli_apply() as operation:
            operation.side_effect = execute_checked
            with self.assertRaisesRegex(ValueError, "inputs or server changed"):
                PROVISION.main()
            operation.assert_called_once()
            executed.assert_not_called()

    def test_planning_records_rendered_deletion_intent_and_actual_plan_digest(self):
        for actions, expected in ((["delete"], True), (["create"], False), (["delete", "create"], False)):
            with self.subTest(actions=actions):
                self.assert_recorded_plan_fact(actions, expected)

    def assert_recorded_plan_fact(self, actions, expected):
        args = SimpleNamespace(action="plan", destroy=True, project="unit", remote="local",
                               storage_pool="default", network="default")
        prepared = SimpleNamespace(args=args, directory=self.directory, identity=copy.deepcopy(self.saved))
        shown = resource_plan(actions)
        operator = Mock(target="mock-operator")
        operator.execute.return_value = SimpleNamespace(stdout=json.dumps(shown))
        with tempfile.TemporaryDirectory() as temp, contextlib.redirect_stdout(io.StringIO()):
            temporary = Path(temp)
            operator.run.side_effect = lambda *_: (temporary / workflow.PLAN).write_bytes(b"new approved plan")
            workflow.complete_operation(operator, temporary, prepared)
        saved = json.loads((self.directory / workflow.IDENTITY).read_text())
        self.assertIs(saved["deletion_only"], expected)
        self.assertEqual(saved["plan_sha256"], hashlib.sha256(b"new approved plan").hexdigest())
        self.assertEqual(workflow.saved_deletion(self.directory), expected)


if __name__ == "__main__":
    unittest.main()
