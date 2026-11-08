"""Builder diagnostics for #284 (not an acceptance test): what this host's
network does with the helper's multicast, printed so a native CI run shows
why a proof fails. It asserts nothing about the network."""

import json
import os
from pathlib import Path
import socket
import sys
import time
import unittest

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import peer_discovery as engine  # noqa: E402

NATIVE = (os.environ.get("PAIRTASK_MDNS_PROOF") == "1" or
          os.environ.get("PAIRTASK_MAC_MDNS_PROOF") == "1")


def option(sock, name):
    try:
        return int.from_bytes(sock.getsockopt(socket.IPPROTO_IP, name, 4)[:4], sys.byteorder)
    except OSError as error:
        return "error %s" % error.errno


def report_for(port):
    channel = engine.Channel(engine.GROUP, port)
    interfaces = engine.interface_addresses()
    channel.join([value for values in interfaces.values() for value in engine.ipv4(values)])
    report = {"platform": sys.platform, "interfaces": interfaces, "joined": sorted(channel.joined),
              "ttl": option(channel.sock, socket.IP_MULTICAST_TTL),
              "loop": option(channel.sock, socket.IP_MULTICAST_LOOP), "sends": {}}
    for interface in engine.Session.send_interfaces(type("S", (), {"interfaces": interfaces})()):
        report["sends"][interface] = channel.send(b"pairtask-diagnostic", interface)
    time.sleep(0.5)
    report["received"] = len(channel.receive(0.5))
    report["failures"] = sorted("%s errno %s" % key for key in channel.failures)
    channel.close()
    return report


@unittest.skipUnless(NATIVE, "native multicast diagnostics are opt-in")
class DiscoveryNativeDiagnostics(unittest.TestCase):
    def test_print_multicast_path(self):
        probe = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        probe.bind(("127.0.0.1", 0))
        port = probe.getsockname()[1]
        probe.close()
        print("\nDISCOVERY-DIAGNOSTICS " + json.dumps(report_for(port), sort_keys=True))


if __name__ == "__main__":
    unittest.main()
