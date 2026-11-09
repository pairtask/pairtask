"""Named invitations must reach the operator report, including a late browser."""

import datetime
import os
import socket
import subprocess
import time
import unittest

from discovery_host_fixture import OperatorSocket, helper_command, lease


NATIVE = (os.environ.get("PAIRTASK_MDNS_PROOF") == "1" or
          os.environ.get("PAIRTASK_MAC_MDNS_PROOF") == "1")


@unittest.skipUnless(NATIVE, "native multicast proof is opt-in")
class DiscoveryNamedHostAcceptance(unittest.TestCase):
    def operator(self, value):
        operator = OperatorSocket(value)
        self.addCleanup(operator.close)
        return operator

    def start_helper(self, operator, multicast_port, host_port):
        command = helper_command(operator, once=False, port=host_port) + ["--interval", "5s"]
        environment = dict(os.environ, PAIRTASK_DISCOVERY_GROUP="224.0.0.251",
                           PAIRTASK_DISCOVERY_PORT=str(multicast_port),
                           PAIRTASK_DISCOVERY_TAILSCALE="none")
        process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   text=True, env=environment)
        self.addCleanup(self.stop_helper, process)
        return process

    def stop_helper(self, process):
        if process.poll() is None:
            process.terminate()
        process.communicate(timeout=5)

    def wait_report(self, process, operator, predicate):
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            for request in list(operator.requests):
                if request.get("contact_op") == "discovery" and predicate(request):
                    return request
            if process.poll() is not None:
                self.fail("discovery helper exited: " + process.communicate()[1])
            time.sleep(0.05)
        self.fail("no matching discovery report: " + repr(operator.requests))

    def test_late_find_reports_named_invitation_without_candidate_probing(self):
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as reservation:
            reservation.bind(("224.0.0.251", 0))
            multicast_port = reservation.getsockname()[1]

        advertised = lease(seconds=60)
        advertised["invitation"] = {"id": "late-browser-proof", "name": "Office Mac"}
        advertiser = self.operator(advertised)
        advertising = self.start_helper(advertiser, multicast_port, 8445)
        self.wait_report(advertising, advertiser,
                         lambda r: r.get("contact_announcing", {}).get("sent") is True)
        # The browser must miss both startup announcements (at 0 and 1s).
        time.sleep(1.5)

        wanted = lease(seconds=60)
        wanted["find"] = True
        browser = self.operator(wanted)
        browsing = self.start_helper(browser, multicast_port, 0)
        report = self.wait_report(browsing, browser,
                                 lambda r: any(i.get("name") == "Office Mac"
                                               for i in r.get("contact_found", [])))
        found = [i for i in report["contact_found"] if i["name"] == "Office Mac"]
        self.assertEqual(len(found), 1)
        self.assertEqual(found[0]["port"], 8445)
        self.assertTrue(found[0]["host"])
        datetime.datetime.fromisoformat(found[0]["seen_at"].replace("Z", "+00:00"))
        self.assertFalse(any(r.get("contact_op") == "candidates" for r in browser.requests),
                         "Find alone must report invitations without probing candidates")


if __name__ == "__main__":
    unittest.main()
