"""Acceptance cases for the operator-owned macOS VM resize (#382)."""

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


vm = load("sandbox_vm_size.py", "pairtask_size_vm_logic")
host = load("sandbox-host.py", "pairtask_size_vm_host")
size_cli = load("sandbox-size.py", "pairtask_size_vm_entry")
cli = load("pairtask.py", "pairtask_size_vm_cli")


class VMSizeAcceptance(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.choice = self.root / "host" / "vm-size.json"
        self.profile = self.root / "pairtask"
        self.profile.mkdir()
        self.current = json.loads((SCRIPTS.parent / "infra/incus/colima.json").read_text())
        self.current["arch"] = "aarch64"
        (self.profile / "pairtask-owner.json").write_text(json.dumps({"schema": 1, "config": self.current}))
        (self.profile / "colima.yaml").write_text(json.dumps(self.current))
        self.online = True
        self.fail_start = False
        self.fail_pool = False
        self.pool_size = 30
        self.pool_driver = "zfs"
        self.commands = []

    def query(self, path):
        if not self.online:
            import sandbox_size
            raise sandbox_size.Unreachable("VM is stopped")
        if path == "/1.0/instances/workspace?project=pairtask":
            return {"expanded_config": {"limits.cpu": "2", "limits.memory": "4GiB"},
                    "expanded_devices": {"root": {"pool": "default", "size": "10GiB"}}}
        if path == "/1.0/instances?project=pairtask&recursion=1":
            return [{"name": name} for name in ("workspace", "secured", "connector")]
        if path == "/1.0/storage-pools/default":
            return {"name": "default", "driver": self.pool_driver, "config": {
                "source": "/var/lib/incus/disks/default.img", "size": f"{self.pool_size}GiB"}}
        if path == "/1.0/storage-pools/default/resources":
            return {"space": {"total": self.pool_size * vm.GIB, "used": 6 * vm.GIB}}
        return None

    def run_command(self, argv):
        self.commands.append(argv)
        if argv[:2] == ["colima", "stop"]:
            return self.stop_vm()
        if Path(argv[1]).name == "sandbox-host.py" and argv[2] == "apply":
            return self.start_vm()
        if argv[:3] == ["incus", "storage", "set"]:
            return self.grow_pool(argv)
        raise AssertionError(f"unexpected command: {argv}")

    def stop_vm(self):
        self.online = False
        return 0

    def start_vm(self):
        if self.fail_start:
            return 1
        self.online = True
        return 0

    def grow_pool(self, argv):
        if self.fail_pool:
            return 1
        self.pool_size = int(argv[4].removeprefix("size=").removesuffix("GiB"))
        return 0

    def invoke(self, *args):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(size_cli, "profile_dir", return_value=self.profile), \
                mock.patch("platform.machine", return_value="arm64"), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = size_cli.main(["vm", "--remote", "colima-pairtask", *args], query=self.query,
                                 system="Darwin", run=self.run_command,
                                 choice=self.choice)
        return code, out.getvalue(), err.getvalue()

    def test_operator_choice_is_private_merged_and_read_outside_installed_tree(self):
        vm.write_choice({"cpu": 8, "disk": 80}, self.choice)
        vm.write_choice({"memory": 16}, self.choice)
        self.assertEqual(vm.read_choice(self.choice), {"cpu": 8, "memory": 16, "disk": 80})
        self.assertEqual(json.loads(self.choice.read_text()),
                         {"schema": 1, "cpu": 8, "memory": 16, "disk": 80})
        self.assertEqual(stat.S_IMODE(self.choice.parent.stat().st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(self.choice.stat().st_mode), 0o600)
        installed = {"cpu": 4, "memory": 8, "disk": 40, "runtime": "incus"}
        self.assertEqual(vm.overlay(installed, vm.read_choice(self.choice)),
                         {"cpu": 8, "memory": 16, "disk": 80, "runtime": "incus"})
        self.assertEqual(installed["cpu"], 4)

    def test_malformed_foreign_or_symlinked_choice_is_refused(self):
        self.choice.parent.mkdir()
        for content in ('{broken', '[]', '{"schema": 2}',
                        '{"schema": 1, "cpu": "many"}',
                        '{"schema": 1, "disk": 10}',
                        '{"schema": 1, "provision": "untrusted"}'):
            with self.subTest(content=content):
                self.choice.write_text(content)
                with self.assertRaises(ValueError):
                    vm.read_choice(self.choice)
        outside = self.root / "outside.json"
        outside.write_text('{"private": "keep"}')
        self.choice.unlink()
        self.choice.symlink_to(outside)
        with self.assertRaises(ValueError):
            vm.read_choice(self.choice)
        with self.assertRaises(ValueError):
            vm.write_choice({"cpu": 8}, self.choice)
        self.assertEqual(outside.read_text(), '{"private": "keep"}')

    def test_host_bootstrap_uses_saved_sizes_across_installed_default_changes(self):
        config = self.root / "colima.json"
        config.write_text(json.dumps({"cpu": 4, "memory": 8, "disk": 40, "mounts": None}))
        vm.write_choice({"cpu": 8, "memory": 16, "disk": 80}, self.choice)
        with mock.patch.object(host, "CONFIG", config), \
                mock.patch.object(host.sandbox_vm_size, "read_choice", return_value=vm.read_choice(self.choice)), \
                mock.patch.object(host.platform, "machine", return_value="arm64"):
            first = host.desired_config()
            config.write_text(json.dumps({"cpu": 6, "memory": 12, "disk": 60, "mounts": None}))
            second = host.desired_config()
        self.assertEqual((first["cpu"], first["memory"], first["disk"]), (8, 16, 80))
        self.assertEqual((second["cpu"], second["memory"], second["disk"]), (8, 16, 80))
        self.assertEqual(second["arch"], "aarch64")

    def test_host_profile_accepts_operator_sizes_after_the_profile_is_rewritten(self):
        config = self.root / "colima.json"
        config.write_text(json.dumps(self.current))
        vm.write_choice({"cpu": 8, "memory": 16, "disk": 80}, self.choice)
        with mock.patch.object(host, "CONFIG", config), \
                mock.patch.object(host.sandbox_vm_size, "read_choice", return_value=vm.read_choice(self.choice)), \
                mock.patch.object(host.platform, "machine", return_value="arm64"):
            selected = host.desired_config()
            vm.rewrite_profile(self.profile, selected)
            self.assertEqual(host.profile_config(self.profile), selected)

    def test_disk_never_shrinks_and_cpu_ram_decreases_must_fit_containers(self):
        current = {"cpu": 8, "memory": 16, "disk": 80}
        needs = {"cpu": 4, "memory": 9}
        self.assertEqual(vm.validate({"cpu": 4, "memory": 9, "disk": 80}, current, needs), [])
        for request, fragment in (({"disk": 79}, "shrink"), ({"cpu": 3}, "need"),
                                  ({"memory": 8}, "need"), ({"cpu": 0}, "whole number"),
                                  ({"memory": 257}, "whole number"), ({"disk": 2049}, "whole number")):
            with self.subTest(request=request):
                self.assertIn(fragment, " ".join(vm.validate(request, current, needs)))

    def test_unreachable_incus_blocks_decreases_but_not_increases(self):
        current = {"cpu": 8, "memory": 16, "disk": 80}
        self.assertIn("can't be read", " ".join(vm.validate({"cpu": 6}, current, None)))
        self.assertIn("can't be read", " ".join(vm.validate({"memory": 12}, current, None)))
        self.assertEqual(vm.validate({"cpu": 10, "memory": 20, "disk": 100}, current, None), [])

    def test_vm_cpu_ram_increase_can_be_previewed_while_incus_is_down(self):
        self.online = False
        code, output, error = self.invoke("--memory", "12")
        self.assertEqual((code, error), (0, ""))
        self.assertIn("12 GiB RAM", output)
        self.assertEqual(self.commands, [])

    def test_malformed_pending_pool_target_is_refused(self):
        self.choice.parent.mkdir()
        for pending in ("bad", {"pool": "default"},
                        {"pool": "default", "size_gib": "many"},
                        {"pool": "../../other", "size_gib": 70}):
            with self.subTest(pending=pending):
                self.choice.write_text(json.dumps({"schema": 1, "disk": 80, "pending": pending}))
                with self.assertRaises(ValueError):
                    vm.read_pending(self.choice)

    def test_only_managed_loop_zfs_pool_is_grown_by_the_vm_disk_delta(self):
        pool = {"name": "default", "driver": "zfs", "config": {
            "source": "/var/lib/incus/disks/default.img", "size": "30GiB"}}
        self.assertEqual(vm.pool_growth(pool, 40, 80), 70)
        self.assertIsNone(vm.pool_growth(pool, 80, 80))
        self.assertIsNone(vm.pool_growth({**pool, "driver": "dir"}, 40, 80))
        self.assertIsNone(vm.pool_growth({**pool, "config": {"source": "/dev/sdb"}}, 40, 80))

    def test_host_cli_routes_vm_and_preview_writes_nothing(self):
        step = cli.plan(["size", "vm", "--memory", "12"], system="Darwin")[0]
        self.assertEqual(step.argv, ["sandbox-size.py", "vm", "--memory", "12"])
        code, output, error = self.invoke("--memory", "12")
        self.assertEqual((code, error), (0, ""))
        self.assertIn("8 GiB RAM", output)
        self.assertIn("12 GiB RAM", output)
        self.assertIn("stops the VM", output)
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())
        self.assertEqual(json.loads((self.profile / "pairtask-owner.json").read_text())["config"], self.current)

    def test_unsafe_vm_request_is_rejected_before_stopping_or_writing(self):
        for request in (("--cpu", "3"), ("--memory", "7"), ("--disk", "39")):
            with self.subTest(request=request):
                code, _output, error = self.invoke(*request, "--apply")
                self.assertEqual(code, 1)
                self.assertTrue(error)
                self.assertEqual(self.commands, [])
                self.assertFalse(self.choice.exists())
                self.assertEqual(json.loads((self.profile / "pairtask-owner.json").read_text())["config"],
                                 self.current)

    def test_profile_drift_is_refused_before_stopping_or_rewriting_it(self):
        changed = {**self.current, "runtime": "docker"}
        (self.profile / "colima.yaml").write_text(json.dumps(changed))
        code, _output, error = self.invoke("--memory", "12", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("drift", error.lower())
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())
        self.assertEqual(json.loads((self.profile / "colima.yaml").read_text()), changed)

    def test_symlinked_colima_settings_are_refused_before_stopping(self):
        outside = self.root / "outside.yaml"
        outside.write_text("private settings")
        (self.profile / "colima.yaml").unlink()
        (self.profile / "colima.yaml").symlink_to(outside)
        code, _output, error = self.invoke("--memory", "12", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("symlink", error.lower())
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())
        self.assertEqual(outside.read_text(), "private settings")

    def test_foreign_owner_marker_is_refused_before_stopping(self):
        marker = self.profile / "pairtask-owner.json"
        marker.write_text(json.dumps({"schema": 2, "config": self.current}))
        code, _output, error = self.invoke("--memory", "12", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("owner", error.lower())
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())

    def test_marker_and_settings_cannot_jointly_adopt_foreign_runtime(self):
        changed = {**self.current, "runtime": "docker"}
        (self.profile / "pairtask-owner.json").write_text(json.dumps({"schema": 1, "config": changed}))
        (self.profile / "colima.yaml").write_text(json.dumps(changed))
        code, _output, error = self.invoke("--memory", "12", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("settings", error.lower())
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())

    def test_vm_resize_refuses_a_remote_unrelated_to_the_owned_colima_profile(self):
        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(size_cli, "profile_dir", return_value=self.profile), \
                mock.patch("platform.machine", return_value="arm64"), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = size_cli.main(["vm", "--remote", "unrelated", "--disk", "80", "--apply"],
                                 query=self.query, system="Darwin", run=self.run_command, choice=self.choice)
        self.assertEqual(code, 1)
        self.assertIn("remote", err.getvalue().lower())
        self.assertEqual(self.commands, [])
        self.assertFalse(self.choice.exists())

    def test_missing_colima_is_a_clean_failure_with_a_resumable_choice(self):
        def missing_tool(argv):
            if argv[:2] == ["colima", "stop"]:
                raise FileNotFoundError("colima is not installed")
            return self.run_command(argv)

        out, err = io.StringIO(), io.StringIO()
        with mock.patch.object(size_cli, "profile_dir", return_value=self.profile), \
                mock.patch("platform.machine", return_value="arm64"), \
                contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
            code = size_cli.main(["vm", "--remote", "colima-pairtask", "--memory", "12", "--apply"],
                                 query=self.query, system="Darwin", run=missing_tool, choice=self.choice)
        self.assertEqual(code, 1)
        self.assertIn("colima is not installed", err.getvalue())
        self.assertIn("resume", err.getvalue())
        self.assertEqual(vm.read_choice(self.choice)["memory"], 12)

    def test_apply_stops_rewrites_starts_and_grows_pool_once(self):
        code, output, error = self.invoke("--disk", "80", "--apply")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.pool_size, 70)
        self.assertEqual(self.commands[0], ["colima", "stop", "pairtask"])
        self.assertEqual(Path(self.commands[1][1]).name, "sandbox-host.py")
        self.assertEqual(self.commands[2], ["incus", "storage", "set", "colima-pairtask:default", "size=70GiB"])
        self.assertEqual(vm.read_choice(self.choice)["disk"], 80)
        self.assertIsNone(vm.read_pending(self.choice))
        self.assertEqual(json.loads((self.profile / "pairtask-owner.json").read_text())["config"]["disk"], 80)
        self.assertEqual(json.loads((self.profile / "colima.yaml").read_text())["disk"], 80)

    def test_pool_failure_keeps_absolute_target_and_retry_does_not_add_again(self):
        self.fail_pool = True
        code, _output, error = self.invoke("--disk", "80", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("resume", error)
        self.assertEqual(vm.read_pending(self.choice), {"pool": "default", "size_gib": 70})
        self.assertEqual(self.pool_size, 30)
        self.fail_pool = False
        code, _output, error = self.invoke("--apply")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.pool_size, 70)
        self.assertIsNone(vm.read_pending(self.choice))
        self.assertEqual(sum(argv[:3] == ["incus", "storage", "set"] for argv in self.commands), 2)

    def test_start_failure_while_vm_is_down_can_resume_without_readding_pool_delta(self):
        self.fail_start = True
        code, _output, error = self.invoke("--disk", "80", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("resume", error)
        self.assertFalse(self.online)
        self.assertEqual(vm.read_pending(self.choice), {"pool": "default", "size_gib": 70})
        self.fail_start = False
        code, _output, error = self.invoke("--apply")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.pool_size, 70)
        self.assertIsNone(vm.read_pending(self.choice))

    def test_interrupted_profile_pair_write_can_be_repaired_on_retry(self):
        real_write = size_cli.sandbox_vm_size.write_json

        def fail_marker_once(path, value):
            if Path(path).name == "pairtask-owner.json":
                raise OSError("simulated marker write failure")
            return real_write(path, value)

        with mock.patch.object(size_cli.sandbox_vm_size, "write_json", side_effect=fail_marker_once):
            code, _output, error = self.invoke("--disk", "80", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("resume", error)
        self.assertFalse(self.online)
        self.assertEqual(json.loads((self.profile / "colima.yaml").read_text())["disk"], 80)
        self.assertEqual(json.loads((self.profile / "pairtask-owner.json").read_text())["config"]["disk"], 40)
        self.assertEqual(vm.read_pending(self.choice)["size_gib"], 70)
        code, _output, error = self.invoke("--apply")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.pool_size, 70)
        self.assertIsNone(vm.read_pending(self.choice))

    def test_new_disk_request_cannot_silently_reuse_an_older_pending_pool_target(self):
        self.fail_pool = True
        self.assertEqual(self.invoke("--disk", "80", "--apply")[0], 1)
        self.assertEqual(vm.read_pending(self.choice)["size_gib"], 70)
        self.fail_pool = False
        code, _output, error = self.invoke("--disk", "100", "--apply")
        self.assertEqual(code, 1)
        self.assertIn("pending", error.lower())
        self.assertEqual(vm.read_pending(self.choice)["size_gib"], 70)
        self.assertEqual(self.pool_size, 30)

    def test_retry_after_pool_set_does_not_add_the_disk_delta_twice(self):
        vm.write_choice({"disk": 80}, self.choice, pending={"pool": "default", "size_gib": 70})
        vm.rewrite_profile(self.profile, {**self.current, "disk": 80})
        self.pool_size = 70
        code, _output, error = self.invoke("--apply")
        self.assertEqual((code, error), (0, ""))
        self.assertEqual(self.pool_size, 70)
        self.assertFalse(any(argv[:3] == ["incus", "storage", "set"] for argv in self.commands))
        self.assertIsNone(vm.read_pending(self.choice))

    def test_non_loop_pool_growth_requires_an_explicit_operator_note(self):
        self.pool_driver = "dir"
        code, output, error = self.invoke("--disk", "80")
        self.assertEqual((code, error), (0, ""))
        self.assertIn("pool", output.lower())
        self.assertTrue("manual" in output.lower() or "not grown" in output.lower())
        self.assertEqual(self.commands, [])


if __name__ == "__main__":
    unittest.main()
