import os
import socket
import subprocess
import struct
import time
import unittest

from discovery_host_fixture import OperatorSocket, helper_command, lease, run_helper


NATIVE = (os.environ.get("PAIRTASK_MDNS_PROOF") == "1" or
          os.environ.get("PAIRTASK_MAC_MDNS_PROOF") == "1")


@unittest.skipUnless(NATIVE, "native multicast proof is opt-in; enabled in mac-sharing CI")
class DiscoveryMulticastAcceptance(unittest.TestCase):
    def operator(self, value):
        fixture = OperatorSocket(value)
        self.addCleanup(fixture.close)
        return fixture

    def stop_process(self, process):
        if process.poll() is None:
            process.terminate()
        process.communicate(timeout=5)

    def environment(self, port):
        return dict(os.environ, PAIRTASK_DISCOVERY_GROUP="224.0.0.251",
                    PAIRTASK_DISCOVERY_PORT=str(port), PAIRTASK_DISCOVERY_TAILSCALE="none")

    def wait_for_lease(self, process, operator):
        deadline = time.monotonic() + 4
        while not operator.requests:
            if process.poll() is not None:
                self.fail("host discovery process failed: " + process.communicate()[1])
            if time.monotonic() > deadline:
                self.fail("host discovery never requested its broker lease")
            time.sleep(0.02)

    def test_real_advertisement_is_browsed_and_submitted_without_manual_address(self):
        reserve = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        reserve.bind(("224.0.0.251", 0))
        port = reserve.getsockname()[1]
        reserve.close()
        advertiser = self.operator(lease(advertise=True))
        browser = self.operator(lease(browse=True))
        process = subprocess.Popen(helper_command(advertiser, once=False, port=7445),
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
                                   env=self.environment(port))
        self.addCleanup(self.stop_process, process)
        self.wait_for_lease(process, advertiser)
        run_helper(self, browser, self.environment(port))
        submitted = [value for r in browser.requests if r.get("contact_op") == "candidates"
                     for value in r.get("contact_candidates", [])]
        self.assertTrue(any(value.endswith(":7445") for value in submitted),
                        "real multicast never reached the autonomous candidate operation")

    def test_real_helper_can_share_mdns_port_with_another_responder(self):
        responder = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.addCleanup(responder.close)
        responder.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
        if hasattr(socket, "SO_REUSEPORT"):
            responder.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEPORT, 1)
        responder.bind(("224.0.0.251", 5353))
        membership = socket.inet_aton("224.0.0.251") + socket.inet_aton("0.0.0.0")
        responder.setsockopt(socket.IPPROTO_IP, socket.IP_ADD_MEMBERSHIP, membership)
        responder.settimeout(0.2)
        advertiser = self.operator(lease(advertise=True))
        result = subprocess.run(helper_command(advertiser, port=7445),
                                capture_output=True, text=True, env=self.environment(5353), timeout=12)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(advertiser.requests)
        deadline = time.monotonic() + 2
        while time.monotonic() < deadline:
            try:
                data, sender = responder.recvfrom(9001)
            except socket.timeout:
                continue
            if b"_pairtask" in data and struct.pack("!3H", 0, 0, 7445) in data:
                self.assertEqual(sender[1], 5353, "advertisements must retain the mDNS source port")
                return
        self.fail("helper claimed success but shared-port responder saw no service advertisement")
