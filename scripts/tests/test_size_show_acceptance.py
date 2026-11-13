"""Acceptance cases for the read-only host size report."""

import contextlib
import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))


def load_script(name, module_name):
    spec = importlib.util.spec_from_file_location(module_name, SCRIPTS / name)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


cli = load_script("pairtask.py", "pairtask_size_cli")
size_cli = load_script("sandbox-size.py", "pairtask_size_entry")


GIB = 1024 ** 3
INSTANCE = "/1.0/instances/workspace?project=pairtask"
INSTANCE_STATE = "/1.0/instances/workspace/state?project=pairtask"
VOLUMES = "/1.0/storage-pools/default/volumes/custom?project=pairtask&recursion=1"
POOL = "/1.0/storage-pools/default/resources"


def fixture(used_home=98, missing_usage=()):
    volumes = {
        "workspace-data": (10, 1),
        "agent-home": (2, used_home),
        "docker-data": (10, 22),
        "workspace-tools": (4, 40),
    }
    responses = {
        INSTANCE: {
            "expanded_config": {"limits.cpu": "2", "limits.memory": "4GiB"},
            "expanded_devices": {"root": {"type": "disk", "path": "/", "pool": "default", "size": "10GiB"}},
        },
        INSTANCE_STATE: {"disk": {"root": {"usage": 3 * GIB}}},
        VOLUMES: [{"name": name, "config": {"size": f"{size}GiB"}}
                  for name, (size, _percent) in volumes.items()],
        POOL: {"space": {"total": 38 * GIB, "used": 6 * GIB}},
    }
    for name, (size, percent) in volumes.items():
        if name not in missing_usage:
            responses[f"/1.0/storage-pools/default/volumes/custom/{name}/state?project=pairtask"] = {
                "usage": {"used": (size * GIB * percent + 99) // 100}
            }
    return responses


class SizeShowAcceptance(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.profile = Path(self.temporary.name) / ".colima" / "pairtask"

    def size_module(self):
        return __import__("sandbox_size")

    def query(self, responses):
        calls = []

        def lookup(path):
            calls.append(path)
            return responses.get(path)

        return lookup, calls

    def test_cli_routes_size_show_and_documents_it(self):
        steps = cli.plan(["size", "show", "--json"], system="Darwin")
        self.assertEqual(len(steps), 1)
        self.assertEqual(steps[0].kind, "script")
        self.assertEqual(steps[0].argv, ["sandbox-size.py", "show", "--json"])
        with contextlib.redirect_stdout(io.StringIO()) as output:
            self.assertEqual(cli.main(["--dry-run", "size", "show"]), 0)
        self.assertIn("sandbox-size.py", output.getvalue())
        with contextlib.redirect_stdout(io.StringIO()) as output:
            cli.main(["--help"])
        self.assertIn("size", output.getvalue())

    def test_live_report_shows_real_limits_pool_and_all_volume_usage(self):
        size = self.size_module()
        self.profile.mkdir(parents=True)
        (self.profile / "pairtask-owner.json").write_text(json.dumps({"schema": 1, "config": {
            "cpu": 4, "memory": 8, "disk": 40,
        }}))
        lookup, calls = self.query(fixture())
        report = size.collect(lookup, self.profile, "Darwin")
        rendered = size.render(report)
        for expected in ("4 CPU", "8 GiB", "40 GiB", "38 GiB", "2 CPU", "4 GiB", "10 GiB",
                         "workspace-data", "agent-home", "docker-data", "workspace-tools", "98%"):
            with self.subTest(expected=expected):
                self.assertIn(expected, rendered)
        self.assertIn("agent-home", " ".join(size.warnings(report)))
        self.assertIn(INSTANCE, calls)
        self.assertIn(INSTANCE_STATE, calls)
        self.assertIn(VOLUMES, calls)
        self.assertIn(POOL, calls)
        self.assertEqual(len([path for path in calls if "/volumes/custom/" in path and "/state" in path]), 4)

    def test_warning_boundary_is_at_ninety_percent(self):
        size = self.size_module()
        for percent, warns in ((89, False), (90, True), (98, True)):
            with self.subTest(percent=percent):
                lookup, _ = self.query(fixture(used_home=percent))
                report = size.collect(lookup, self.profile, "Linux")
                warning = " ".join(size.warnings(report))
                self.assertEqual("agent-home" in warning, warns)

    def test_json_report_preserves_bytes_and_warning_for_automation(self):
        size = self.size_module()
        lookup, _ = self.query(fixture())
        report = json.loads(size.as_json(size.collect(lookup, self.profile, "Linux")))
        self.assertEqual(report["workspace"]["root_bytes"], 10 * GIB)
        home = next(volume for volume in report["volumes"] if volume["name"] == "agent-home")
        self.assertEqual(home["size_bytes"], 2 * GIB)
        self.assertEqual(home["percent"], 98)
        self.assertTrue(any("agent-home" in warning for warning in report["warnings"]))

    def test_project_is_scoped_on_every_project_resource_query(self):
        size = self.size_module()
        responses = {path.replace("project=pairtask", "project=other"):
                     value for path, value in fixture().items()}
        lookup, calls = self.query(responses)
        report = size.collect(lookup, self.profile, "Linux", project="other")
        self.assertIn("agent-home", size.render(report))
        for path in calls:
            if "/instances/" in path or "/volumes/" in path:
                self.assertIn("project=other", path)

    def test_cli_json_uses_requested_project_and_returns_machine_readable_usage(self):
        responses = {path.replace("project=pairtask", "project=other"):
                     value for path, value in fixture().items()}
        lookup, calls = self.query(responses)
        with contextlib.redirect_stdout(io.StringIO()) as output:
            code = size_cli.main(["show", "--remote", "local", "--project", "other", "--json"],
                                 query=lookup, system="Linux")
        self.assertEqual(code, 0)
        report = json.loads(output.getvalue())
        self.assertEqual(report["workspace"]["cpu"], "2")
        self.assertIn("project=other", calls[0])
        self.assertTrue(any(item["name"] == "agent-home" and item["percent"] == 98
                            for item in report["volumes"]))

    def test_cli_reports_unreachable_incus_as_failure(self):
        size = self.size_module()

        def unavailable(_path):
            raise size.Unreachable("connection refused")

        with contextlib.redirect_stdout(io.StringIO()) as output, \
                contextlib.redirect_stderr(io.StringIO()) as error:
            code = size_cli.main(["show", "--remote", "colima-pairtask"],
                                 query=unavailable, system="Darwin")
        self.assertEqual(code, 1)
        self.assertEqual(output.getvalue(), "")
        self.assertIn("connection refused", error.getvalue())
        self.assertIn("colima-pairtask", error.getvalue())

    def test_missing_usage_is_unknown_not_zero_or_a_false_warning(self):
        size = self.size_module()
        lookup, _ = self.query(fixture(missing_usage=("agent-home",)))
        report = size.collect(lookup, self.profile, "Linux")
        rendered = size.render(report)
        self.assertIn("agent-home", rendered)
        self.assertNotIn("agent-home", " ".join(size.warnings(report)))
        self.assertNotIn("agent-home 2 GiB (0%)", rendered)

    def test_a_real_zero_usage_is_reported_as_zero_percent(self):
        size = self.size_module()
        responses = fixture()
        responses["/1.0/storage-pools/default/volumes/custom/agent-home/state?project=pairtask"] = {
            "usage": {"used": 0}
        }
        lookup, _ = self.query(responses)
        report = size.collect(lookup, self.profile, "Linux")
        self.assertIn("agent-home 2 GiB (0%)", size.render(report))
        self.assertNotIn("agent-home", " ".join(size.warnings(report)))

    def test_missing_resource_is_distinct_from_missing_remote_or_client(self):
        size = self.size_module()
        with mock.patch.object(size.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 1, stdout="", stderr="Error: Instance not found")):
            self.assertIsNone(size.incus_query("colima-pairtask")(INSTANCE))
        with mock.patch.object(size.subprocess, "run", return_value=subprocess.CompletedProcess(
                [], 1, stdout="", stderr="Error: remote 'missing' not found")):
            with self.assertRaises(size.Unreachable):
                size.incus_query("missing")(INSTANCE)
        with mock.patch.object(size.subprocess, "run", side_effect=FileNotFoundError("incus")):
            with self.assertRaises(size.Unreachable):
                size.incus_query("colima-pairtask")(INSTANCE)

    def test_unresponsive_incus_fails_within_a_bounded_wait(self):
        size = self.size_module()
        stalled = subprocess.TimeoutExpired(["incus", "query"], 30)
        with mock.patch.object(size.subprocess, "run", side_effect=stalled) as run:
            with self.assertRaises(size.Unreachable):
                size.incus_query("colima-pairtask")(INSTANCE)
        self.assertGreater(run.call_args.kwargs["timeout"], 0)
        self.assertLessEqual(run.call_args.kwargs["timeout"], 60)

    def test_unprovisioned_show_does_not_create_state_or_profile(self):
        size = self.size_module()
        lookup, _ = self.query({})
        with mock.patch("sandbox_operator_config.new_deployment", side_effect=AssertionError("show must not create state")):
            report = size.collect(lookup, self.profile, "Darwin")
        self.assertIn("not provisioned", size.render(report))
        self.assertFalse(self.profile.exists())


if __name__ == "__main__":
    unittest.main()
