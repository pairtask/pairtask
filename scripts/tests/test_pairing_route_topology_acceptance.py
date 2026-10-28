"""Adversarial inputs for the installed route's measured isolation checks."""

from copy import deepcopy
from pathlib import Path
import sys
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import pairing_route_checks as route


INGRESS = "/mnt/pairtask-pairing"
EGRESS = "/mnt/pairtask-egress"
ENV = "/run/pairtask/pairing.env"


def instance(network=False):
    devices = {"root": {"type": "disk", "path": "/", "pool": "default"}}
    for name, path in (("ingress", INGRESS), ("egress", EGRESS)):
        devices[name] = {"type": "disk", "path": path, "pool": "default", "source": name}
    if network:
        devices["eth0"] = {"type": "nic", "network": "route0"}
    return {"expanded_devices": devices}


class PairingRouteTopologyAcceptanceTests(unittest.TestCase):
    def setUp(self):
        self.secured = instance()
        self.connector = instance(network=True)
        self.connector["expanded_config"] = {"user.pairtask.role": "pairing-connector"}
        self.workspace = {"expanded_devices": {"root": {"type": "disk", "path": "/", "pool": "default"}}}
        self.instances = {"workspace": self.workspace, "secured": self.secured, "connector": self.connector}
        self.volumes = {INGRESS: ("default", "ingress"), EGRESS: ("default", "egress")}

    def test_only_three_expected_instances_and_route_role_are_accepted(self):
        route.check_instances(self.instances)
        for name in self.instances:
            changed = dict(self.instances)
            del changed[name]
            with self.subTest(missing=name), self.assertRaises(ValueError):
                route.check_instances(changed)
        with self.assertRaises(ValueError):
            route.check_instances(dict(self.instances, unexpected={}))
        self.connector["expanded_config"]["user.pairtask.role"] = "peer-connector"
        with self.assertRaises(ValueError):
            route.check_instances(self.instances)

    def test_secured_rejects_network_proxy_and_unmanaged_route(self):
        route.check_secured_devices(self.secured)
        for device in ({"type": "nic"}, {"type": "proxy"}):
            changed = deepcopy(self.secured)
            changed["expanded_devices"]["escape"] = device
            with self.subTest(device=device), self.assertRaises(ValueError):
                route.check_secured_devices(changed)
        for key in ("pool", "source"):
            changed = deepcopy(self.secured)
            del changed["expanded_devices"]["ingress"][key]
            with self.subTest(key=key), self.assertRaises(ValueError):
                route.check_secured_devices(changed)

    def test_connector_has_only_three_disks_and_one_nic(self):
        route.check_connector_devices(self.connector)
        for extra in ({"type": "disk", "path": "/private", "source": "/host"},
                      {"type": "proxy"}, {"type": "nic"}, {"type": "unix-char"}):
            changed = deepcopy(self.connector)
            changed["expanded_devices"]["extra"] = extra
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                route.check_connector_devices(changed)
        for missing in ("eth0", "ingress", "egress"):
            changed = deepcopy(self.connector)
            del changed["expanded_devices"][missing]
            with self.subTest(missing=missing), self.assertRaises(ValueError):
                route.check_connector_devices(changed)

    def test_workspace_rejects_shared_volumes_even_under_an_alias(self):
        route.check_workspace_devices(self.workspace, self.volumes)
        for path, source in ((INGRESS, "other"), (EGRESS, "other"),
                             ("/innocent", "ingress"), ("/other", "egress")):
            self.workspace["expanded_devices"]["leak"] = {
                "type": "disk", "pool": "default", "path": path, "source": source}
            with self.subTest(path=path, source=source), self.assertRaises(ValueError):
                route.check_workspace_devices(self.workspace, self.volumes)

    def test_sides_must_share_the_same_two_distinct_volumes(self):
        self.assertEqual(route.check_shared_volumes(self.secured, self.connector), self.volumes)
        for key, value in (("source", "other"), ("pool", "other-pool")):
            changed = deepcopy(self.connector)
            changed["expanded_devices"]["ingress"][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                route.check_shared_volumes(self.secured, changed)
        for side in (self.secured, self.connector):
            side["expanded_devices"]["egress"]["source"] = "ingress"
        with self.assertRaises(ValueError):
            route.check_shared_volumes(self.secured, self.connector)

    def test_mounts_and_actual_write_refusal_are_both_required(self):
        for role, writable, readonly in (("secured", INGRESS, EGRESS), ("connector", EGRESS, INGRESS)):
            options = {writable: {"rw", "relatime"}, readonly: {"ro", "relatime"}}
            route.check_mount_options(role, options)
            route.check_write_refused(role, {writable: False, readonly: True})
            for bad in ({writable: {"rw"}}, {writable: {"ro"}, readonly: {"rw"}}):
                with self.subTest(role=role, options=bad), self.assertRaises(ValueError):
                    route.check_mount_options(role, bad)
            for result in ({}, {readonly: False}, {readonly: "true"}):
                with self.subTest(role=role, result=result), self.assertRaises(ValueError):
                    route.check_write_refused(role, result)

    def endpoint_stats(self):
        return {INGRESS: (0o40750, 1002, 1050), INGRESS + "/pairing.sock": (0o140660, 1002, 1050),
                EGRESS: (0o40750, 1052, 1050), EGRESS + "/egress.sock": (0o140660, 1052, 1050),
                ENV: (0o100640, 0, 1002)}

    def test_every_endpoint_has_exact_private_mode_and_owner(self):
        ids = {"broker": 1002, "peer": 1052, "collab_peer": 1050, "broker_gid": 1002}
        stats = self.endpoint_stats()
        route.check_endpoint_modes(stats, ids)
        for path, row in stats.items():
            missing = dict(stats)
            del missing[path]
            with self.subTest(path=path, change="missing"), self.assertRaises(ValueError):
                route.check_endpoint_modes(missing, ids)
            for column in range(3):
                changed = dict(stats)
                altered = list(row)
                altered[column] += 1
                changed[path] = tuple(altered)
                with self.subTest(path=path, column=column), self.assertRaises(ValueError):
                    route.check_endpoint_modes(changed, ids)

    def test_each_role_runs_only_its_own_services(self):
        expected = {
            "secured": ("pairtask-pairing-setup.service", "pairtask-secured-broker.service"),
            "connector": ("pairtask-connector-setup.service", "pairtask-peer-egress.service", "pairtask-peer-relay.service"),
            "workspace": (),
        }
        all_units = set(sum(expected.values(), ())) | {"pairtask-broker.service"}
        for role, required in expected.items():
            active = {unit: "active" if unit in required else "inactive" for unit in all_units}
            route.check_units(role, active)
            for unit in all_units:
                if role == "workspace" and unit in ("pairtask-broker.service", "pairtask-secured-broker.service"):
                    continue
                changed = dict(active)
                changed[unit] = "failed" if unit in required else "active"
                with self.subTest(role=role, unit=unit), self.assertRaises(ValueError):
                    route.check_units(role, changed)

    def test_no_private_paths_or_secured_network_are_allowed(self):
        route.check_private_paths_absent([])
        for path in ("/mnt/collab-ipc/broker.sock", "/run/collab-ai-operator/operator.sock",
                     "/var/lib/collab-ai-secured/broker/broker.db", "/var/lib/incus/unix.socket"):
            with self.subTest(path=path), self.assertRaises(ValueError):
                route.check_private_paths_absent([path])
        route.check_secured_network([{"ifname": "lo"}], [], [])
        for links, v4, v6 in (([{"ifname": "lo"}, {"ifname": "eth0"}], [], []),
                              ([{"ifname": "lo"}], [{"dst": "default"}], []),
                              ([{"ifname": "lo"}], [], [{"dst": "default"}])):
            with self.subTest(links=links, v4=v4, v6=v6), self.assertRaises(ValueError):
                route.check_secured_network(links, v4, v6)

    def test_gatherer_rejects_nondisposable_projects_before_execution(self):
        with patch.object(route, "_run") as run:
            for project in ("default", "production", "", "pairtask"):
                with self.subTest(project=project), self.assertRaises(ValueError):
                    route.Route("local", project).instances()
            run.assert_not_called()

    def test_gatherer_does_not_hide_unexpected_instances(self):
        import json
        with patch.object(route, "_run", return_value=json.dumps([
                {"name": name, **item} for name, item in dict(self.instances, unexpected={}).items()])):
            found = route.Route("local", "pairtask-smoke-route-test").instances()
        self.assertIn("unexpected", found)
        with self.assertRaises(ValueError):
            route.check_instances(found)

    def test_image_source_archive_contains_the_installed_route(self):
        import hashlib
        import tarfile
        import tempfile
        from sandbox_source import source_archive

        required = ("pairtask-pairing-setup", "pairtask-pairing-setup.service",
                    "pairtask-connector-setup.service", "pairtask-peer-egress.service",
                    "pairtask-peer-relay.service", "pairtask-secured-broker.service.d/pairing.conf")
        with tempfile.TemporaryDirectory() as directory:
            archive = Path(directory) / "source.tar"
            provenance = source_archive(archive)
            with tarfile.open(archive) as source:
                for name in required:
                    path = "infra/image/" + name
                    with self.subTest(path=path):
                        self.assertTrue(path in source.getnames(), "image source is missing " + path)
                        contents = source.extractfile(path).read()
                        self.assertEqual(hashlib.sha256(contents).hexdigest(), provenance["files_sha256"][path])


if __name__ == "__main__":
    unittest.main()
