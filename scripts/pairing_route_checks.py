"""Checks for the installed pairing route (#178): secured, connector and
workspace as deployed with pairing_route. The check_* functions take data
already gathered and raise ValueError on any boundary that doesn't hold;
Route gathers that data from a disposable project with incus exec."""

from dataclasses import dataclass
import json
import subprocess

import sandbox_idmap_checks


INGRESS = "/mnt/pairtask-pairing"
EGRESS = "/mnt/pairtask-egress"
ROLES = ("workspace", "secured", "connector")
ENV = "/run/pairtask/pairing.env"
PAIRING_SOCKET = INGRESS + "/pairing.sock"
EGRESS_SOCKET = EGRESS + "/egress.sock"
CONNECTOR_ROLE_TAG = "pairing-connector"
# Disposable projects use the smoke prefix, which the shared teardown and
# network helpers require.
PROJECT_PREFIX = "pairtask-smoke-"

# Which route mount each role may write; the other is read-only there.
WRITABLE = {"secured": INGRESS, "connector": EGRESS}
ROUTE_UNITS = ("pairtask-pairing-setup.service", "pairtask-connector-setup.service",
               "pairtask-peer-egress.service", "pairtask-peer-relay.service")
BROKER_UNITS = ("pairtask-broker.service", "pairtask-secured-broker.service")
# Per role, the units that must be active (True) or must not be (False).
# Workspace's own standalone broker is outside the route and not checked.
EXPECTED_UNITS = {
    "secured": {"pairtask-pairing-setup.service": True, "pairtask-secured-broker.service": True,
                "pairtask-connector-setup.service": False, "pairtask-peer-egress.service": False,
                "pairtask-peer-relay.service": False, "pairtask-broker.service": False},
    "connector": {"pairtask-connector-setup.service": True, "pairtask-peer-egress.service": True,
                  "pairtask-peer-relay.service": True, "pairtask-pairing-setup.service": False,
                  "pairtask-broker.service": False, "pairtask-secured-broker.service": False},
    "workspace": {unit: False for unit in ROUTE_UNITS},
}
PRIVATE_PATHS = ("/mnt/collab-ipc/broker.sock", "/run/collab-ai-operator/operator.sock",
                 "/var/lib/collab-ai-secured/broker/broker.db", "/var/lib/collab-ai-secured/broker/broker.db.peer",
                 "/var/lib/collab-ai-secured", "/tmp/collab-ai.sock", "/var/lib/incus/unix.socket",
                 "/dev/incus/sock", "/var/run/docker.sock")


def require(condition, message):
    if not condition:
        raise ValueError(message)


def check_instances(instances):
    require(set(instances) == set(ROLES), f"Expected exactly {sorted(ROLES)}, found {sorted(instances)}")
    config = instances["connector"].get("expanded_config") or {}
    require(config.get("user.pairtask.role") == CONNECTOR_ROLE_TAG, "The connector is not the pairing route's")


def _devices(instance):
    return list((instance.get("expanded_devices") or {}).values())


def _disk_paths(instance):
    return sorted(d.get("path") for d in _devices(instance) if d.get("type") == "disk")


def _route_disks(instance):
    return {d.get("path"): d for d in _devices(instance) if d.get("type") == "disk" and d.get("path") in (INGRESS, EGRESS)}


def check_secured_devices(instance):
    require(not any(d.get("type") in ("nic", "proxy") for d in _devices(instance)), "Secured acquired a NIC or proxy device")
    disks = _route_disks(instance)
    require(set(disks) == {INGRESS, EGRESS}, "Secured lacks a route mount")
    require(all(d.get("pool") and d.get("source") for d in disks.values()), "Secured's route mounts are not managed volumes")


def check_connector_devices(instance):
    devices = _devices(instance)
    require(_disk_paths(instance) == sorted(["/", INGRESS, EGRESS]), "Connector has a disk beyond root and the route")
    require(all(d.get("type") in ("disk", "nic") for d in devices), "Connector has an unexpected device")
    require(sum(d.get("type") == "nic" for d in devices) == 1, "Connector needs exactly one NIC")
    require(all(d.get("pool") and d.get("source") for d in _route_disks(instance).values()),
            "Connector's route mounts are not managed volumes")


def check_workspace_devices(instance, volumes):
    for device in _devices(instance):
        path = device.get("path", "")
        require(not path.startswith((INGRESS, EGRESS)), "Workspace exposes a route path")
        require((device.get("pool"), device.get("source")) not in set(volumes.values()),
                "Workspace exposes a route volume under another path")


def check_shared_volumes(secured, connector):
    sides = [{p: (d.get("pool"), d.get("source")) for p, d in _route_disks(i).items()} for i in (secured, connector)]
    require(sides[0] == sides[1], "Secured and connector mount different route volumes")
    require(len(set(sides[0].values())) == 2, "Both route paths share one volume")
    return sides[0]


def check_mount_options(role, options):
    require(set(options) == {INGRESS, EGRESS}, f"{role} lacks a measured route mount")
    for path, flags in options.items():
        want = "rw" if WRITABLE[role] == path else "ro"
        require(want in flags, f"{role} mounts {path} without {want}")


def check_write_refused(role, results):
    readonly = INGRESS if WRITABLE[role] == EGRESS else EGRESS
    require(results.get(readonly) is True, f"{role} could write to its read-only {readonly}")


def check_endpoint_modes(stats, ids):
    peer_group = ids["collab_peer"]
    expected = {
        INGRESS: (0o750, ids["broker"], peer_group),
        PAIRING_SOCKET: (0o660, ids["broker"], peer_group),
        EGRESS: (0o750, ids["peer"], peer_group),
        EGRESS_SOCKET: (0o660, ids["peer"], peer_group),
        ENV: (0o640, 0, ids["broker_gid"]),
    }
    for path, want in expected.items():
        require(path in stats, f"{path} is missing")
        mode, uid, gid = stats[path]
        require((mode & 0o7777, uid, gid) == want, f"{path} is {oct(mode & 0o7777)} {uid}:{gid}, want {oct(want[0])} {want[1]}:{want[2]}")


def check_units(role, active):
    for unit, expected in EXPECTED_UNITS[role].items():
        state = active.get(unit, "inactive")
        require((state == "active") == expected, f"{role}: {unit} is {state}, want {'active' if expected else 'not active'}")


def check_private_paths_absent(exposed):
    require(not exposed, f"Connector exposes private state or administrative paths: {exposed}")


def check_secured_network(links, routes4, routes6):
    require({link["ifname"] for link in links} == {"lo"}, "Secured has an external interface")
    require(not routes4 and not routes6, "Secured has a default network route")


def _run(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=30)
    require(result.returncode == 0, f"Route assertion failed: {command}\n{result.stderr}")
    return result.stdout.strip()


@dataclass
class Route:
    remote: str
    project: str

    def base(self):
        require(self.project.startswith(PROJECT_PREFIX), "Route checks require a disposable pairing-route project")
        return ["incus", "--project", self.project]

    def run(self, role, command, identity=(0, 0)):
        uid, gid = identity
        target = f"{self.remote.rstrip(':')}:{role}"
        return _run(self.base() + ["exec", target, "-T", "--user", str(uid), "--group", str(gid), "--", *command])

    def python(self, role, program, *args, identity=(0, 0)):
        return json.loads(self.run(role, ["python3", "-c", program, *args], identity))

    def instances(self):
        data = json.loads(_run(self.base() + ["list", self.remote.rstrip(":") + ":", "--format", "json"]))
        return {item["name"]: item for item in data}

    def ids(self, role):
        program = """import grp, json, pwd
print(json.dumps({"broker": pwd.getpwnam("broker").pw_uid, "broker_gid": pwd.getpwnam("broker").pw_gid,
                  "peer": pwd.getpwnam("peer").pw_uid, "collab_peer": grp.getgrnam("collab-peer").gr_gid}))"""
        return self.python(role, program)

    def mount_options(self, role):
        program = """import json, sys
rows = [line.split() for line in open('/proc/mounts')]
print(json.dumps({row[1]: row[3].split(',') for row in rows if row[1] in sys.argv[1:]}))"""
        return {path: set(flags) for path, flags in self.python(role, program, INGRESS, EGRESS).items()}

    def write_refused(self, role):
        program = """import json, os, sys, uuid
out = {}
for path in sys.argv[1:]:
    probe = os.path.join(path, '.write-probe-' + uuid.uuid4().hex)
    try:
        open(probe, 'x').close()
    except OSError:
        out[path] = True
    else:
        os.unlink(probe)
        out[path] = False
print(json.dumps(out))"""
        return self.python(role, program, INGRESS, EGRESS)

    def stats(self, role, paths):
        program = """import json, os, sys
print(json.dumps({p: [os.lstat(p).st_mode, os.lstat(p).st_uid, os.lstat(p).st_gid] for p in sys.argv[1:] if os.path.lexists(p)}))"""
        return {path: tuple(row) for path, row in self.python(role, program, *paths).items()}

    def units(self, role):
        out = self.run(role, ["systemctl", "show", "--property=Id,ActiveState", *ROUTE_UNITS, *BROKER_UNITS])
        active, unit = {}, None
        for line in out.splitlines():
            key, _, value = line.partition("=")
            if key == "Id":
                unit = value
            elif key == "ActiveState" and unit:
                active[unit] = value
        return active

    def exposed(self):
        program = "import json, os, sys; print(json.dumps([p for p in sys.argv[1:] if os.path.lexists(p)]))"
        return self.python("connector", program, *PRIVATE_PATHS)

    def network(self):
        links = json.loads(self.run("secured", ["ip", "-json", "link"]))
        routes = [json.loads(self.run("secured", ["ip", "-json", family, "route", "show", "default"])) for family in ("-4", "-6")]
        return links, routes[0], routes[1]


def check_all(remote, project):
    route = Route(remote, project)
    instances = route.instances()
    check_instances(instances)
    sandbox_idmap_checks.check_disjoint(instances)
    check_secured_devices(instances["secured"])
    check_connector_devices(instances["connector"])
    volumes = check_shared_volumes(instances["secured"], instances["connector"])
    check_workspace_devices(instances["workspace"], volumes)
    check_secured_network(*route.network())
    for role in ("secured", "connector"):
        check_mount_options(role, route.mount_options(role))
        check_write_refused(role, route.write_refused(role))
    stats = route.stats("secured", [INGRESS, PAIRING_SOCKET, ENV])
    stats.update(route.stats("connector", [EGRESS, EGRESS_SOCKET]))
    check_endpoint_modes(stats, route.ids("secured"))
    for role in ROLES:
        check_units(role, route.units(role))
    check_private_paths_absent(route.exposed())
    print("PASS: the pairing route's boundaries hold: secured offline, one-way shares, private state absent from connector.", flush=True)
