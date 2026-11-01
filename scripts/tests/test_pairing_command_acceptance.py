"""Pairing host command acceptance: real files and rollout, mocked host effects."""

import argparse
from contextlib import ExitStack, redirect_stdout
import copy
import hashlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_pairing as pairing
import sandbox_rollout as rollout


def load_script(name):
    spec = importlib.util.spec_from_file_location("pairing_acceptance_" + name.replace("-", "_"), SCRIPTS / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def deployment_state(connector=False):
    resources = [{"mode": "managed", "type": "incus_project", "name": "sandbox",
                  "instances": [{"attributes": {"name": "proof"}}]}]
    for name in ("workspace", "secured"):
        resources.append({"mode": "managed", "type": "incus_instance", "name": name,
                          "instances": [{"attributes": {"project": "proof", "name": name}}]})
    if connector:
        resources.append({"mode": "managed", "type": "incus_instance", "name": "pairing_connector",
                          "instances": [{"attributes": {"project": "proof", "name": "connector",
                              "config": {"user.pairtask.role": "pairing-connector"}}}]})
    return {"resources": resources}


class PairingFixture(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.variables = self.root / "operator.auto.tfvars.json"

    def write_variables(self, value, path=None):
        target = path or self.variables
        target.write_text(json.dumps(value))
        return target


class PairingFilesAcceptance(PairingFixture):
    def test_on_off_preserve_other_keys_and_validate_without_writing(self):
        original = {"project_name": "proof", "secured_runtime": False,
                    "storage_pool": "custom", "nested": {"keep": [1, "two"]}}
        self.write_variables(original)
        before = self.variables.read_bytes()
        change = pairing.pairing_change(self.root, True)
        self.assertEqual(self.variables.read_bytes(), before)
        self.assertEqual(change.path, self.variables)
        pairing.write_change(change)
        self.assertEqual(json.loads(self.variables.read_text()),
                         dict(original, secured_runtime=True, pairing_route=True, pairing_host_port=7445))
        self.assertEqual(stat.S_IMODE(self.variables.stat().st_mode), 0o600)
        pairing.write_change(pairing.pairing_change(self.root, False))
        self.assertEqual(json.loads(self.variables.read_text()),
                         dict(original, secured_runtime=True, pairing_route=False, pairing_host_port=7445))
        self.assertFalse(pairing.configured(self.root))

    def test_existing_control_file_is_updated_without_creating_competing_default(self):
        target = self.write_variables({"secured_runtime": True, "custom": 7}, self.root / "custom.auto.tfvars.json")
        change = pairing.pairing_change(self.root, True)
        self.assertEqual(change.path, target)
        pairing.write_change(change)
        self.assertFalse(self.variables.exists())
        self.assertEqual(json.loads(target.read_text()), {"secured_runtime": True, "custom": 7, "pairing_route": True, "pairing_host_port": 7445})

    def test_invalid_json_shapes_and_nonboolean_controls_never_write(self):
        bodies = ["{broken", "[]", "null", "42"]
        for key in ("pairing_route", "secured_runtime", "peer_connector_probe"):
            bodies.extend(json.dumps({key: value}) for value in ("false", 0, 1, None, [], {}))
        for body in bodies:
            with self.subTest(body=body):
                self.variables.write_text(body)
                with self.assertRaises(ValueError):
                    pairing.pairing_change(self.root, True)
                self.assertEqual(self.variables.read_text(), body)

    def test_probe_refused_on_but_off_does_not_change_security_controls(self):
        original = {"peer_connector_probe": True, "secured_runtime": True, "pairing_route": False}
        self.write_variables(original)
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)
        pairing.write_change(pairing.pairing_change(self.root, False))
        self.assertEqual(json.loads(self.variables.read_text()), original)

    def test_split_controls_and_hcl_controls_refused(self):
        self.write_variables({"secured_runtime": True})
        other = self.write_variables({"pairing_route": False}, self.root / "other.auto.tfvars.json")
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)
        other.unlink()
        for filename in ("terraform.tfvars", "custom.auto.tfvars"):
            for key in ("pairing_route", "secured_runtime", "peer_connector_probe"):
                with self.subTest(filename=filename, key=key):
                    path = self.root / filename
                    path.write_text(key + " = false\n")
                    with self.assertRaises(ValueError):
                        pairing.pairing_change(self.root, True)
                    path.unlink()
        self.assertEqual(json.loads(self.variables.read_text()), {"secured_runtime": True})

    def test_hcl_control_after_inline_comment_is_not_silently_overridden(self):
        self.write_variables({"secured_runtime": True})
        (self.root / "terraform.tfvars").write_text("/* operator setting */ pairing_route = false\n")
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)

    def test_read_only_discovery_missing_registry_creates_nothing(self):
        location = self.root / "missing" / "registry.json"
        with patch.object(pairing, "worktree_directories", return_value=[]):
            found = pairing.find_deployment(None, "proof", root=self.root, location=location)
        self.assertIsNone(found)
        self.assertEqual(list(self.root.iterdir()), [])

    def test_symlinked_variables_and_state_are_refused(self):
        target = self.root / "private.json"
        target.write_text('{"secured_runtime":true}')
        self.variables.symlink_to(target)
        with self.assertRaises(ValueError):
            pairing.pairing_change(self.root, True)
        self.assertEqual(target.read_text(), '{"secured_runtime":true}')
        (self.root / "terraform.tfstate").symlink_to(target)
        with self.assertRaises(ValueError):
            pairing.applied(self.root, "proof")

    def test_applied_distinguishes_missing_off_on_and_wrong_project(self):
        self.assertIsNone(pairing.applied(self.root, "proof"))
        state = self.root / "terraform.tfstate"
        for enabled in (False, True):
            state.write_text(json.dumps(deployment_state(enabled)))
            self.assertIs(pairing.applied(self.root, "proof"), enabled)
        with self.assertRaises(ValueError):
            pairing.applied(self.root, "other")
        malformed = deployment_state(True)
        malformed["resources"][-1]["instances"][0]["attributes"]["project"] = "other"
        state.write_text(json.dumps(malformed))
        with self.assertRaises(ValueError):
            pairing.applied(self.root, "proof")
        state.write_text("{broken")
        with self.assertRaises(ValueError):
            pairing.applied(self.root, "proof")

    def test_status_reads_explicit_state_without_host_calls_or_writes(self):
        self.write_variables({"pairing_route": True, "secured_runtime": True})
        (self.root / "terraform.tfstate").write_text(json.dumps(deployment_state(False)))
        before = {p.name: p.read_bytes() for p in self.root.iterdir()}
        args = argparse.Namespace(state_dir=self.root, project="proof", remote="local")
        with patch("subprocess.run", side_effect=AssertionError("status ran a host command")), \
             patch("subprocess.check_output", side_effect=AssertionError("status queried the host")), \
             redirect_stdout(io.StringIO()) as output:
            pairing.status(args)
        self.assertIn("configured on", output.getvalue().lower())
        self.assertIn("not applied", output.getvalue().lower())
        self.assertIn("pairtask pairing on", output.getvalue())
        self.assertEqual({p.name: p.read_bytes() for p in self.root.iterdir()}, before)


class PairingRolloutAcceptance(PairingFixture):
    def setUp(self):
        super().setUp()
        (self.root / "terraform.tfstate").write_text(json.dumps(deployment_state()))
        self.write_variables({"project_name": "proof", "secured_runtime": True})
        self.image = self.root / "image"
        self.image.mkdir()
        archive = self.image / "workspace.tar.gz"
        archive.write_bytes(b"test image")
        (self.image / "image.tfvars.json").write_text(json.dumps({
            "image_file": str(archive), "image_fingerprint": hashlib.sha256(archive.read_bytes()).hexdigest()}))
        self.args = argparse.Namespace(project="proof", remote="local", image_dir=self.image,
                                       release="latest", repo="pairtask/pairtask", pairing=True,
                                       storage_pool="custom-pool", network="custom-network")
        self.server = {"architectures": ["aarch64"]}

    def run_rollout(self, operation):
        with ExitStack() as stack:
            for name in ("stop_managed", "configure_ssh", "install_tools", "refresh_recipe"):
                stack.enter_context(patch.object(rollout, name))
            stack.enter_context(patch.object(rollout, "run_locked_operation", side_effect=operation))
            stack.enter_context(patch.object(rollout.sandbox_pairing, "NameBarrier"))
            rollout.rollout(self.args, self.root, self.server)

    def test_on_upgrade_off_accepts_owned_connector_and_preserves_settings(self):
        calls = []
        def operation(args, directory, server):
            calls.append(args.action)
            if args.action == "apply":
                (directory / "terraform.tfstate").write_text(json.dumps(deployment_state(pairing.configured(directory))))
        self.run_rollout(operation)
        self.assertTrue(pairing.applied(self.root, "proof"))
        self.args.pairing = None
        self.run_rollout(operation)
        self.assertTrue(pairing.configured(self.root))
        self.assertTrue(pairing.applied(self.root, "proof"))
        self.args.pairing = False
        self.run_rollout(operation)
        self.assertFalse(pairing.configured(self.root))
        self.assertFalse(pairing.applied(self.root, "proof"))
        self.assertTrue(json.loads(self.variables.read_text())["secured_runtime"])
        self.assertEqual(calls, ["plan", "apply"] * 3)

    def test_failed_apply_keeps_configured_intent_for_retry(self):
        def operation(args, *_):
            if args.action == "apply":
                raise ValueError("apply failed")
        with self.assertRaisesRegex(ValueError, "apply failed"):
            self.run_rollout(operation)
        self.assertTrue(pairing.configured(self.root))
        self.assertFalse(pairing.applied(self.root, "proof"))

    def test_validation_happens_before_initialization_or_host_effects(self):
        self.variables.write_text('{"pairing_route":"false"}')
        with patch.object(rollout, "initialize_variables") as initialize, \
             patch.object(rollout, "stop_managed") as stop, \
             patch.object(rollout, "download_release") as download:
            with self.assertRaises(ValueError):
                rollout.rollout(self.args, self.root, self.server)
        initialize.assert_not_called()
        stop.assert_not_called()
        download.assert_not_called()

    def test_fresh_install_keeps_initialized_project_network_and_pool(self):
        (self.root / "terraform.tfstate").unlink()
        self.variables.unlink()
        with patch("subprocess.check_output", return_value=b"[]"):
            self.run_rollout(lambda *_: None)
        variables = json.loads(self.variables.read_text())
        self.assertEqual(variables["project_name"], "proof")
        self.assertEqual(variables["storage_pool"], "custom-pool")
        self.assertEqual(variables["dev_network"], "custom-network")
        self.assertIs(variables["pairing_route"], True)

    def test_only_state_owned_pairing_connector_is_admitted(self):
        state = deployment_state(True)
        path = self.root / "terraform.tfstate"
        path.write_text(json.dumps(state))
        self.assertEqual(set(rollout.managed_instances(self.root, "proof")), {"workspace", "secured", "connector"})
        for field, value in (("resource", "peer_connector"), ("project", "other"), ("role", "peer-probe")):
            changed = copy.deepcopy(state)
            resource = changed["resources"][-1]
            attrs = resource["instances"][0]["attributes"]
            if field == "resource":
                resource["name"] = value
            elif field == "project":
                attrs["project"] = value
            else:
                attrs["config"]["user.pairtask.role"] = value
            path.write_text(json.dumps(changed))
            with self.subTest(field=field), self.assertRaises(ValueError):
                rollout.managed_instances(self.root, "proof")

    def test_configuration_write_and_apply_remain_inside_deployment_lock(self):
        from sandbox_operator_state import deployment_lock
        def assert_locked(*_):
            with self.assertRaisesRegex(ValueError, "owns this deployment"):
                with deployment_lock(self.root):
                    self.fail("rollout released its lock")
        original_write = rollout.write_change
        def write(change):
            assert_locked()
            original_write(change)
        with patch.object(rollout, "write_change", side_effect=write):
            self.run_rollout(assert_locked)

    def test_invalid_image_does_not_persist_requested_setting_or_stop(self):
        (self.image / "workspace.tar.gz").write_bytes(b"corrupt")
        before = self.variables.read_bytes()
        with patch.object(rollout, "stop_managed") as stop:
            with self.assertRaisesRegex(ValueError, "checksum"):
                rollout.rollout(self.args, self.root, self.server)
        stop.assert_not_called()
        self.assertEqual(self.variables.read_bytes(), before)

    def test_stop_order_keeps_secured_last(self):
        with patch.object(rollout, "instances", return_value={name: "Running" for name in ("workspace", "connector", "secured")}), \
             patch.object(rollout.subprocess, "run") as run:
            rollout.stop_managed(self.args, ["secured", "connector", "workspace"])
        self.assertEqual([call.args[0][4] for call in run.call_args_list],
                         ["local:workspace", "local:connector", "local:secured"])


class PairingCLICommandsAcceptance(unittest.TestCase):
    def test_cli_routes_actions_and_preserves_rollout_arguments(self):
        cli = load_script("pairtask")
        flags = ["--release", "workspace-v0.0.0-build.572.1", "--state-dir", "/tmp/a directory"]
        for action in ("on", "off", "status"):
            with self.subTest(action=action):
                steps = cli.plan(["pairing", action, *flags], system="Linux")
                self.assertEqual(len(steps), 1)
                self.assertEqual(steps[0].argv, ["sandbox-provision.py", "pairing-" + action, *flags])

    def test_dry_run_never_invokes_provisioning(self):
        cli = load_script("pairtask")
        with patch.object(cli, "run") as run, redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "pairing", "on"]), 0)
        run.assert_not_called()
        self.assertIn("pairing-on", output.getvalue())

    def test_provision_parser_sets_pairing_and_allows_release(self):
        provision = load_script("sandbox-provision")
        for action, expected in (("pairing-on", True), ("pairing-off", False), ("rollout", None)):
            with self.subTest(action=action), patch.object(sys, "argv", ["sandbox-provision", action,
                    "--release", "workspace-v0.0.0-build.572.1"]):
                args = provision.arguments()
                self.assertIs(args.pairing, expected)
                self.assertEqual(args.release, "workspace-v0.0.0-build.572.1")

    def test_executable_status_missing_deployment_preserves_exit_status(self):
        program = ("import runpy, sys; sys.path.insert(0, sys.argv[1]); "
                   "import sandbox_pairing; sandbox_pairing.find_deployment=lambda *a, **k: None; "
                   "sys.argv=[sys.argv[2], 'pairing-status', '--remote', 'local']; "
                   "runpy.run_path(sys.argv[0], run_name='__main__')")
        result = subprocess.run([sys.executable, "-c", program, str(SCRIPTS),
                                 str(SCRIPTS / "sandbox-provision.py")],
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 1, result.stdout + result.stderr)
        self.assertIn("no deployment found", result.stdout.lower())
        self.assertEqual(result.stderr, "")

    def test_status_dispatch_bypasses_metadata_and_remember(self):
        provision = load_script("sandbox-provision")
        args = argparse.Namespace(action="pairing-status", state_dir=Path("/missing"), project="proof", remote="local")
        with patch.object(provision, "arguments", return_value=args), \
             patch("subprocess.check_output", side_effect=AssertionError("Incus query")), \
             patch.object(provision, "remember", side_effect=AssertionError("registry write")), \
             patch.object(pairing, "status", return_value=0) as status:
            provision.main()
        status.assert_called_once_with(args)


if __name__ == "__main__":
    unittest.main()
