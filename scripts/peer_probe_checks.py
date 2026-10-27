"""Independent assertions for the disposable offline-TLS topology probe.

Running these requires live Incus instances; syntax checks are not topology
validation. The runner owns provisioning and teardown.
"""

from dataclasses import dataclass
import json
import subprocess

import sandbox_idmap_checks

PEER_DIR = "/mnt/collab-peer"
PEER_SOCKET = PEER_DIR + "/gate.sock"
STATE_DIR = "/var/lib/peerprobe"


def require(condition, message):
    if not condition:
        raise ValueError(message)


def _run(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=20)
    require(result.returncode == 0, f"Peer assertion failed: {command}\n{result.stderr}")
    return result.stdout.strip()


@dataclass
class Probe:
    remote: str
    project: str

    def base(self):
        require(self.project.startswith(("pairtask-peer-probe-", "pairtask-smoke-")),
                "Peer checks require a disposable probe/smoke project")
        return ["incus", "--project", self.project]

    def run(self, role, command, identity=(0, 0)):
        uid, gid = identity
        prefix = self.base() + ["exec", f"{self.remote.rstrip(':')}:{role}", "-T",
                                "--user", str(uid), "--group", str(gid), "--"]
        return _run(prefix + command)

    def instances(self):
        data = json.loads(_run(self.base() + ["list", self.remote.rstrip(":") + ":", "--format", "json"]))
        instances = {item["name"]: item for item in data}
        require(set(instances) == {"workspace", "secured", "connector"},
                "Expected exactly the three disposable probe instances")
        return instances

    def identity(self, role, user):
        uid = int(self.run(role, ["id", "-u", user]))
        gid = int(self.run(role, ["id", "-g", user]))
        require(uid != 0, f"Probe user {user} is root")
        return uid, gid


def _control_network(probe, instance):
    devices = instance["expanded_devices"].values()
    require(not any(d.get("type") in ("nic", "proxy") for d in devices),
            "Secured acquired a NIC or proxy device")
    links = json.loads(probe.run("secured", ["ip", "-json", "link"]))
    require({link["ifname"] for link in links} == {"lo"}, "Secured has an external interface")
    for family in ("-4", "-6"):
        routes = json.loads(probe.run("secured", ["ip", "-json", family, "route", "show", "default"]))
        require(not routes, "Secured has a default network route")


def _control_config(role, instance):
    config = instance["expanded_config"]
    require(config.get("security.nesting", "false") == "false", f"{role} allows nesting")
    require(config.get("security.guestapi", "true") == "false", f"{role} exposes the Incus guest API")


def check_topology(remote, project):
    probe = Probe(remote, project)
    instances = probe.instances()
    _control_network(probe, instances["secured"])
    sandbox_idmap_checks.check_disjoint(instances)
    for role in ("secured", "connector"):
        _control_config(role, instances[role])
    print("PASS: secured has only loopback and no default route; host UID and GID ranges are disjoint.", flush=True)


def _mount_options(probe, role):
    program = """import json
rows = [line.split() for line in open('/proc/mounts')]
print(json.dumps([row[3].split(',') for row in rows if row[1] == '/mnt/collab-peer']))
"""
    mounts = json.loads(probe.run(role, ["python3", "-c", program]))
    require(len(mounts) == 1, f"{role} does not have exactly one peer IPC mount")
    return set(mounts[0])


def _connector_devices(instance):
    devices = list(instance["expanded_devices"].values())
    disks = [d for d in devices if d.get("type") == "disk"]
    require(sorted(d.get("path") for d in disks) == ["/", PEER_DIR],
            "Connector has a disk beyond root and peer IPC")
    require(all(d.get("type") in ("disk", "nic") for d in devices), "Connector has an unexpected device")
    require(sum(d.get("type") == "nic" for d in devices) == 1, "Connector needs one explicit NIC")


def _peer_volume(role, instance):
    disks = [d for d in instance["expanded_devices"].values() if d.get("type") == "disk"]
    mounts = [d for d in disks if d.get("path") == PEER_DIR]
    require(len(mounts) == 1, f"{role} needs one dedicated peer mount")
    require(bool(mounts[0].get("pool")), f"{role} peer IPC is not a managed volume")
    return mounts[0]["pool"], mounts[0]["source"]


def _workspace_excludes_peer(instance, volume):
    for device in instance["expanded_devices"].values():
        require(not device.get("path", "").startswith(PEER_DIR), "Workspace exposes the peer path")
        source = device.get("pool"), device.get("source")
        require(source != volume, "Workspace exposes the peer volume under another path")


def _private_paths_absent(probe):
    paths = ["/mnt/collab-ipc/broker.sock", "/run/collab-ai-operator/operator.sock",
             "/var/lib/collab-ai-secured/broker/broker.db", "/var/lib/incus/unix.socket",
             "/dev/incus/sock", "/var/run/docker.sock", STATE_DIR]
    program = "import json,os,sys; print(json.dumps([p for p in sys.argv[1:] if os.path.lexists(p)]))"
    exposed = json.loads(probe.run("connector", ["python3", "-c", program, *paths]))
    require(not exposed, f"Connector exposes private state or administrative paths: {exposed}")


def check_mounts(remote, project):
    probe = Probe(remote, project)
    instances = probe.instances()
    _connector_devices(instances["connector"])
    volume = _peer_volume("secured", instances["secured"])
    require(volume == _peer_volume("connector", instances["connector"]), "Peer IPC volumes differ")
    _workspace_excludes_peer(instances["workspace"], volume)
    require("ro" in _mount_options(probe, "connector"), "Connector peer mount is writable")
    require("rw" in _mount_options(probe, "secured"), "Secured peer mount is not writable")
    _private_paths_absent(probe)
    print("PASS: peer IPC is exclusive to the connector, read-only there; private paths are absent.", flush=True)


def _gate_permissions(probe):
    uid, gid = probe.identity("secured", "peergate")
    program = """import json,os,stat,sys
rows=[]
for path in sys.argv[1:]:
    info=os.lstat(path)
    rows.append({'path':path,'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid,
                 'socket':stat.S_ISSOCK(info.st_mode),'directory':stat.S_ISDIR(info.st_mode)})
print(json.dumps(rows))
"""
    paths = [PEER_DIR, PEER_SOCKET, STATE_DIR, STATE_DIR + "/gate.key"]
    rows = json.loads(probe.run("secured", ["python3", "-c", program, *paths]))
    for row, mode in zip(rows, (0o750, 0o660, 0o700, 0o600), strict=True):
        require((row["mode"], row["uid"], row["gid"]) == (mode, uid, gid),
                f"Unexpected gate permissions: {row}")
    require(rows[0]["directory"], "Peer IPC path is not a directory")
    require(rows[1]["socket"], "Peer endpoint is not a socket")
    require(rows[2]["directory"], "Private gate state is not a directory")


def _connect_as(probe, role, identity):
    program = """import errno,json,socket,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(2)
try:
    s.connect(sys.argv[1]);print(json.dumps({'connected':True}))
except OSError as e:
    print(json.dumps({'connected':False,'errno':errno.errorcode.get(e.errno,str(e.errno))}))
finally:
    s.close()
"""
    return json.loads(probe.run(role, ["python3", "-c", program, PEER_SOCKET], identity))


def _connector_cannot_write(probe, identity):
    program = """import errno,json,os,sys
try:
    fd=os.open(sys.argv[1],os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd);os.unlink(sys.argv[1])
    print(json.dumps({'created':True}))
except OSError as e:
    print(json.dumps({'created':False,'errno':errno.errorcode.get(e.errno,str(e.errno))}))
"""
    command = ["python3", "-c", program, PEER_DIR + "/connector-must-not-create"]
    result = json.loads(probe.run("connector", command, identity))
    require(result.get("created") is False, "Connector can modify peer IPC")
    require(result.get("errno") in ("EROFS", "EACCES", "EPERM"), f"IPC write denial inconclusive: {result}")


def check_connect(remote, project):
    probe = Probe(remote, project)
    _gate_permissions(probe)
    peer_uid, _ = probe.identity("connector", "peer")
    group = probe.run("connector", ["getent", "group", "collab-peer"])
    peer_identity = peer_uid, int(group.split(":")[2])
    allowed = _connect_as(probe, "connector", peer_identity)
    require(allowed.get("connected") is True, f"Connector cannot reach peer socket: {allowed}")
    denied = _connect_as(probe, "workspace", probe.identity("workspace", "agent"))
    require(denied.get("connected") is False, "Workspace agent can reach peer socket")
    require(denied.get("errno") in ("ENOENT", "EACCES", "EPERM"), f"Agent denial inconclusive: {denied}")
    _connector_cannot_write(probe, peer_identity)
    print("PASS: connector can connect but cannot alter IPC; workspace agent cannot connect.", flush=True)


def check_uid(record):
    require(record.get("event") == "peer_credentials", "Expected a peer_credentials observation")
    for field in ("uid", "gid"):
        value = record.get(field)
        require(type(value) is int, f"Linux peer credential observation missing {field}")
        require(value >= 0, f"Linux peer credential observation invalid: {field}")
    print("PASS: gate observed " + json.dumps(record, sort_keys=True) +
          "; mapped credentials are observations, not authenticated workspace identities.", flush=True)
