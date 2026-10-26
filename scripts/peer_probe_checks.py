"""Independent assertions for the disposable offline-TLS topology probe.

These functions require running Incus instances; importing or compiling this
module is not a successful topology test. The runner owns setup and teardown.
"""

import json
import subprocess


PEER_DIR = "/mnt/collab-peer"
PEER_SOCKET = PEER_DIR + "/gate.sock"


def _base(project):
    if not project.startswith(("pairtask-peer-probe-", "pairtask-smoke-")):
        raise ValueError("Peer checks require a disposable probe/smoke project")
    return ["incus", "--project", project]


def _run(command):
    result = subprocess.run(command, capture_output=True, text=True, timeout=20)
    if result.returncode:
        raise ValueError(f"Peer assertion command failed: {command}\n{result.stderr}")
    return result.stdout.strip()


def _execute(remote, project, role, *command, uid=0, gid=0):
    return _base(project) + ["exec", f"{remote.rstrip(':')}:{role}", "-T",
                             "--user", str(uid), "--group", str(gid), "--", *command]


def _instances(remote, project):
    data = json.loads(_run(_base(project) + ["list", remote.rstrip(":") + ":", "--format", "json"]))
    instances = {instance["name"]: instance for instance in data}
    if set(instances) != {"workspace", "secured", "connector"}:
        raise ValueError("Expected exactly the three disposable probe instances")
    return instances


def check_topology(remote, project):
    """Measure actual interfaces, routes and isolated container mappings."""
    instances = _instances(remote, project)
    secured_devices = instances["secured"]["expanded_devices"]
    if any(d.get("type") in ("nic", "proxy") for d in secured_devices.values()):
        raise ValueError("Secured acquired a NIC or proxy device")
    links = json.loads(_run(_execute(remote, project, "secured", "ip", "-json", "link")))
    if {link["ifname"] for link in links} != {"lo"}:
        raise ValueError("Secured has an external network interface")
    for family in ("-4", "-6"):
        routes = json.loads(_run(_execute(remote, project, "secured", "ip", "-json", family, "route", "show", "default")))
        if routes:
            raise ValueError("Secured has a default network route")
    mappings = []
    for role, instance in instances.items():
        config = instance["expanded_config"]
        if config.get("security.privileged", "false") != "false":
            raise ValueError(f"{role} is privileged")
        if config.get("security.idmap.isolated") != "true":
            raise ValueError(f"{role} does not use an isolated UID mapping")
        mapping = json.loads(config["volatile.idmap.current"])
        if not mapping:
            raise ValueError(f"{role} has no measured UID mapping")
        mappings.append(json.dumps(mapping, sort_keys=True))
    if len(set(mappings)) != 3:
        raise ValueError("Probe containers share a UID mapping")
    for role in ("secured", "connector"):
        config = instances[role]["expanded_config"]
        if config.get("security.nesting", "false") != "false":
            raise ValueError(f"{role} allows nesting")
        if config.get("security.guestapi", "true") != "false":
            raise ValueError(f"{role} exposes the Incus guest API")
    print("PASS: secured has only loopback and no default route; all UID mappings differ.", flush=True)


def _mount_options(remote, project, role):
    program = """import json
rows = [line.split() for line in open('/proc/mounts')]
print(json.dumps([row[3].split(',') for row in rows if row[1] == '/mnt/collab-peer']))
"""
    mounts = json.loads(_run(_execute(remote, project, role, "python3", "-c", program)))
    if len(mounts) != 1:
        raise ValueError(f"{role} does not have exactly one peer IPC mount")
    return set(mounts[0])


def check_mounts(remote, project):
    """Verify mounts from Incus metadata and from the actual mount table."""
    instances = _instances(remote, project)
    connector_devices = instances["connector"]["expanded_devices"]
    disks = [d for d in connector_devices.values() if d.get("type") == "disk"]
    if sorted(d.get("path") for d in disks) != ["/", PEER_DIR]:
        raise ValueError("Connector has a disk beyond its root and peer IPC")
    if any(d.get("type") not in ("disk", "nic") for d in connector_devices.values()):
        raise ValueError("Connector has an unexpected device")
    if sum(d.get("type") == "nic" for d in connector_devices.values()) != 1:
        raise ValueError("Connector must have one explicit network interface")
    peer_mounts = []
    for role in ("secured", "connector"):
        mounts = [d for d in instances[role]["expanded_devices"].values()
                  if d.get("type") == "disk" and d.get("path") == PEER_DIR]
        if len(mounts) != 1 or not mounts[0].get("pool"):
            raise ValueError(f"{role} peer IPC is not a dedicated managed volume")
        peer_mounts.append((mounts[0]["pool"], mounts[0]["source"]))
    if peer_mounts[0] != peer_mounts[1]:
        raise ValueError("Gate and connector do not share the same IPC volume")
    for device in instances["workspace"]["expanded_devices"].values():
        if device.get("path", "").startswith(PEER_DIR) or (device.get("pool"), device.get("source")) == peer_mounts[0]:
            raise ValueError("Workspace can see the peer IPC volume")
    if "ro" not in _mount_options(remote, project, "connector"):
        raise ValueError("Connector peer mount is writable")
    if "rw" not in _mount_options(remote, project, "secured"):
        raise ValueError("Secured peer mount is not writable")
    absent = ["/mnt/collab-ipc/broker.sock", "/run/collab-ai-operator/operator.sock",
              "/var/lib/collab-ai-secured/broker/broker.db", "/var/lib/incus/unix.socket",
              "/dev/incus/sock", "/var/run/docker.sock", "/var/lib/peerprobe"]
    program = "import json,os,sys; print(json.dumps([p for p in sys.argv[1:] if os.path.lexists(p)]))"
    exposed = json.loads(_run(_execute(remote, project, "connector", "python3", "-c", program, *absent)))
    if exposed:
        raise ValueError(f"Connector has private state or administrative paths: {exposed}")
    print("PASS: peer IPC is shared only with the connector, read-only there; private paths are absent.", flush=True)


def _check_gate_permissions(remote, project):
    gate_uid, gate_gid = _id(remote, project, "secured", "peergate")
    program = """import json,os,stat,sys
rows=[]
for path in sys.argv[1:]:
    info=os.lstat(path)
    rows.append({'path':path,'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid,
                 'socket':stat.S_ISSOCK(info.st_mode),'directory':stat.S_ISDIR(info.st_mode)})
print(json.dumps(rows))
"""
    rows = json.loads(_run(_execute(remote, project, "secured", "python3", "-c", program,
                                   PEER_DIR, PEER_SOCKET, "/var/lib/peerprobe", "/var/lib/peerprobe/gate.key")))
    for row, mode in zip(rows, (0o750, 0o660, 0o700, 0o600), strict=True):
        if (row["mode"], row["uid"], row["gid"]) != (mode, gate_uid, gate_gid):
            raise ValueError(f"Unexpected gate IPC/key ownership or permissions: {row}")
    if not rows[0]["directory"] or not rows[1]["socket"] or not rows[2]["directory"]:
        raise ValueError("Gate IPC or private state has an unexpected filesystem type")


def _id(remote, project, role, user):
    uid = int(_run(_execute(remote, project, role, "id", "-u", user)))
    gid = int(_run(_execute(remote, project, role, "id", "-g", user)))
    if uid == 0:
        raise ValueError(f"Probe user {user} is root")
    return uid, gid


def check_connect(remote, project):
    """Use real unprivileged connects; do not equate path visibility with access."""
    _check_gate_permissions(remote, project)
    peer_uid, _ = _id(remote, project, "connector", "peer")
    peer_group = _run(_execute(remote, project, "connector", "getent", "group", "collab-peer"))
    peer_gid = int(peer_group.split(":")[2])
    agent_uid, agent_gid = _id(remote, project, "workspace", "agent")
    program = """import errno,json,socket,sys
s=socket.socket(socket.AF_UNIX);s.settimeout(2)
try:
    s.connect(sys.argv[1]);print(json.dumps({'connected':True}))
except OSError as e:
    print(json.dumps({'connected':False,'errno':errno.errorcode.get(e.errno,str(e.errno))}))
finally:
    s.close()
"""
    for role, uid, gid, allowed in (("connector", peer_uid, peer_gid, True),
                                   ("workspace", agent_uid, agent_gid, False)):
        result = json.loads(_run(_execute(remote, project, role, "python3", "-c", program, PEER_SOCKET, uid=uid, gid=gid)))
        if result.get("connected") is not allowed:
            raise ValueError(f"Unexpected {role} peer socket access: {result}")
        if not allowed and result.get("errno") not in ("ENOENT", "EACCES", "EPERM"):
            raise ValueError(f"Agent connect failed for a reason other than isolation: {result}")
    # A read-only volume must still allow socket connect, but not replacement.
    program = """import errno,json,os,sys
try:
    fd=os.open(sys.argv[1],os.O_CREAT|os.O_EXCL|os.O_WRONLY,0o600);os.close(fd);os.unlink(sys.argv[1])
    print(json.dumps({'created':True}))
except OSError as e:
    print(json.dumps({'created':False,'errno':errno.errorcode.get(e.errno,str(e.errno))}))
"""
    result = json.loads(_run(_execute(remote, project, "connector", "python3", "-c", program,
                                     PEER_DIR + "/connector-must-not-create", uid=peer_uid, gid=peer_gid)))
    if result.get("created") is not False or result.get("errno") not in ("EROFS", "EACCES", "EPERM"):
        raise ValueError(f"Connector can modify peer IPC, or denial was inconclusive: {result}")
    print("PASS: connector user can connect but cannot alter IPC; workspace agent cannot connect.", flush=True)


def check_uid(record):
    """Record Linux peer credentials without mistaking mapped UID for identity."""
    if record.get("event") != "peer_credentials":
        raise ValueError("Expected a peer_credentials observation from the gate")
    for field in ("uid", "gid"):
        if type(record.get(field)) is not int or record[field] < 0:
            raise ValueError(f"Linux peer credential observation missing {field}")
    print("PASS: gate observed peer credentials " + json.dumps(record, sort_keys=True) +
          "; these are mapping observations, not authenticated workspace identities.", flush=True)
