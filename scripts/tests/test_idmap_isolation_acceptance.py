"""Independent acceptance cases for measured Incus UID/GID separation (#183)."""

from contextlib import ExitStack, redirect_stderr, redirect_stdout
import importlib
import io
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "scripts"))
import peer_probe_checks as peer
import sandbox_secured_checks as secured


def interval(host, size=100, ns=0, uid=True, gid=True):
    return {"Hostid": host, "Nsid": ns, "Maprange": size, "Isuid": uid, "Isgid": gid}


def instance(role, mappings):
    return {"name": role, "expanded_config": {
        "security.privileged": "false", "security.idmap.isolated": "true",
        "volatile.idmap.current": json.dumps(mappings),
    }, "expanded_devices": {name: {} for name in (
        "root", "secured-state", "budget-status", "dev-executor", "broker-ipc", "secured-tools")}}


def topology():
    return {role: instance(role, [interval(host)]) for role, host in (
        ("workspace", 100000), ("secured", 200000), ("connector", 300000))}


def replace_map(instances, role, mappings):
    instances[role]["expanded_config"]["volatile.idmap.current"] = json.dumps(mappings)


def check(instances):
    importlib.import_module("sandbox_idmap_checks").check_disjoint(instances)


class IDMapIsolationAcceptanceTests(unittest.TestCase):
    def assert_refused(self, instances):
        with self.assertRaises(ValueError):
            check(instances)

    def test_disjoint_and_adjacent_ranges_pass(self):
        instances = topology()
        check(instances)
        replace_map(instances, "secured", [interval(100100)])
        check(instances)

    def test_equal_partial_containing_and_single_id_overlaps_fail(self):
        for host, size in ((100000, 100), (100050, 100), (99950, 200), (100099, 1)):
            with self.subTest(host=host, size=size):
                instances = topology()
                replace_map(instances, "secured", [interval(host, size, ns=500)])
                self.assert_refused(instances)

    def test_split_and_reordered_records_cannot_hide_overlap(self):
        instances = topology()
        replace_map(instances, "secured", [interval(100050, 50, 50), interval(100000, 50)])
        self.assert_refused(instances)

    def test_uid_and_gid_overlap_are_each_rejected(self):
        for uid, gid in ((True, False), (False, True)):
            with self.subTest(uid=uid):
                instances = topology()
                replace_map(instances, "secured", [
                    interval(100050, uid=uid, gid=gid),
                    interval(200000, uid=gid, gid=uid),
                ])
                self.assert_refused(instances)

    def test_uid_numbers_do_not_collide_with_gid_numbers(self):
        instances = topology()
        replace_map(instances, "workspace", [
            interval(100000, gid=False), interval(200000, uid=False)])
        replace_map(instances, "secured", [
            interval(200000, gid=False), interval(100000, uid=False)])
        check(instances)

    def test_host_share_exceptions_outside_isolated_range_are_valid(self):
        instances = topology()
        replace_map(instances, "workspace", [
            interval(100000, 1001), interval(1001, 1, 1001),
            interval(101002, 100, 1002),
        ])
        check(instances)

    def test_host_share_exception_must_not_intersect_another_role(self):
        instances = topology()
        replace_map(instances, "workspace", [interval(100000), interval(200001, 1, 1001)])
        self.assert_refused(instances)

    def test_every_role_pair_is_checked(self):
        instances = topology()
        replace_map(instances, "connector", [interval(200050)])
        self.assert_refused(instances)

    def test_missing_empty_or_malformed_map_is_not_evidence(self):
        for raw in (None, "", "not-json", "null", "{}", "[]", "[null]", '[{"Hostid": 100000}]'):
            with self.subTest(raw=raw):
                instances = topology()
                config = instances["secured"]["expanded_config"]
                config["volatile.idmap.current"] = raw
                self.assert_refused(instances)
        instances = topology()
        del instances["secured"]["expanded_config"]["volatile.idmap.current"]
        self.assert_refused(instances)

    def test_invalid_range_values_fail_closed(self):
        cases = (("Hostid", -1), ("Nsid", -1), ("Maprange", 0), ("Maprange", -1),
                 ("Hostid", True), ("Maprange", "100"), ("Nsid", 0.5), ("Isuid", "false"))
        for field, value in cases:
            with self.subTest(field=field, value=value):
                instances = topology()
                entry = interval(200000)
                entry[field] = value
                replace_map(instances, "secured", [entry])
                self.assert_refused(instances)

    def test_both_uid_and_gid_evidence_are_required(self):
        for uid, gid in ((True, False), (False, True), (False, False)):
            with self.subTest(uid=uid, gid=gid):
                instances = topology()
                replace_map(instances, "secured", [interval(200000, uid=uid, gid=gid)])
                self.assert_refused(instances)

    def test_entry_without_either_id_family_is_not_silently_ignored(self):
        instances = topology()
        replace_map(instances, "secured", [
            interval(200000), interval(100050, uid=False, gid=False)])
        self.assert_refused(instances)

    def test_privileged_or_nonisolated_instance_fails(self):
        for key, value in (("security.privileged", "true"), ("security.idmap.isolated", "false")):
            with self.subTest(key=key):
                instances = topology()
                instances["secured"]["expanded_config"][key] = value
                self.assert_refused(instances)

    def test_failure_retains_role_maps_without_unrelated_config(self):
        instances = topology()
        replace_map(instances, "secured", [interval(100050)])
        instances["workspace"]["expanded_config"]["user.private"] = "DO-NOT-LOG-THIS"
        captured = io.StringIO()
        with redirect_stdout(captured), redirect_stderr(captured):
            with self.assertRaises(ValueError) as raised:
                check(instances)
        diagnostic = captured.getvalue() + str(raised.exception)
        for expected in ("workspace", "secured", "connector", "Hostid", "100000", "100050", "300000"):
            self.assertIn(expected, diagnostic)
        self.assertNotIn("DO-NOT-LOG-THIS", diagnostic)


class IDMapGateIntegrationAcceptanceTests(unittest.TestCase):
    def test_peer_probe_rejects_different_records_with_overlapping_ranges(self):
        instances = topology()
        replace_map(instances, "connector", [interval(200050)])
        with ExitStack() as stack:
            stack.enter_context(patch.object(peer.Probe, "instances", return_value=instances))
            stack.enter_context(patch.object(peer, "_control_network"))
            stack.enter_context(patch.object(peer, "_control_config"))
            with self.assertRaises(ValueError):
                peer.check_topology("local", "pairtask-peer-probe-test")

    def test_regular_secured_smoke_rejects_overlapping_ranges(self):
        instances = topology()
        del instances["connector"]
        replace_map(instances, "secured", [interval(100050)])
        deployment = SimpleNamespace(base=["incus"], remote="local:", execute=Mock())
        with ExitStack() as stack:
            stack.enter_context(patch.object(secured, "check_volume_permissions"))
            stack.enter_context(patch.object(secured, "check_offline"))
            stack.enter_context(patch.object(secured, "denied"))
            stack.enter_context(patch.object(secured, "run", return_value=json.dumps(list(instances.values()))))
            with self.assertRaises(ValueError):
                secured.check_configuration(deployment)
