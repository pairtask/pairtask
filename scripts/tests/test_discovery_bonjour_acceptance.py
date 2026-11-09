"""Find on macOS consumes Bonjour callbacks without opening a multicast socket."""

import ctypes
import ctypes.util
from collections import namedtuple
import contextlib
import io
import socket
import unittest
from unittest import mock

from bonjour_api_fixture import BonjourAPI
from discovery_test_support import engine
from test_discovery_report_acceptance import helper


Service = namedtuple("Service", "instance interface name host txt ttl",
                     defaults=(b"0123456789abcdef", 14, "Office Mac", "192.168.1.164", None, 60))


class BonjourDiscoveryAcceptance(unittest.TestCase):
    def setUp(self):
        self.engine = engine(self)
        self.api = BonjourAPI()
        self.now = 100.0
        self.addCleanup(self.api.close)
        for patch in (
                mock.patch.object(self.engine.sys, "platform", "darwin"),
                mock.patch.object(ctypes, "CDLL", return_value=self.api),
                mock.patch.object(self.engine, "interface_addresses", return_value={"en0": ["192.168.1.117"]}),
                mock.patch.object(socket, "getaddrinfo", side_effect=AssertionError("blocking DNS in helper loop"))):
            patch.start()
            self.addCleanup(patch.stop)
        self.raw = mock.patch.object(self.engine, "Channel", side_effect=AssertionError("Find opened raw mDNS socket"))
        self.channel = self.raw.start()
        self.addCleanup(self.raw.stop)

    def start(self, advert=None):
        session = self.engine.Session(advert, clock=lambda: self.now)
        self.addCleanup(session.stop)
        session.update(False, True)
        self.assertEqual(len(self.api.refs("browse")), 1)
        browse = self.api.operations[self.api.refs("browse")[0]]
        self.assertEqual(browse["interface"], 0)
        self.assertEqual(browse["regtype"].rstrip(b"."), b"_pairtask._tcp")
        self.assertEqual(browse["domain"].rstrip(b"."), b"local")
        return session

    def pump(self, session):
        for _ in range(8):
            session.step(0)

    def add(self, session, **changes):
        service = Service(**changes)
        before = set(self.api.refs("resolve"))
        self.api.service(service.instance, service.interface)
        self.pump(session)
        refs = set(self.api.refs("resolve")) - before
        self.assertEqual(len(refs), 1)
        resolved = refs.pop()
        self.assertEqual(self.api.operations[resolved]["interface"], service.interface)
        before = set(self.api.refs("address"))
        self.api.resolved(resolved, name=service.name, txt=service.txt)
        self.pump(session)
        refs = set(self.api.refs("address")) - before
        if not refs:  # Invalid TXT can be rejected before resolving the address.
            return None
        self.assertEqual(len(refs), 1)
        address_ref = refs.pop()
        self.assertEqual(self.api.operations[address_ref]["interface"], service.interface)
        self.api.address(address_ref, host=service.host, ttl=service.ttl)
        self.pump(session)
        return address_ref

    def rows(self, session):
        return session.browser.invitations(self.now) if session.browser else []

    def test_named_result_uses_native_interface_port_and_utf8_without_raw_socket(self):
        session = self.start(self.engine.Advert(7446))
        self.add(session, name="Büro Mac")
        self.assertEqual(self.rows(session), [{"name": "Büro Mac", "host": "192.168.1.164",
                                               "port": 7445, "seen_at": self.now}])
        self.assertIn("192.168.1.164:7445", session.candidates())
        self.channel.assert_not_called()

    def test_removal_is_scoped_to_interface_and_last_removal_clears_result(self):
        session = self.start()
        self.add(session, interface=14)
        self.add(session, interface=15)
        self.api.service(interface=14, add=False)
        self.pump(session)
        self.assertTrue(self.rows(session), "removal on one interface hid a live service on another")
        self.api.service(interface=15, add=False)
        self.pump(session)
        self.assertEqual(self.rows(session), [])
        self.assertEqual(session.candidates(), [])
        self.assertEqual(len(self.api.operations), 1, "removed services left native handles open")

    def test_address_removal_and_expiry_clear_unusable_rows(self):
        session = self.start()
        address_ref = self.add(session, ttl=5)
        self.api.address(address_ref, add=False)
        self.pump(session)
        self.assertEqual(self.rows(session), [])
        self.api.address(address_ref, ttl=5)
        self.pump(session)
        self.assertTrue(self.rows(session))
        self.now += 6
        self.assertEqual(self.rows(session), [])

    def test_ipv6_and_ineligible_addresses_follow_existing_endpoint_rules(self):
        session = self.start()
        ref = self.add(session, host="127.0.0.1")
        self.assertEqual(self.rows(session), [])
        for host in ("0.0.0.0", "224.0.0.251", "169.254.8.9", "fe80::1"):
            self.api.address(ref, host=host)
        self.pump(session)
        self.assertEqual(self.rows(session), [])
        self.api.address(ref, host="fd00::164")
        self.pump(session)
        self.assertEqual(self.rows(session)[0]["host"], "fd00::164")
        self.assertIn("[fd00::164]:7445", session.candidates())

    def test_malformed_or_nonprintable_txt_never_becomes_named_invitation(self):
        session = self.start()
        for index, txt in enumerate((b"\x20name=short", b"\x06name=\xff", b"\x06name=\n", b"\x05name=", b"\x03v=1")):
            self.add(session, instance=("%016x" % index).encode(), txt=txt)
        self.assertEqual(self.rows(session), [])

    def test_own_advert_is_never_listed(self):
        advert = self.engine.Advert(7445)
        session = self.start(advert)
        self.api.service(advert.instance.split(".")[0].encode())
        self.pump(session)
        # Filtering at browse time is fine; otherwise complete the native chain.
        for ref in list(self.api.refs("resolve")):
            self.api.resolved(ref)
        self.pump(session)
        for ref in list(self.api.refs("address")):
            self.api.address(ref, host="192.168.1.117")
        self.pump(session)
        self.assertEqual(self.rows(session), [])
        self.assertEqual(session.candidates(), [])

    def test_service_flood_keeps_native_work_and_rows_bounded(self):
        session = self.start()
        for index in range(self.engine.CACHE_MAX * 3):
            self.api.service(("%016x" % index).encode())
            self.pump(session)
        self.assertLessEqual(len(self.api.operations), 1 + 2 * self.engine.CACHE_MAX,
                             "unresolved LAN services grew native descriptors without bound")
        self.assertLessEqual(len(self.rows(session)), self.engine.INVITATIONS_MAX)

    def test_browse_off_closes_all_handles_and_restarts_without_stale_results(self):
        session = self.start()
        self.add(session)
        self.assertTrue(self.rows(session))
        session.update(False, False)
        self.assertEqual(self.api.operations, {})
        self.assertEqual(self.rows(session), [])
        session.update(False, True)
        self.assertEqual(len(self.api.refs("browse")), 1)
        self.assertEqual(self.rows(session), [])
        session.stop()
        self.assertEqual(self.api.operations, {})

    def test_browse_failure_is_reported_without_claiming_find_started(self):
        host = helper()
        self.api.start_error = -65570  # kDNSServiceErr_PolicyDenied
        status = mock.Mock(data={})
        discovery = host.Discovery(host.Options(mock.Mock(), 0, status, None, "224.0.0.251", 5353))
        self.addCleanup(discovery.close)
        now = host.time.monotonic()
        wall = host.time.time()
        answer = {"contact_discovery": {"find": True, "find_generation": 7,
                                        "advertise": False, "browse": False, "ports": [],
                                        "expires_at": host.stamp(wall + 20)}}
        discovery.apply(host.engine.lease_from(answer, now, now, wall))
        report = host.report_fields(discovery.report())
        self.assertNotIn("contact_find_generation", report)
        self.assertTrue(any(call.kwargs.get("error") for call in status.update.call_args_list))
        self.assertEqual(self.api.operations, {})

    def finding(self):
        host = helper()
        discovery = host.Discovery(host.Options(mock.Mock(), 0, mock.Mock(data={}), None,
                                                "224.0.0.251", 5353))
        self.addCleanup(discovery.close)
        now, wall = host.time.monotonic(), host.time.time()
        answer = {"contact_discovery": {"find": True, "find_generation": 11,
                                        "advertise": False, "browse": False, "ports": [],
                                        "expires_at": host.stamp(wall + 20)}}
        discovery.apply(host.engine.lease_from(answer, now, now, wall))
        return host, discovery

    def test_native_results_reach_operator_report_and_expiry_releases_all_handles(self):
        host, discovery = self.finding()
        self.add(discovery.session)
        report = host.report_fields(discovery.report())
        self.assertEqual(report["contact_find_generation"], 11)
        self.assertEqual(report["contact_found"][0]["name"], "Office Mac")
        self.assertEqual(report["contact_found"][0]["port"], 7445)
        discovery.expire(discovery.lease.deadline + 1)
        self.assertEqual(self.api.operations, {})
        self.assertNotIn("contact_find_generation", host.report_fields(discovery.report()))

    def test_late_daemon_error_withdraws_current_scan_and_cleans_native_handles(self):
        host, discovery = self.finding()
        self.assertEqual(host.report_fields(discovery.report())["contact_find_generation"], 11)
        self.api.service(error=-65563)  # kDNSServiceErr_ServiceNotRunning
        self.pump(discovery.session)
        report = host.report_fields(discovery.report())
        self.assertNotIn("contact_find_generation", report,
                         "a failed daemon connection was still acknowledged as a live scan")
        self.assertEqual(self.api.operations, {})

    def assert_failed_scan(self, host, discovery):
        self.assertNotIn("contact_find_generation", host.report_fields(discovery.report()))
        self.assertEqual(self.api.operations, {})

    def test_resolve_start_failure_inside_c_callback_withdraws_scan(self):
        host, discovery = self.finding()
        self.api.start_error = -65570
        self.api.service()
        with contextlib.redirect_stderr(io.StringIO()):
            self.pump(discovery.session)
        self.assert_failed_scan(host, discovery)

    def resolving(self, discovery):
        self.api.service()
        self.pump(discovery.session)
        ref, = self.api.refs("resolve")
        return ref

    def test_address_start_failure_inside_c_callback_withdraws_scan(self):
        host, discovery = self.finding()
        ref = self.resolving(discovery)
        self.api.start_error = -65563
        self.api.resolved(ref)
        with contextlib.redirect_stderr(io.StringIO()):
            self.pump(discovery.session)
        self.assert_failed_scan(host, discovery)

    def test_resolve_callback_daemon_failure_withdraws_scan(self):
        host, discovery = self.finding()
        self.api.resolved(self.resolving(discovery), error=-65563)
        self.pump(discovery.session)
        self.assert_failed_scan(host, discovery)

    def test_address_callback_daemon_failure_withdraws_scan(self):
        host, discovery = self.finding()
        self.api.resolved(self.resolving(discovery))
        self.pump(discovery.session)
        ref, = self.api.refs("address")
        self.api.address(ref, error=-65570)
        self.pump(discovery.session)
        self.assert_failed_scan(host, discovery)

    def test_loop_reports_late_daemon_failure_promptly_in_status(self):
        host, discovery = self.finding()
        loop = host.Loop(discovery, 15)
        self.api.service(error=-65563)
        with mock.patch.object(loop, "dispatch"), contextlib.redirect_stderr(io.StringIO()):
            loop.tick(mock.Mock())
        self.assertTrue(any(call.kwargs.get("error") for call in discovery.status.update.call_args_list),
                        "late daemon failure stayed absent from discovery status")

    def test_linux_and_custom_multicast_endpoint_keep_udp_browser(self):
        self.raw.stop()
        for platform, group, port in (("linux", "224.0.0.251", 5353),
                                      ("darwin", "224.0.0.251", 55353),
                                      ("darwin", "239.1.2.3", 5353)):
            with self.subTest(platform=platform, group=group, port=port):
                channel = mock.Mock(sent=0)
                channel.receive.return_value = []
                with mock.patch.object(self.engine.sys, "platform", platform), \
                        mock.patch.object(self.engine, "Channel", return_value=channel) as create:
                    session = self.engine.Session(None, group, port, clock=lambda: self.now)
                    try:
                        session.update(False, True)
                        session.step(0)
                        create.assert_called_once_with(group, port)
                        self.assertIsInstance(session.browser, self.engine.Browser)
                        self.assertEqual(self.api.operations, {})
                        channel.send.assert_called()
                    finally:
                        session.stop()


if __name__ == "__main__":
    unittest.main()
