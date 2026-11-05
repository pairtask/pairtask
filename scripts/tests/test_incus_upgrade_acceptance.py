"""Incus upgrade contract: pure mocks only, never a real VM or package action."""

import copy
import contextlib
import ast
from dataclasses import asdict
import importlib.util
import io
import itertools
import json
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import Mock, patch


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
import sandbox_incus_upgrade as upgrade
import pairtask


def load_host():
    spec = importlib.util.spec_from_file_location("upgrade_acceptance_host", SCRIPTS / "sandbox-host.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


HOST = load_host()
BACKUP_SERIAL = itertools.count(1)


def observation():
    packages = {name: upgrade.Package("7.1-1", "7.5-1", upgrade.ORIGIN) for name in upgrade.PACKAGES}
    return upgrade.Observation(
        server_version="7.1", packages=packages,
        instances=[{"project": "another-project", "name": "kept", "status": "Stopped"}],
        pools=[{"name": "default", "driver": "dir", "source": "/var/lib/incus/storage-pools/default",
                "resolved": "/var/lib/incus/storage-pools/default"}], incus_root=upgrade.INCUS_DIR,
        holds=list(upgrade.PACKAGES) + ["zfsutils-linux"],
        units={name: upgrade.Unit("active", "enabled") for name in upgrade.UNITS},
        backup_free_bytes=10**12, incus_bytes=1024,
        upgrades={name: True for name in upgrade.PACKAGES})


def pins_for(obs):
    return {name: package.candidate for name, package in obs.packages.items()}


def transaction_for(pins):
    return [("Inst", name, version) for name, version in sorted(pins.items())]


def manifest_for(obs):
    pins = pins_for(obs)
    return {"schema": upgrade.MANIFEST_SCHEMA, "target": dict(upgrade.TARGET),
            "server_version": obs.server_version, "expected_server": "7.5", "pins": pins,
            "transaction": transaction_for(pins), "holds": list(obs.holds),
            "units": {name: asdict(unit) for name, unit in obs.units.items()},
            "instances": copy.deepcopy(obs.instances), "pools": copy.deepcopy(obs.pools),
            "packages": {name: asdict(package) for name, package in obs.packages.items()},
            "blockers": []}


class UpgradeBlockerTests(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.tx = transaction_for(pins_for(self.obs))

    def assert_blocked(self):
        self.assertTrue(upgrade.blockers(self.obs, self.tx))

    def test_stopped_other_project_instances_are_allowed(self):
        self.assertEqual(upgrade.blockers(self.obs, self.tx), [])

    def test_running_instance_in_another_project_blocks(self):
        self.obs.instances[0]["status"] = "Running"
        self.assert_blocked()

    def test_external_and_non_dir_pools_block(self):
        cases = (("zfs", "/var/lib/incus/disks/default.img"), ("lvm", "vg"),
                 ("btrfs", "/var/lib/incus/storage-pools/default"), ("dir", "/outside"),
                 ("dir", "/var/lib/incus-other/pool"), ("dir", "/var/lib/incus/../outside"))
        for driver, source in cases:
            with self.subTest(driver=driver, source=source):
                self.obs.pools = [{"name": "pool", "driver": driver, "source": source}]
                self.assert_blocked()

    def test_missing_versions_wrong_origin_or_downgrade_block(self):
        for field, value in (("installed", None), ("candidate", None), ("origin", "untrusted.example")):
            with self.subTest(field=field):
                self.obs = observation()
                setattr(self.obs.packages["incus"], field, value)
                self.assert_blocked()
        self.obs = observation()
        self.obs.upgrades["incus"] = False
        self.assert_blocked()

    def test_unverified_package_comparison_blocks(self):
        self.obs.upgrades["incus"] = None
        self.assert_blocked()

    def test_existing_admin_policy_low_space_and_removal_block(self):
        self.obs.policy_rc_d_exists = True
        self.assert_blocked()
        self.obs = observation()
        self.obs.backup_free_bytes = 0
        self.assert_blocked()
        self.obs = observation()
        self.tx.append(("Remv", "libdependency", "1.0"))
        self.assert_blocked()

    def test_unsupported_expected_server_blocks(self):
        self.obs.packages["incus"].candidate = "7.4-1"
        self.assert_blocked()

    def test_physical_pool_escape_or_unknown_and_symlinked_incus_root_block(self):
        for resolved in (None, "/external/pool", "/var/lib/incus-other/pool"):
            with self.subTest(resolved=resolved):
                self.obs.pools[0]["resolved"] = resolved
                self.assert_blocked()
        self.obs = observation()
        self.obs.incus_root = "/external/incus"
        self.assert_blocked()

    def test_only_explicitly_stopped_instances_pass_the_cold_backup_gate(self):
        for status in ("Frozen", "Starting", "Stopping", "Error", "", None, "missing"):
            with self.subTest(status=status):
                self.obs = observation()
                self.obs.instances[0]["status"] = status
                if status == "missing":
                    self.obs.instances[0].pop("status")
                self.assert_blocked()

    def test_unknown_or_unsupported_unit_states_block(self):
        for active, enabled in (("unknown", "enabled"), ("active", "unknown"),
                                ("activating", "enabled"), ("inactive", "masked")):
            with self.subTest(active=active, enabled=enabled):
                self.obs.units["incus.service"] = upgrade.Unit(active, enabled)
                self.assert_blocked()


class UpgradeManifestTests(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.manifest = manifest_for(self.obs)

    def test_json_round_trip_keeps_a_valid_manifest(self):
        loaded = json.loads(json.dumps(self.manifest))
        self.assertIsInstance(upgrade.validate_manifest(loaded), dict)

    def test_malformed_schema_or_foreign_target_is_rejected(self):
        invalid = (None, [], {}, {**self.manifest, "schema": True},
                   {**self.manifest, "schema": 999},
                   {**self.manifest, "target": {"profile": "another-vm", "remote": "colima-pairtask"}},
                   {**self.manifest, "target": {"profile": "pairtask", "remote": "local"}})
        for data in invalid:
            with self.subTest(data=data):
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)

    def test_package_names_versions_and_missing_fixed_packages_are_rejected(self):
        invalid = ({"../incus": "7.5-1"}, {"incus;touch": "7.5-1"},
                   {"incus": "7.5\ncommand"}, {"incus": "$(command)"})
        for pins in invalid:
            with self.subTest(pins=pins):
                data = copy.deepcopy(self.manifest)
                data["pins"].update(pins)
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)
        self.manifest["pins"].pop("incus-base")
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.validate_manifest(self.manifest)

    def test_removal_unpinned_transaction_or_blocked_manifest_is_rejected(self):
        for tx in ([("Remv", "incus", "7.1-1")], [("Inst", "unapproved", "1.0")]):
            with self.subTest(tx=tx):
                data = {**self.manifest, "transaction": tx}
                with self.assertRaises(upgrade.UpgradeError):
                    upgrade.validate_manifest(data)
        self.manifest["blockers"] = ["running instance"]
        with self.assertRaises(upgrade.UpgradeError):
            upgrade.validate_manifest(self.manifest)

    def test_private_manifest_never_overwrites_existing_file_or_symlink(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "plan.json"
            upgrade.write_manifest(path, self.manifest)
            self.assertEqual(stat.S_IMODE(path.stat().st_mode) & 0o077, 0)
            self.assertEqual(json.loads(path.read_text())["target"], upgrade.TARGET)
            original = path.read_bytes()
            with self.assertRaises((upgrade.UpgradeError, OSError)):
                upgrade.write_manifest(path, {})
            self.assertEqual(path.read_bytes(), original)
            link = Path(temp) / "link.json"
            link.symlink_to(path)
            with self.assertRaises((upgrade.UpgradeError, OSError)):
                upgrade.write_manifest(link, {})
            self.assertEqual(path.read_bytes(), original)


class UpgradePlanningTests(unittest.TestCase):
    def test_trusted_scope_starts_from_only_the_fixed_package_candidates(self):
        obs = observation()
        base = pins_for(obs)
        pins = {**base, "libdependency": "1:2.0-3"}
        tx = transaction_for(pins)
        with patch.object(upgrade, "transaction", return_value=tx) as simulate:
            actual_pins, actual_tx = upgrade.trusted_pins(Mock(), obs)
        self.assertEqual(simulate.call_args_list[0].args[1], base)
        self.assertEqual(simulate.call_args_list[-1].args[1], pins)
        self.assertEqual(actual_pins, pins)
        self.assertEqual(actual_tx, tx)

    def test_already_current_fixed_packages_remain_pinned(self):
        obs = observation()
        pins = pins_for(obs)
        tx = [("Inst", "incus", pins["incus"])]
        with patch.object(upgrade, "transaction", return_value=tx):
            actual_pins, _ = upgrade.trusted_pins(Mock(), obs)
        self.assertEqual(actual_pins, pins)

    def test_plan_records_per_package_and_daemon_wide_snapshot(self):
        obs = observation()
        pins = pins_for(obs)
        runner = Mock()
        with patch.object(upgrade, "observe", return_value=obs), \
                patch.object(upgrade, "trusted_pins", return_value=(pins, transaction_for(pins))):
            manifest = upgrade.plan(runner)
        self.assertEqual(manifest["packages"], {name: asdict(value) for name, value in obs.packages.items()})
        self.assertEqual(manifest["instances"], obs.instances)
        self.assertEqual(manifest["holds"], obs.holds)
        self.assertEqual(manifest["units"], {name: asdict(value) for name, value in obs.units.items()})
        self.assertEqual(manifest["target"], upgrade.TARGET)
        runner.assert_not_called()
        self.assertEqual(runner.mock_calls, [])

    def test_runner_targets_only_the_dedicated_profile_and_preserves_deadline(self):
        runner = upgrade.Runner()
        with patch.object(runner, "run") as run:
            runner.vm(["apt-cache", "policy", "incus"], upgrade.QUERY)
        run.assert_called_once_with(["colima", "ssh", "-p", "pairtask", "--", "sudo",
                                     "apt-cache", "policy", "incus"], upgrade.QUERY)


def creates_policy(argv):
    return argv[:2] == ["sh", "-c"] and "printf '" in argv[2] and upgrade.POLICY in argv[2]


def written_policy(argv):
    return argv[2].split("printf '", 1)[1].split("' >", 1)[0]


class RecordingRunner:
    """A simulated VM command boundary; it never invokes subprocess.run."""

    def __init__(self, obs, fail=None, interrupt=None):
        self.calls = []
        self.holds = set(obs.holds)
        self.units = copy.deepcopy(obs.units)
        self.fail = fail
        self.interrupt = interrupt
        self.server = "7.5"
        self.policy = None
        self.created_policy = None
        self.archive = "var/lib/incus/\nvar/lib/incus/database/\netc/subuid\netc/subgid\n"
        self.probe_state = None
        self.exception = None
        self.after_start_probe = None
        self.started = False
        self.hold_noop = False
        self.query_effect = None
        self.backup_override = None

    def vm(self, argv, timeout):
        argv = list(argv)
        self.calls.append(("vm", argv, timeout))
        raise_injected(self, argv)
        refused = fake_refusal(self, argv)
        if refused is not None:
            return refused
        return subprocess.CompletedProcess(argv, 0, fake_reply(self, argv), "")

    def incus(self, argv, timeout):
        self.calls.append(("incus", list(argv), timeout))
        if self.query_effect:
            self.query_effect()
        answer = {"environment": {"server_version": self.server}}
        return subprocess.CompletedProcess(argv, 0, json.dumps(answer), "")


def raise_injected(runner, argv):
    if runner.exception and runner.exception[0](argv):
        error, runner.exception = runner.exception[1], None
        raise error
    if runner.interrupt and runner.interrupt(argv):
        runner.interrupt = None
        raise KeyboardInterrupt()


def fake_refusal(runner, argv):
    if runner.fail and runner.fail(argv):
        runner.fail = None
        return subprocess.CompletedProcess(argv, 1, "", "injected failure")
    if creates_policy(argv) and runner.policy is not None:
        return subprocess.CompletedProcess(argv, 1, "", "file already exists")
    if argv == ["test", "-e", upgrade.POLICY]:
        return subprocess.CompletedProcess(argv, 0 if runner.policy is not None else 1, "", "")
    return None


def fake_reply(runner, argv):
    if creates_policy(argv):
        runner.policy = runner.created_policy = written_policy(argv)
        return ""
    handlers = {"apt-mark": fake_holds, "systemctl": fake_systemctl, "mktemp": fake_backup_directory,
                "tar": fake_tar, "cat": fake_cat, "rm": fake_remove}
    handler = handlers.get(argv[0])
    return handler(runner, argv) if handler else ""


def fake_holds(runner, argv):
    if argv[1] == "showhold":
        return "\n".join(sorted(runner.holds)) + "\n"
    if argv[1] == "hold" and runner.hold_noop:
        return ""
    actions = {"unhold": runner.holds.difference_update, "hold": runner.holds.update}
    action = actions.get(argv[1])
    if action:
        action(argv[2:])
    return ""


def fake_backup_directory(runner, argv):
    return (runner.backup_override or f"{upgrade.BACKUP_DIR}/pairtask-incus-{next(BACKUP_SERIAL):010d}") + "\n"


def fake_tar(runner, argv):
    return runner.archive if argv[:2] == ["tar", "-tzf"] else ""


def fake_cat(runner, argv):
    return (runner.policy or "") if argv == ["cat", upgrade.POLICY] else ""


def fake_remove(runner, argv):
    if argv == ["rm", "-f", upgrade.POLICY]:
        runner.policy = None
    return ""


def fake_systemctl(runner, argv):
    if argv[1] == "is-active":
        restored = runner.after_start_probe if runner.started else None
        return (restored or runner.probe_state or runner.units[argv[-1]].active) + "\n"
    if argv[1] == "is-enabled":
        return runner.units[argv[-1]].enabled + "\n"
    changes = {"start": ("active", "active"), "stop": ("active", "inactive"),
               "enable": ("enabled", "enabled"), "disable": ("enabled", "disabled")}
    for name in argv[2:]:
        fake_unit_change(runner, name, argv[1], changes.get(argv[1]))
    return ""


def fake_unit_change(runner, name, action, change):
    unit = runner.units.get(name)
    if unit is not None and change is not None:
        setattr(unit, *change)
        runner.started = runner.started or action == "start"


def installs(argv):
    return argv[:2] == ["apt-get", "install"]


def stops(argv):
    return argv[:2] == ["systemctl", "stop"]


@contextlib.contextmanager
def recorded_apply(obs, fresh=None, fail=None, interrupt=None):
    runner = RecordingRunner(obs, fail, interrupt)
    current = fresh or copy.deepcopy(obs)
    pins = pins_for(current)
    with patch.object(upgrade, "observe", return_value=current), \
            patch.object(upgrade, "trusted_pins", return_value=(pins, transaction_for(pins))) as trusted:
        yield runner, trusted


class UpgradeApplyTests(unittest.TestCase):
    def setUp(self):
        self.obs = observation()
        self.manifest = manifest_for(self.obs)

    @contextlib.contextmanager
    def harness(self, fresh=None, fail=None, interrupt=None):
        with recorded_apply(self.obs, fresh, fail, interrupt) as context:
            yield context

    def assert_no_stop_or_install(self, runner):
        self.assertFalse(any(stops(argv) or installs(argv) for _, argv, _ in runner.calls))

    def test_apply_requires_yes_before_any_command(self):
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=False)
            self.assertEqual(runner.calls, [])

    def test_foreign_manifest_cannot_run_any_command(self):
        self.manifest["target"]["profile"] = "foreign"
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            self.assertEqual(runner.calls, [])

    def test_manifest_cannot_expand_the_trusted_package_scope(self):
        self.manifest["pins"]["arbitrary-package"] = "1.0"
        self.manifest["transaction"].append(("Inst", "arbitrary-package", "1.0"))
        with self.harness() as (runner, trusted):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            trusted.assert_called()
            self.assert_no_stop_or_install(runner)

    def test_candidate_or_transaction_drift_stops_before_daemon_mutation(self):
        for field in ("pins", "transaction"):
            with self.subTest(field=field):
                self.assert_rejects_drift(field)

    def assert_rejects_drift(self, field):
        data = copy.deepcopy(self.manifest)
        if field == "pins":
            data[field]["incus"] = "7.6-1"
        else:
            data[field].append(("Conf", "incus", "7.5-1"))
        with self.harness() as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, data, yes=True)
            self.assert_no_stop_or_install(runner)

    def test_all_preconditions_are_rechecked_before_stopping(self):
        for change in ("running", "pool", "holds", "units", "policy", "installed"):
            with self.subTest(change=change):
                self.assert_rejects_observation_change(change)

    def test_instance_started_during_apt_simulation_is_rechecked_before_stop(self):
        current = copy.deepcopy(self.obs)
        pins = pins_for(current)
        runner = RecordingRunner(self.obs)

        def simulate(*_):
            current.instances[0]["status"] = "Running"
            return pins, transaction_for(pins)

        with patch.object(upgrade, "observe", side_effect=lambda _: copy.deepcopy(current)), \
                patch.object(upgrade, "trusted_pins", side_effect=simulate):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
        self.assert_no_stop_or_install(runner)

    def test_manifest_extra_commands_and_paths_are_never_executed(self):
        marker = "manifest-command-must-never-run"
        self.manifest.update(command=["touch", marker], backup_path=f"/tmp/{marker}",
                             profile=f"foreign-{marker}")
        with self.harness() as (runner, _):
            try:
                steps = upgrade.apply(runner, self.manifest, yes=True)
            except upgrade.UpgradeError:
                steps = []
        self.assertFalse(any(marker in " ".join(argv) for _, argv, _ in runner.calls))
        self.assertTrue(all(step.ok for step in steps))

    def assert_rejects_observation_change(self, change):
        fresh = copy.deepcopy(self.obs)
        mutations = {
            "running": lambda: fresh.instances[0].update(status="Running"),
            "pool": lambda: fresh.pools[0].update(driver="zfs"),
            "holds": lambda: fresh.holds.append("new-held-package"),
            "units": lambda: setattr(fresh.units["incus.socket"], "enabled", "disabled"),
            "policy": lambda: setattr(fresh, "policy_rc_d_exists", True),
            "installed": lambda: setattr(fresh.packages["incus"], "installed", "7.2-1"),
        }
        mutations[change]()
        with self.harness(fresh) as (runner, _):
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
            self.assert_no_stop_or_install(runner)

    def test_success_installs_exact_pins_and_restores_original_state(self):
        with self.harness() as (runner, _):
            steps = upgrade.apply(runner, json.loads(json.dumps(self.manifest)), yes=True)
        self.assertTrue(steps)
        self.assertTrue(all(step.ok for step in steps))
        commands = [argv for _, argv, _ in runner.calls]
        install = [argv for argv in commands if installs(argv)]
        self.assertEqual(len(install), 1)
        expected = {f"{name}={version}" for name, version in self.manifest["pins"].items()}
        self.assertEqual({arg for arg in install[0] if "=" in arg}, expected)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assertTrue(all(0 < timeout <= upgrade.TAR for _, _, timeout in runner.calls))
        self.assertTrue(any(argv[:2] == ["tar", "-tzf"] for argv in commands))
        self.assertTrue(any(upgrade.BACKUP_DIR in str(arg) for argv in commands for arg in argv))
        self.assertIsNone(runner.policy)

    def test_install_failure_still_restores_holds_and_units(self):
        with self.harness(fail=installs) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assertIsNone(runner.policy)

    def test_interruption_during_install_still_restores_holds_and_units(self):
        with self.harness(interrupt=installs) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)
        self.assertIsNone(runner.policy)

    def apply_with_failure(self, runner):
        try:
            steps = upgrade.apply(runner, self.manifest, yes=True)
        except (upgrade.UpgradeError, KeyboardInterrupt, subprocess.TimeoutExpired):
            return
        self.assertTrue(any(not step.ok for step in steps))

    def test_failed_stop_never_backs_up_or_installs(self):
        with self.harness(fail=stops) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))

    def test_backup_verification_failure_never_installs(self):
        with self.harness(fail=lambda argv: argv[:2] == ["tar", "-tzf"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)

    def test_hold_cleanup_failure_does_not_skip_unit_restoration(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.units, self.obs.units)

    def test_stop_requires_confirmed_inactive_not_unknown_or_transitioning(self):
        for state in ("unknown", "activating", "deactivating", "failed"):
            with self.subTest(state=state), self.harness() as (runner, _):
                runner.probe_state = state
                self.apply_with_failure(runner)
                self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))

    def test_archive_verification_requires_subuid_and_subgid_when_present(self):
        with self.harness() as (runner, _):
            runner.archive = "var/lib/incus/\nvar/lib/incus/database/\n"
            self.apply_with_failure(runner)
            self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))

    def test_tampered_expected_server_cannot_make_old_server_a_success(self):
        self.manifest["expected_server"] = "7.1"
        with self.harness() as (runner, _):
            runner.server = "7.1"
            self.apply_with_failure(runner)

    def test_one_failed_unit_cleanup_does_not_skip_the_other_unit(self):
        fail = lambda argv: argv == ["systemctl", "enable", "incus.socket"]
        with self.harness(fail=fail) as (runner, _):
            self.apply_with_failure(runner)
        commands = [argv for _, argv, _ in runner.calls]
        self.assertIn(["systemctl", "start", "incus.service"], commands)

    def test_originally_unheld_packages_remain_unheld(self):
        self.obs.holds = ["incus", "zfsutils-linux"]
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            upgrade.apply(runner, self.manifest, yes=True)
        self.assertEqual(runner.holds, set(self.obs.holds))

    def test_policy_changed_by_another_actor_is_retained_and_reported(self):
        with self.harness() as (runner, _):
            def change_policy(argv):
                if installs(argv):
                    runner.policy = "administrator policy\n"
                return False
            runner.fail = change_policy
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, "administrator policy\n")
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_failed_policy_removal_still_restores_holds_and_units(self):
        with self.harness(fail=lambda argv: argv == ["rm", "-f", upgrade.POLICY]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, runner.created_policy)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_install_timeout_still_cleans_up_policy_holds_and_units(self):
        with self.harness() as (runner, _):
            runner.exception = (installs, subprocess.TimeoutExpired("apt-get", upgrade.APT))
            self.apply_with_failure(runner)
        self.assertIsNone(runner.policy)
        self.assertEqual(runner.holds, set(self.obs.holds))
        self.assertEqual(runner.units, self.obs.units)

    def test_backup_interruption_restores_units_without_installing(self):
        with self.harness(interrupt=lambda argv: argv[:1] == ["tar"]) as (runner, _):
            self.apply_with_failure(runner)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertIsNone(runner.policy)
        self.assertEqual(runner.units, self.obs.units)

    def test_partial_policy_creation_failure_cleans_up_our_file(self):
        with self.harness() as (runner, _):
            def fail_after_creation(argv):
                if creates_policy(argv):
                    runner.policy = runner.created_policy = written_policy(argv)
                    return True
                return False
            runner.fail = fail_after_creation
            self.apply_with_failure(runner)
        self.assertIsNone(runner.policy)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)

    def test_admin_standard_policy_created_after_observation_is_never_deleted(self):
        admin_body = "#!/bin/sh\nexit 101\n"
        with self.harness() as (runner, _):
            def admin_wins_race(argv):
                if creates_policy(argv):
                    runner.policy = admin_body
                return False
            runner.fail = admin_wins_race
            self.apply_with_failure(runner)
        self.assertEqual(runner.policy, admin_body)
        self.assertFalse(any(installs(argv) for _, argv, _ in runner.calls))
        self.assertEqual(runner.units, self.obs.units)

    def test_successful_start_command_still_requires_confirmed_restored_state(self):
        with self.harness() as (runner, _):
            runner.after_start_probe = "failed"
            self.apply_with_failure(runner)

    def test_cleanup_failure_is_a_structured_error_before_version_query(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))
        self.assertFalse(any(scope == "incus" for scope, _, _ in runner.calls))

    def test_primary_error_and_cleanup_failure_are_both_retained(self):
        with self.harness(fail=lambda argv: argv[:2] == ["apt-mark", "hold"]) as (runner, _):
            runner.exception = (installs, upgrade.UpgradeError("primary-install-failure"))
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertIn("primary-install-failure", str(caught.exception))
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))

    def test_socket_activated_service_returns_to_its_original_inactive_state(self):
        self.obs.units["incus.service"] = upgrade.Unit("inactive", "disabled")
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            runner.query_effect = lambda: setattr(runner.units["incus.service"], "active", "active")
            upgrade.apply(runner, self.manifest, yes=True)
        self.assertEqual(runner.units, self.obs.units)

    def test_hold_restoration_is_verified_after_a_successful_command(self):
        with self.harness() as (runner, _):
            runner.hold_noop = True
            with self.assertRaises(upgrade.UpgradeError) as caught:
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertTrue(any(not step.ok and step.name == "restore holds" for step in caught.exception.steps))

    def test_an_extra_incus_hold_is_removed_to_restore_the_original_set(self):
        self.obs.holds = ["incus", "zfsutils-linux"]
        self.manifest = manifest_for(self.obs)
        with self.harness() as (runner, _):
            def add_hold(argv):
                if installs(argv):
                    runner.holds.add("incus-base")
                return False
            runner.fail = add_hold
            try:
                upgrade.apply(runner, self.manifest, yes=True)
            except upgrade.UpgradeError as error:
                self.fail(f"Available cleanup did not restore the original holds: {error}")
        self.assertEqual(runner.holds, set(self.obs.holds))

    def test_backup_destinations_are_unique_even_at_the_same_clock_second(self):
        paths = []
        with patch.object(upgrade, "datetime", create=True) as clock:
            clock.now.return_value.strftime.return_value = "20261105T090000Z"
            for _ in range(2):
                with self.harness() as (runner, _):
                    upgrade.apply(runner, self.manifest, yes=True)
                paths.extend(argv[argv.index("-czpf") + 1] for _, argv, _ in runner.calls if "-czpf" in argv)
        self.assertEqual(len(paths), 2)
        self.assertNotEqual(paths[0], paths[1])
        self.assertTrue(all(path.startswith(upgrade.BACKUP_DIR + "/") for path in paths))

    def test_untrusted_backup_reservation_path_is_rejected_before_tar(self):
        with self.harness() as (runner, _):
            runner.backup_override = "/tmp/foreign-directory"
            with self.assertRaises(upgrade.UpgradeError):
                upgrade.apply(runner, self.manifest, yes=True)
        self.assertFalse(any(argv[:1] == ["tar"] or installs(argv) for _, argv, _ in runner.calls))


class UpgradeReadBoundaryTests(unittest.TestCase):
    def test_transaction_parses_upgrades_dependencies_configures_and_removals(self):
        text = ("Reading package lists...\nInst incus [7.1-1] (7.5-1 Zabbly:stable [amd64])\n"
                "Inst libdependency (1:2.0-3 Zabbly:stable [amd64])\n"
                "Conf incus (7.5-1 Zabbly:stable [amd64])\nRemv legacy [1.0-1]\n")
        runner = Mock()
        runner.vm.return_value = subprocess.CompletedProcess([], 0, text, "")
        actual = upgrade.transaction(runner, {"incus": "7.5-1"})
        self.assertEqual(actual, [("Inst", "incus", "7.5-1"), ("Inst", "libdependency", "1:2.0-3"),
                                  ("Conf", "incus", "7.5-1"), ("Remv", "legacy", "1.0-1")])
        argv, timeout = runner.vm.call_args.args
        self.assertEqual(argv[:3], ["apt-get", "-s", "install"])
        self.assertIn("incus=7.5-1", argv)
        self.assertGreater(timeout, 0)
        self.assertLessEqual(timeout, upgrade.APT)

    def test_debian_epoch_and_package_revision_are_separate_from_server_version(self):
        for package, server in (("7.5-1~ubuntu24.04", "7.5"), ("1:7.0.2-20261105", "7.0.2")):
            with self.subTest(package=package):
                self.assertEqual(upgrade.expected_server(package), server)

    def test_dpkg_comparison_does_not_sort_package_versions_in_python(self):
        runner = Mock()
        runner.vm.return_value = subprocess.CompletedProcess([], 0, "", "")
        self.assertTrue(upgrade.compare(runner, upgrade.Package("7.9-1", "7.10-1", upgrade.ORIGIN)))
        runner.vm.assert_called_once_with(["dpkg", "--compare-versions", "7.10-1", "ge", "7.9-1"], upgrade.QUERY)

    def test_apt_policy_uses_the_candidate_origin_not_an_older_table_entry(self):
        text = ("incus:\n  Installed: 7.1-1\n  Candidate: 7.5-1\n  Version table:\n"
                "     7.5-1 500\n        500 https://pkgs.zabbly.com/incus/stable noble/main amd64 Packages\n"
                " *** 7.1-1 100\n        100 /var/lib/dpkg/status\n")
        package = upgrade.parse_policy(text)["incus"]
        self.assertEqual(asdict(package), {"installed": "7.1-1", "candidate": "7.5-1", "origin": upgrade.ORIGIN})

    def test_observe_reads_actual_all_project_scope_without_mutating_the_vm(self):
        runner = Mock()
        runner.vm.side_effect = self.vm_read
        runner.incus.side_effect = self.incus_read
        obs = upgrade.observe(runner)
        self.assertEqual(obs.server_version, "7.1")
        self.assertEqual(obs.instances[0]["project"], "another-project")
        self.assertEqual(obs.pools[0]["driver"], "dir")
        self.assertFalse(obs.policy_rc_d_exists)
        self.assertEqual(obs.backup_free_bytes, 10**12)
        self.assertTrue(all(value is True for value in obs.upgrades.values()))
        queries = [call.args[0] for call in runner.incus.call_args_list]
        self.assertTrue(any("--all-projects" in argv for argv in queries))
        self.assertTrue(all("colima-pairtask:" in " ".join(argv) for argv in queries))

    def vm_read(self, argv, timeout):
        self.assertGreater(timeout, 0)
        if argv[:2] == ["apt-cache", "policy"]:
            text = "".join(f"{name}:\n  Installed: 7.1-1\n  Candidate: 7.5-1\n  Version table:\n"
                           "     7.5-1 500\n        500 https://pkgs.zabbly.com/incus/stable noble/main amd64 Packages\n"
                           for name in upgrade.PACKAGES)
            return subprocess.CompletedProcess(argv, 0, text, "")
        answers = {"apt-mark": "incus\nzfsutils-linux\n", "systemctl": "active\n",
                   "df": f"Avail\n{10**12}\n", "du": "1024 /var/lib/incus\n", "dpkg": ""}
        if argv == ["test", "-e", upgrade.POLICY]:
            return subprocess.CompletedProcess(argv, 1, "", "")
        if argv[:3] == ["realpath", "-e", "--"]:
            return subprocess.CompletedProcess(argv, 0, argv[-1] + "\n", "")
        self.assertIn(argv[0], answers, f"unexpected VM command in observation: {argv}")
        return subprocess.CompletedProcess(argv, 0, answers[argv[0]], "")

    def incus_read(self, argv, timeout):
        if argv[0] == "query":
            data = {"environment": {"server_version": "7.1"}}
        elif argv[0] == "list":
            data = observation().instances
        else:
            self.assertEqual(argv[:2], ["storage", "list"])
            data = [{"name": "default", "driver": "dir", "config": {"source": "/var/lib/incus/storage-pools/default"}}]
        return subprocess.CompletedProcess(argv, 0, json.dumps(data), "")

    def test_observe_resolves_a_configured_pool_path_before_accepting_it(self):
        runner = Mock()

        def read_with_escape(argv, timeout):
            if argv[:3] == ["realpath", "-e", "--"] and argv[-1].endswith("/storage-pools/default"):
                return subprocess.CompletedProcess(argv, 0, "/external/pool\n", "")
            return self.vm_read(argv, timeout)

        runner.vm.side_effect = read_with_escape
        runner.incus.side_effect = self.incus_read
        obs = upgrade.observe(runner)
        self.assertEqual(obs.pools[0]["resolved"], "/external/pool")
        self.assertTrue(upgrade.blockers(obs, transaction_for(pins_for(obs))))


class UpgradeCommandTests(unittest.TestCase):
    def setUp(self):
        fixtures = (patch.object(HOST, "check_remote"), patch.object(HOST.Path, "is_socket", return_value=True),
                    patch.object(HOST.shutil, "which", return_value="/mock/tool"),
                    patch.dict(HOST.os.environ, {}, clear=True),
                    patch.object(HOST.subprocess, "run", side_effect=AssertionError("unexpected real command")))
        for fixture in fixtures:
            fixture.start()
            self.addCleanup(fixture.stop)

    def test_upgrade_scripts_remain_python39_syntax(self):
        for filename in ("sandbox_incus_upgrade.py", "sandbox-host.py", "pairtask.py"):
            with self.subTest(filename=filename):
                ast.parse((SCRIPTS / filename).read_text(), feature_version=(3, 9))

    def test_vm_commands_preserve_manifest_and_yes_flags(self):
        for action in ("incus-plan", "incus-apply"):
            with self.subTest(action=action):
                steps = pairtask.plan(["vm", action, "--manifest", "plan.json", "--yes"], system="Darwin")
                self.assertEqual(steps, [pairtask.Step("script", ["sandbox-host.py", action,
                                                                  "--manifest", "plan.json", "--yes"])])

    def test_linux_local_is_guidance_only(self):
        argv = ["sandbox-host.py", "incus-plan", "--manifest", "unused.json"]
        with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Linux"), \
                patch.object(upgrade, "plan") as plan:
            with self.assertRaisesRegex(ValueError, "Linux.*host"):
                HOST.main()
            plan.assert_not_called()

    def test_plan_explains_supported_dir_only_scope_without_apply(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            argv = ["sandbox-host.py", "incus-plan", "--manifest", str(path)]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "plan", return_value=manifest_for(observation())), \
                    patch.object(upgrade, "apply") as apply, contextlib.redirect_stdout(output):
                HOST.main()
            apply.assert_not_called()
            self.assertTrue(path.exists())
            self.assertIn("dir storage", output.getvalue())
            self.assertIn("manual upgrade", output.getvalue())

    def test_failure_reports_steps_backup_location_and_restore_guidance(self):
        backup = "/var/backups/pairtask-incus-0123456789/incus.tar.gz"
        steps = [upgrade.Step("backup readable", True, backup), upgrade.Step("install", False, "failed"),
                 upgrade.Step("restore units", False, "failed to start")]
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            path.write_text(json.dumps(manifest_for(observation())))
            argv = ["sandbox-host.py", "incus-apply", "--manifest", str(path), "--yes"]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "apply", side_effect=upgrade.UpgradeError("install failed", steps)), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                with self.assertRaises(ValueError):
                    HOST.main()
            self.assertIn(backup, output.getvalue())
            self.assertIn("FAIL", output.getvalue())
            self.assertIn("restore", output.getvalue().lower())
            self.assertIn("docs/sandbox.md", output.getvalue())

    def test_foreign_remote_is_rejected_before_plan_or_apply(self):
        for action in ("incus-plan", "incus-apply"):
            with self.subTest(action=action):
                self.assert_identity_refused(action)

    def assert_identity_refused(self, action):
        manifest = manifest_for(observation())
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            if action == "incus-apply":
                path.write_text(json.dumps(manifest))
            argv = ["sandbox-host.py", action, "--manifest", str(path), "--yes"]
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(HOST, "check_remote", side_effect=ValueError("foreign remote")), \
                    patch.object(upgrade, "plan", return_value=manifest) as plan, \
                    patch.object(upgrade, "apply", return_value=[]) as apply, contextlib.redirect_stdout(io.StringIO()):
                with self.assertRaisesRegex(ValueError, "foreign remote"):
                    HOST.main()
                plan.assert_not_called()
                apply.assert_not_called()

    def test_foreign_colima_home_is_rejected_before_plan(self):
        argv = ["sandbox-host.py", "incus-plan", "--manifest", "unused.json"]
        with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                patch.dict(HOST.os.environ, {"COLIMA_HOME": "/foreign/profile"}), \
                patch.object(upgrade, "plan", return_value=manifest_for(observation())) as plan, \
                patch.object(upgrade, "write_manifest"), contextlib.redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(ValueError, "COLIMA_HOME"):
                HOST.main()
            plan.assert_not_called()

    def test_command_boundary_exceptions_keep_steps_backup_and_nonzero_outcome(self):
        errors = (subprocess.TimeoutExpired("apt-get", upgrade.APT), OSError("install boundary failed"), KeyboardInterrupt())
        for error in errors:
            with self.subTest(error=type(error).__name__):
                self.assert_boundary_error_reported(error)

    def assert_boundary_error_reported(self, error):
        obs = observation()
        with tempfile.TemporaryDirectory() as temp, recorded_apply(obs) as (runner, _):
            path = Path(temp) / "manifest.json"
            path.write_text(json.dumps(manifest_for(obs)))
            runner.exception = (installs, error)
            argv = ["sandbox-host.py", "incus-apply", "--manifest", str(path), "--yes"]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "Runner", return_value=runner), \
                    contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                with self.assertRaises((upgrade.UpgradeError, SystemExit, subprocess.SubprocessError, OSError, KeyboardInterrupt)) as caught:
                    HOST.main()
            self.assert_structured_outcome(caught.exception, error)
            self.assertIn(upgrade.BACKUP_DIR + "/", output.getvalue())
            self.assertIn("docs/sandbox.md", output.getvalue())
            self.assertEqual(runner.holds, set(obs.holds))
            self.assertEqual(runner.units, obs.units)
            self.assertIsNone(runner.policy)

    def assert_structured_outcome(self, caught, original):
        if isinstance(original, KeyboardInterrupt):
            self.assertIsInstance(caught, SystemExit)
            self.assertEqual(caught.code, 130)
        else:
            self.assertIsInstance(caught, upgrade.UpgradeError)
            self.assertTrue(caught.steps)
            self.assertIn(type(original).__name__, str(caught))

    def test_plan_keeps_extra_manifest_metadata_in_the_private_file(self):
        manifest = manifest_for(observation())
        manifest["metadata_note"] = "not-for-the-summary"
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp) / "manifest.json"
            argv = ["sandbox-host.py", "incus-plan", "--manifest", str(path)]
            output = io.StringIO()
            with patch.object(HOST.sys, "argv", argv), patch.object(HOST.platform, "system", return_value="Darwin"), \
                    patch.object(upgrade, "plan", return_value=manifest), contextlib.redirect_stdout(output):
                HOST.main()
            self.assertIn("not-for-the-summary", path.read_text())
            self.assertNotIn("not-for-the-summary", output.getvalue())


if __name__ == "__main__":
    unittest.main()
