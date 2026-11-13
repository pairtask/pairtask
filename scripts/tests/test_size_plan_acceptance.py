"""Acceptance cases for safe workspace and volume resizing (#382)."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import stat
import sys
import tempfile
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))


def load(name, module_name):
    spec = importlib.util.spec_from_file_location(module_name, SCRIPTS / name)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cli = load("pairtask.py", "pairtask_size_plan_host")
size_cli = load("sandbox-size.py", "pairtask_size_plan_entry")
import sandbox_size


GIB = 1024 ** 3


def live_fixture(names=("secured", "connector"), project="pairtask"):
    root = "/1.0/storage-pools/default"
    volumes = {"workspace-data": 10, "agent-home": 2, "docker-data": 10, "workspace-tools": 4}
    responses = {
        f"/1.0/instances/workspace?project={project}": {
            "expanded_config": {"limits.cpu": "2", "limits.memory": "4GiB"},
            "expanded_devices": {"root": {"pool": "default", "size": "10GiB"}},
        },
        f"/1.0/instances/workspace/state?project={project}": {"disk": {"root": {"usage": 3 * GIB}}},
        f"{root}/volumes/custom?project={project}&recursion=1": [
            {"name": name, "config": {"size": f"{size}GiB"}} for name, size in volumes.items()
        ],
        f"{root}/resources": {"space": {"total": 38 * GIB, "used": 6 * GIB}},
        f"/1.0/instances?project={project}&recursion=1": [{"name": name} for name in ("workspace", *names)],
        "/1.0/resources": {"cpu": {"total": 4}, "memory": {"total": 8 * GIB}},
    }
    for name, total in volumes.items():
        responses[f"{root}/volumes/custom/{name}/state?project={project}"] = {
            "usage": {"used": total * GIB // 4}
        }
    for name in names:
        responses[f"/1.0/instances/{name}?project={project}"] = {
            "expanded_devices": {"root": {"pool": "default", "size": "10GiB"}}}
    return responses


class SizePlanAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.deployment = self.root / "operator"
        self.deployment.mkdir()
        self.profile = self.root / "pairtask"
        self.profile.mkdir()
        (self.profile / "pairtask-owner.json").write_text(json.dumps({
            "schema": 1, "config": {"cpu": 4, "memory": 8, "disk": 40}
        }))
        self.responses = live_fixture()
        self.run = mock.Mock(return_value=0)

    def query(self, path):
        return self.responses.get(path)

    def invoke(self, *args, query=None, system="Darwin"):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(size_cli, "profile_dir", return_value=self.profile), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = size_cli.main(list(args), query=query or self.query, system=system,
                                 run=self.run, directory=self.deployment)
        return code, out.getvalue(), err.getvalue()

    def size_file(self):
        return self.deployment / "size.auto.tfvars.json"

    def test_host_cli_routes_both_actions_and_dry_run_does_not_mutate(self):
        for command, expected in (("plan", "--agent-home"), ("apply", "--project")):
            with self.subTest(command=command):
                arguments = ["size", command, expected, "10" if command == "plan" else "pairtask"]
                step = cli.plan(arguments, system="Darwin")[0]
                self.assertEqual(step.argv, ["sandbox-size.py", command, *arguments[2:]])
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "size", "plan", "--agent-home", "10"]), 0)
        self.assertIn("sandbox-size.py", output.getvalue())
        self.assertFalse(self.size_file().exists())

    def test_valid_plan_writes_private_operator_file_then_uses_existing_plan(self):
        managed = self.deployment / "operator.auto.tfvars.json"
        managed.write_text(json.dumps({"project_name": "pairtask", "secured_runtime": True}))
        self.size_file().write_text(json.dumps({"memory_gib": 4}))
        code, output, error = self.invoke("plan", "--agent-home", "10", "--remote", "colima-pairtask")
        self.assertEqual((code, error), (0, ""))
        self.assertIn("agent_home_gib=10", output)
        self.assertEqual(json.loads(self.size_file().read_text()), {"memory_gib": 4, "agent_home_gib": 10})
        self.assertEqual(stat.S_IMODE(self.size_file().stat().st_mode), 0o600)
        self.assertEqual(json.loads(managed.read_text()), {"project_name": "pairtask", "secured_runtime": True})
        self.run.assert_called_once()
        argv = self.run.call_args.args[0]
        self.assertEqual(Path(argv[1]).name, "sandbox-provision.py")
        self.assertEqual(argv[2:], ["plan", "--project", "pairtask", "--remote", "colima-pairtask"])

    def test_every_deployed_size_refuses_to_shrink_before_writing_or_planning(self):
        requests = (("--cpu", "1"), ("--memory", "3"), ("--disk", "9"),
                    ("--workspace", "9"), ("--agent-home", "1"),
                    ("--docker", "9"), ("--tools", "3"))
        for flag, value in requests:
            with self.subTest(flag=flag):
                code, _output, error = self.invoke("plan", flag, value)
                self.assertEqual(code, 1)
                self.assertIn("shrink", error)
                self.assertFalse(self.size_file().exists())
                self.run.assert_not_called()

    def test_unreadable_live_limit_cannot_be_assumed_safe_to_resize(self):
        self.responses["/1.0/instances/workspace?project=pairtask"]["expanded_devices"]["root"].pop("size")
        code, _output, error = self.invoke("plan", "--disk", "20")
        self.assertEqual(code, 1)
        self.assertIn("current", error.lower())
        self.assertFalse(self.size_file().exists())
        self.run.assert_not_called()

    def test_shared_root_disk_limit_cannot_shrink_other_containers(self):
        for name in ("secured", "connector"):
            with self.subTest(name=name):
                self.responses = live_fixture()
                self.responses[f"/1.0/instances/{name}?project=pairtask"]["expanded_devices"]["root"]["size"] = "12GiB"
                code, _output, error = self.invoke("plan", "--disk", "11")
                self.assertEqual(code, 1)
                self.assertIn("shrink", error)
                self.assertFalse(self.size_file().exists())
                self.run.assert_not_called()

    def test_unreadable_other_root_size_cannot_be_assumed_safe(self):
        self.responses["/1.0/instances/secured?project=pairtask"]["expanded_devices"]["root"].pop("size")
        code, _output, error = self.invoke("plan", "--disk", "20")
        self.assertEqual(code, 1)
        self.assertIn("current", error.lower())
        self.assertFalse(self.size_file().exists())
        self.run.assert_not_called()

    def test_terraform_bounds_are_checked_before_any_write(self):
        for flag, value in (("--cpu", "0"), ("--cpu", "9"), ("--memory", "17"),
                            ("--disk", "1"), ("--agent-home", "101")):
            with self.subTest(flag=flag, value=value):
                code, _output, error = self.invoke("plan", flag, value)
                self.assertEqual(code, 1)
                self.assertIn("whole number", error)
                self.assertFalse(self.size_file().exists())
                self.run.assert_not_called()

    def test_cpu_and_ram_leave_room_for_secured_and_connector(self):
        for flag, value in (("--cpu", "3"), ("--memory", "5"), ("--memory", "8")):
            with self.subTest(flag=flag, value=value):
                code, _output, error = self.invoke("plan", flag, value)
                self.assertEqual(code, 1)
                self.assertIn("room", error)
                self.assertFalse(self.size_file().exists())
                self.run.assert_not_called()
        self.responses = live_fixture(names=("secured",))
        code, _output, error = self.invoke("plan", "--cpu", "3", "--memory", "5")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(self.size_file().read_text())["cpu_count"], 3)

    def test_a_disk_only_grow_can_recover_an_existing_memory_overcommit(self):
        self.responses = live_fixture(names=("secured", "connector"))
        self.responses["/1.0/instances/workspace?project=pairtask"]["expanded_config"]["limits.memory"] = "7GiB"
        code, _output, error = self.invoke("plan", "--agent-home", "10")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(json.loads(self.size_file().read_text())["agent_home_gib"], 10)

    def test_conflicting_tfvars_refuses_without_overwriting_either_file(self):
        other = self.deployment / "custom.auto.tfvars.json"
        other.write_text(json.dumps({"agent_home_gib": 8}))
        code, _output, error = self.invoke("plan", "--agent-home", "10")
        self.assertEqual(code, 1)
        self.assertIn(other.name, error)
        self.assertEqual(json.loads(other.read_text())["agent_home_gib"], 8)
        self.assertFalse(self.size_file().exists())
        self.run.assert_not_called()

    def test_unprovisioned_workspace_fails_without_a_size_file_or_plan(self):
        self.responses = {}
        code, _output, error = self.invoke("plan", "--agent-home", "10")
        self.assertEqual(code, 1)
        self.assertIn("install", error)
        self.assertFalse(self.size_file().exists())
        self.run.assert_not_called()

    def test_apply_uses_the_saved_plan_and_does_not_rewrite_sizes(self):
        self.size_file().write_text(json.dumps({"agent_home_gib": 10}))
        before = self.size_file().read_bytes()
        code, _output, error = self.invoke("apply", "--remote", "colima-pairtask")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.size_file().read_bytes(), before)
        self.run.assert_called_once()
        self.assertEqual(self.run.call_args.args[0][2:],
                         ["apply", "--project", "pairtask", "--remote", "colima-pairtask"])

    def test_a_symlinked_size_file_cannot_read_or_replace_another_file(self):
        outside = self.root / "private.json"
        outside.write_text(json.dumps({"private": "keep"}))
        self.size_file().symlink_to(outside)
        code, _output, error = self.invoke("plan", "--agent-home", "10")
        self.assertEqual(code, 1)
        self.assertIn("symlink", error.lower())
        self.assertEqual(outside.read_text(), json.dumps({"private": "keep"}))
        self.run.assert_not_called()

    def test_incus_failure_during_validation_is_clean_and_writes_nothing(self):
        def failed(path):
            if path.startswith("/1.0/instances?"):
                raise sandbox_size.Unreachable("connection refused")
            return self.query(path)

        code, output, error = self.invoke("plan", "--agent-home", "10", query=failed)
        self.assertEqual(code, 1)
        self.assertEqual(output, "")
        self.assertIn("connection refused", error)
        self.assertFalse(self.size_file().exists())
        self.run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
