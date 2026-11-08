"""Independent DNS wire fixtures for the public known-peer discovery boundary."""
import importlib.util
import ipaddress
from pathlib import Path
import struct
import sys

ROOT = Path(__file__).resolve().parents[2]
SERVICE = "_pairtask._tcp.local."


def engine(test):
    path = ROOT / "scripts" / "peer_discovery.py"
    test.assertTrue(path.is_file(), "known-peer host discovery engine is not implemented")
    spec = importlib.util.spec_from_file_location("peer_discovery", path)
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def name(value):
    return b"".join(bytes([len(part)]) + part.encode("ascii")
                    for part in value.rstrip(".").split(".")) + b"\0"


def record(owner, kind, data, ttl=60):
    return name(owner) + struct.pack("!HHIH", kind, 0x8001, ttl, len(data)) + data


def packet(records):
    return struct.pack("!6H", 0, 0x8400, 0, len(records), 0, 0) + b"".join(records)


def service_records(index=1, address="192.168.8.20", ttl=60):
    instance = "node%d.%s" % (index, SERVICE)
    host = "random%d.local." % index
    ip = ipaddress.ip_address(address)
    return [record(SERVICE, 12, name(instance), ttl),
            record(instance, 33, struct.pack("!3H", 0, 0, 7445) + name(host), ttl),
            record(host, 1 if ip.version == 4 else 28, ip.packed, ttl)]
